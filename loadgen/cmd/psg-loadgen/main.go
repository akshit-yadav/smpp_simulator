// Command psg-loadgen is an open-loop, windowed SMPP load generator. It sends
// submit_sm at a fixed rate regardless of how fast responses come back (so a
// slow server shows up as latency, not as lower offered load), records
// submit_sm_resp latency and DLR delay, and answers deliver_sm.
//
//	psg-loadgen -addr 127.0.0.1:20001 -system-id vendora -password secret1 \
//	    -binds 8 -tps 25000 -duration 60s
//
// The same engine runs PSG's panel-driven client simulator (psg-tester).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/akshit-yadav/smpp_simulator/loadgen/internal/client"
)

func main() {
	var (
		addr, systemID, password, src, dstPrefix, out, netDelay, respDelay string
		binds, tps, window                                                 int
		duration, report, drain                                            time.Duration
		dlr, sim                                                           bool
	)
	flag.StringVar(&addr, "addr", "127.0.0.1:20001", "gateway host:port")
	flag.StringVar(&systemID, "system-id", "", "system_id")
	flag.StringVar(&password, "password", "", "password")
	flag.IntVar(&binds, "binds", 4, "transceiver sessions")
	flag.IntVar(&tps, "tps", 1000, "offered submit_sm per second, across all binds")
	flag.DurationVar(&duration, "duration", 30*time.Second, "how long to send")
	flag.IntVar(&window, "window", 1000, "max unacknowledged submit_sm per session")
	flag.BoolVar(&dlr, "dlr", true, "request DLRs (registered_delivery=1)")
	flag.StringVar(&src, "src", "PSGTEST", "source address")
	flag.StringVar(&dstPrefix, "dst-prefix", "9198", "destination prefix; 8 random digits are appended")
	flag.DurationVar(&report, "report", 5*time.Second, "progress report interval")
	flag.DurationVar(&drain, "drain", 5*time.Second, "after sending, wait this long for responses and DLRs")
	flag.StringVar(&out, "out", "", "write the final summary as JSON to this file")
	flag.StringVar(&netDelay, "net-delay", "", "network delay on every PDU each way, in ms: N or MIN-MAX")
	flag.StringVar(&respDelay, "resp-delay", "", "delay before answering deliver_sm, in ms: N or MIN-MAX")
	flag.BoolVar(&sim, "simulator", false, "mark binds as PSG simulator traffic (left out of PSG's reports)")
	flag.Parse()
	nd, err1 := parseDelay(netDelay)
	rd, err2 := parseDelay(respDelay)
	if systemID == "" || tps <= 0 || binds <= 0 || err1 != nil || err2 != nil {
		flag.Usage()
		os.Exit(2)
	}
	log.SetFlags(log.Ltime)

	spec := client.Spec{
		Targets:  []client.Target{{Name: addr, Addr: addr, SystemID: systemID, Password: password, Sessions: binds}},
		TPS:      float64(tps),
		Duration: duration, Window: window, Src: src, DstPrefix: dstPrefix, Drain: drain,
		Text: "PSG load test {seq}", NetDelay: nd, RespDelay: rd, Simulator: sim,
	}
	if dlr {
		spec.RegisteredDelivery = 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var lastReport time.Time
	var acc client.Second
	var secs int
	hooks := client.Hooks{Progress: func(p client.Progress) {
		switch p.Phase {
		case "binding":
			return
		case "draining":
			if !lastReport.IsZero() {
				log.Printf("sending done; draining responses and DLRs for up to %v", drain)
				lastReport = time.Time{}
			}
			return
		}
		if lastReport.IsZero() && secs == 0 {
			log.Printf("bound %d sessions to %s; offering %d TPS for %v (window %d, dlr %v)", binds, addr, tps, duration, window, dlr)
			lastReport = time.Now()
		}
		acc.Sent += p.Last.Sent
		acc.OK += p.Last.OK
		acc.DLRs += p.Last.DLRs
		acc.P50us, acc.P99us = max(acc.P50us, p.Last.P50us), max(acc.P99us, p.Last.P99us)
		secs++
		if time.Since(lastReport) >= report-50*time.Millisecond {
			n := float64(secs)
			log.Printf("sent %6.0f/s  ok %6.0f/s  dlr %6.0f/s | resp p50 %s p99 %s (worst second) | errors %d",
				float64(acc.Sent)/n, float64(acc.OK)/n, float64(acc.DLRs)/n, us(acc.P50us), us(acc.P99us), p.Errors)
			acc, secs, lastReport = client.Second{}, 0, time.Now()
		}
	}}
	res, err := client.Run(ctx, spec, hooks)
	if err != nil {
		log.Fatal(err)
	}
	print(res, spec)
	if out != "" {
		data, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(out, data, 0o644); err != nil {
			log.Printf("write %s: %v", out, err)
		}
	}
	if res.Unexpected > 0 || res.BindFailed > 0 {
		os.Exit(1)
	}
}

// parseDelay reads "" (off), "N" or "MIN-MAX" milliseconds.
func parseDelay(s string) (client.Delay, error) {
	if s == "" {
		return client.Delay{}, nil
	}
	lo, hi, ranged := strings.Cut(s, "-")
	a, err := strconv.Atoi(lo)
	if err != nil {
		return client.Delay{}, err
	}
	b := a
	if ranged {
		if b, err = strconv.Atoi(hi); err != nil || b < a {
			return client.Delay{}, fmt.Errorf("invalid delay %q", s)
		}
	}
	return client.Delay{MinMS: a, MaxMS: b}, nil
}

func us(v int64) string { return (time.Duration(v) * time.Microsecond).String() }

func print(r *client.Result, spec client.Spec) {
	fmt.Println()
	fmt.Println("=== psg-loadgen summary ===")
	fmt.Printf("offered        %.0f TPS for %v over %d binds (window %d)\n", spec.TPS, spec.Duration, r.Sessions, spec.Window)
	if r.Stopped != "" {
		fmt.Printf("stopped early  %s\n", r.Stopped)
	}
	fmt.Printf("sent           %d (%.0f TPS achieved), not sent %d\n", r.Sent, r.AchievedTPS, r.NotSent)
	fmt.Printf("responses      ok %d, throttled %d, rejected %d, unanswered %d\n", r.OK, r.Throttled, r.Rejected, r.Unanswered)
	l := r.Latency
	fmt.Printf("resp latency   p50 %s  p99 %s  p99.9 %s  max %s (bind round trip %.2fms)\n", us(l.P50), us(l.P99), us(l.P999), us(l.Max), r.BindRTTms)
	if spec.RegisteredDelivery != 0 {
		keys := make([]string, 0, len(r.DLRStatus))
		for k := range r.DLRStatus {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("DLRs           %d received, %d missing, %d unmatched\n", r.DLRs, r.DLRsMissing, r.DLRUnmatched)
		for _, k := range keys {
			fmt.Printf("  %-12s %d\n", k, r.DLRStatus[k])
		}
		fmt.Printf("DLR delay      p50 %dms  p99 %dms (from submit_sm_resp)\n", r.DLRDelayP50, r.DLRDelayP99)
	}
	fmt.Printf("unexpected     %d\n", r.Unexpected)
}
