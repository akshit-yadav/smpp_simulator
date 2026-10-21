// Command fsg-loadgen is an open-loop, windowed SMPP load generator. It sends
// submit_sm at a fixed rate regardless of how fast responses come back (so a
// slow server shows up as latency, not as lower offered load), records
// submit_sm_resp latency and DLR delay, and answers deliver_sm.
//
//	fsg-loadgen -addr 127.0.0.1:20001 -system-id vendora -password secret1 \
//	    -binds 8 -tps 25000 -duration 60s
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type options struct {
	Addr      string        `json:"addr"`
	SystemID  string        `json:"system_id"`
	Password  string        `json:"-"`
	Binds     int           `json:"binds"`
	TPS       int           `json:"tps"`
	Duration  time.Duration `json:"duration"`
	Window    int           `json:"window"`
	DLR       bool          `json:"dlr"`
	Src       string        `json:"src"`
	DstPrefix string        `json:"dst_prefix"`
	Report    time.Duration `json:"report"`
	Drain     time.Duration `json:"drain"`
	Out       string        `json:"-"`
}

func main() {
	var o options
	flag.StringVar(&o.Addr, "addr", "127.0.0.1:20001", "gateway host:port")
	flag.StringVar(&o.SystemID, "system-id", "", "system_id")
	flag.StringVar(&o.Password, "password", "", "password")
	flag.IntVar(&o.Binds, "binds", 4, "transceiver sessions")
	flag.IntVar(&o.TPS, "tps", 1000, "offered submit_sm per second, across all binds")
	flag.DurationVar(&o.Duration, "duration", 30*time.Second, "how long to send")
	flag.IntVar(&o.Window, "window", 1000, "max unacknowledged submit_sm per session")
	flag.BoolVar(&o.DLR, "dlr", true, "request DLRs (registered_delivery=1)")
	flag.StringVar(&o.Src, "src", "FSGTEST", "source address")
	flag.StringVar(&o.DstPrefix, "dst-prefix", "9198", "destination prefix; 8 random digits are appended")
	flag.DurationVar(&o.Report, "report", 5*time.Second, "progress report interval")
	flag.DurationVar(&o.Drain, "drain", 5*time.Second, "after sending, wait this long for responses and DLRs")
	flag.StringVar(&o.Out, "out", "", "write the final summary as JSON to this file")
	flag.Parse()
	if o.SystemID == "" || o.TPS <= 0 || o.Binds <= 0 {
		flag.Usage()
		os.Exit(2)
	}
	log.SetFlags(log.Ltime)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st := newStats()
	conns := make([]*conn, o.Binds)
	for i := range conns {
		c, err := dial(o, st)
		if err != nil {
			log.Fatalf("bind %d: %v", i+1, err)
		}
		conns[i] = c
	}
	log.Printf("bound %d sessions to %s; offering %d TPS for %v (window %d, dlr %v)",
		o.Binds, o.Addr, o.TPS, o.Duration, o.Window, o.DLR)

	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() { defer wg.Done(); c.sendLoop() }()
	}

	reportDone := make(chan struct{})
	go func() { st.reportLoop(o.Report, reportDone); close(reportDone) }()

	schedule(ctx, o, conns)
	for _, c := range conns {
		close(c.queue)
	}
	wg.Wait()

	log.Printf("sending done; draining responses and DLRs for up to %v", o.Drain)
	deadline := time.After(o.Drain)
	for !st.drained(o.DLR) {
		select {
		case <-deadline:
		case <-time.After(50 * time.Millisecond):
			continue
		}
		break
	}
	reportDone <- struct{}{}
	<-reportDone
	for _, c := range conns {
		c.unbind()
	}

	sum := st.summary(o)
	sum.print()
	if o.Out != "" {
		data, _ := json.MarshalIndent(sum, "", "  ")
		if err := os.WriteFile(o.Out, data, 0o644); err != nil {
			log.Printf("write %s: %v", o.Out, err)
		}
	}
	if sum.Unexpected > 0 {
		os.Exit(1)
	}
}

// schedule releases submits at a constant rate, 1 ms at a time, round-robin
// over sessions. It never waits for responses (open loop).
func schedule(ctx context.Context, o options, conns []*conn) {
	start := time.Now()
	end := start.Add(o.Duration)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	var sent int64
	next := 0
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if now.After(end) {
				return
			}
			due := int64(now.Sub(start).Seconds()*float64(o.TPS)) - sent
			for ; due > 0; due-- {
				select {
				case conns[next].queue <- now:
				default:
					conns[next].st.queueFull.Add(1) // sender behind: offered load not met
				}
				sent++
				next = (next + 1) % len(conns)
			}
		}
	}
}
