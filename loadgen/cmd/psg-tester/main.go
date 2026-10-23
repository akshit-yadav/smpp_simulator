// Command psg-tester is PSG's SMPP client simulator agent. The admin panel
// queues test runs; the tester picks them up, binds to the chosen gateways,
// sends at the chosen rate with the chosen latency, and reports progress,
// errors and the result. It only ever calls the control plane's HTTP API
// (outbound), so it can run on another machine behind NAT or a firewall.
//
// Settings (environment):
//
//	PSG_CONTROL_URL      control plane, e.g. http://10.0.0.5:8080
//	PSG_TESTER_TOKEN     shared secret for the tester API
//	PSG_TESTER_ID        1-255, unique per tester (default 1)
//	PSG_TESTER_NAME      shown in the panel (default: host name)
//	PSG_TESTER_TRACE_DIR temporary per-PDU traces, deleted after each run (default /tmp/psg-tester)
//	PSG_TESTER_MIN_MEMORY_MB  stop a run when the host has less available (default 500)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/akshit-yadav/smpp_simulator/loadgen/internal/client"
)

var version = "dev"

// keepAlive is how often a run with no fresh progress re-reports its last.
const keepAlive = 10 * time.Second

type agent struct {
	url, token string
	id         int
	name, ip   string
	traceDir   string
	minMemMB   int64
	http       *http.Client
	log        *slog.Logger
	running    atomic.Int64 // run ID, 0 = idle
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	a := &agent{
		url:      strings.TrimRight(env("PSG_CONTROL_URL", "http://127.0.0.1:8080"), "/"),
		token:    os.Getenv("PSG_TESTER_TOKEN"),
		name:     env("PSG_TESTER_NAME", hostname()),
		ip:       localIP(),
		traceDir: env("PSG_TESTER_TRACE_DIR", "/tmp/psg-tester"),
		http:     &http.Client{Timeout: 10 * time.Second},
		log:      log,
	}
	var err error
	if a.id, err = strconv.Atoi(env("PSG_TESTER_ID", "1")); err != nil || a.id < 1 || a.id > 255 {
		fmt.Fprintln(os.Stderr, "PSG_TESTER_ID must be 1-255")
		os.Exit(2)
	}
	if a.minMemMB, err = strconv.ParseInt(env("PSG_TESTER_MIN_MEMORY_MB", "500"), 10, 64); err != nil {
		fmt.Fprintln(os.Stderr, "PSG_TESTER_MIN_MEMORY_MB must be a number")
		os.Exit(2)
	}
	if a.token == "" {
		fmt.Fprintln(os.Stderr, "PSG_TESTER_TOKEN is required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("tester started", "id", a.id, "name", a.name, "ip", a.ip, "control_url", a.url, "version", version)
	go a.heartbeats(ctx)
	a.jobs(ctx)
	log.Info("tester stopped")
}

// job is a run as the control plane hands it out.
type job struct {
	RunID              int64           `json:"run_id"`
	TPS                float64         `json:"tps"`
	DurationS          int             `json:"duration_s"`
	RampUpS            int             `json:"ramp_up_s"`
	Window             int             `json:"window"`
	RegisteredDelivery byte            `json:"registered_delivery"`
	Text               string          `json:"text"`
	Encoding           string          `json:"encoding"`
	NetDelay           client.Delay    `json:"net_delay"`
	RespDelay          client.Delay    `json:"resp_delay"`
	Trace              bool            `json:"trace"`
	Targets            []client.Target `json:"targets"`
}

type targetWithPassword struct {
	client.Target
	Password string `json:"password"`
}

func (a *agent) jobs(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var raw struct {
			job
			Targets []targetWithPassword `json:"targets"`
		}
		found, err := a.call(ctx, http.MethodGet, fmt.Sprintf("/internal/v1/testers/%d/job", a.id), nil, &raw)
		if err != nil {
			a.log.Warn("job poll failed", "err", err)
			continue
		}
		if !found {
			continue
		}
		j := raw.job
		j.Targets = make([]client.Target, len(raw.Targets))
		for i, tp := range raw.Targets {
			j.Targets[i] = tp.Target
			j.Targets[i].Password = tp.Password
		}
		a.run(ctx, j)
	}
}

func (a *agent) run(ctx context.Context, j job) {
	a.running.Store(j.RunID)
	defer a.running.Store(0)
	a.log.Info("run started", "run", j.RunID, "targets", len(j.Targets), "tps", j.TPS, "duration_s", j.DurationS)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	base := fmt.Sprintf("/internal/v1/tests/%d", j.RunID)

	spec := client.Spec{
		Targets: j.Targets, TPS: j.TPS, Duration: time.Duration(j.DurationS) * time.Second,
		RampUp: time.Duration(j.RampUpS) * time.Second, Window: j.Window,
		RegisteredDelivery: j.RegisteredDelivery, Text: j.Text, UCS2: j.Encoding == "ucs2",
		NetDelay: j.NetDelay, RespDelay: j.RespDelay, Simulator: true,
	}
	if j.Trace {
		spec.TraceDir = fmt.Sprintf("%s/run-%d", a.traceDir, j.RunID)
	}
	posting := atomic.Bool{}
	var last atomic.Pointer[client.Progress]
	var postedAt atomic.Int64 // unix ns
	post := func(p client.Progress) {
		if !posting.CompareAndSwap(false, true) {
			return // the last update is still on its way: skip this one
		}
		postedAt.Store(time.Now().UnixNano())
		go func() {
			defer posting.Store(false)
			var reply struct {
				Stop bool `json:"stop"`
			}
			body := map[string]any{"progress": p, "mem_available_mb": memAvailableMB()}
			if _, err := a.call(ctx, http.MethodPost, base+"/progress", body, &reply); err == nil && reply.Stop {
				a.log.Info("run stopped from the panel", "run", j.RunID)
				cancel()
			}
		}()
	}
	// Progress stops once sending ends, but unbinding, reading the PDU trace
	// back and delivering the result take a while: keep the run alive, or the
	// control plane fails it as stale (60 s) and drops the late result.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		t := time.NewTicker(keepAlive)
		defer t.Stop()
		for {
			select {
			case <-finished:
				return
			case <-t.C:
			}
			if p := last.Load(); p != nil && time.Since(time.Unix(0, postedAt.Load())) >= keepAlive {
				q := *p
				q.Last = client.Second{} // no new timeline point
				post(q)
			}
		}
	}()
	hooks := client.Hooks{
		Progress: func(p client.Progress) {
			last.Store(&p)
			post(p)
		},
		Errors: func(batch []client.ErrorRecord) {
			if _, err := a.call(ctx, http.MethodPost, base+"/errors", batch, nil); err != nil {
				a.log.Warn("error upload failed", "run", j.RunID, "err", err)
			}
		},
		LowMemory: func() bool { return memAvailableMB() < a.minMemMB },
	}
	res, err := client.Run(runCtx, spec, hooks)
	body := map[string]any{"result": res}
	if err != nil {
		body = map[string]any{"error": err.Error()}
	}
	for attempt := range 30 { // keep the result: retry while the control plane is unreachable
		if _, err := a.call(ctx, http.MethodPost, base+"/result", body, nil); err == nil {
			break
		} else if attempt == 29 || ctx.Err() != nil {
			a.log.Error("result could not be delivered", "run", j.RunID, "err", err)
			return
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		a.log.Error("run failed", "run", j.RunID, "err", err)
		return
	}
	a.log.Info("run finished", "run", j.RunID, "sent", res.Sent, "ok", res.OK, "errors", res.Errors, "stopped", res.Stopped)
}

func (a *agent) heartbeats(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	failing := false
	for {
		body := map[string]any{
			"name": a.name, "ip": a.ip, "version": version,
			"mem_available_mb": memAvailableMB(), "running": a.running.Load(),
		}
		_, err := a.call(ctx, http.MethodPost, fmt.Sprintf("/internal/v1/testers/%d/heartbeat", a.id), body, nil)
		switch {
		case err != nil && !failing && ctx.Err() == nil:
			a.log.Warn("heartbeat failed; control plane unreachable", "err", err)
			failing = true
		case err == nil && failing:
			a.log.Info("heartbeat restored")
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// call sends JSON and decodes the reply into out. found is false on 204.
func (a *agent) call(ctx context.Context, method, path string, in, out any) (found bool, err error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return false, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.url+path, body)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return false, nil
	case resp.StatusCode >= 300:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return false, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	case out != nil:
		return true, json.NewDecoder(resp.Body).Decode(out)
	}
	return true, nil
}

// memAvailableMB is the host's available memory (Linux), or a large number
// when unknown so the guard never trips by mistake.
func memAvailableMB() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 1 << 30
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemAvailable:" {
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			return kb / 1024
		}
	}
	return 1 << 30
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// localIP is the address this host uses to reach the internet (UDP "dial"
// sends nothing).
func localIP() string {
	c, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}
