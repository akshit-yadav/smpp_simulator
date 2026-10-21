package main

import (
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
)

type stats struct {
	start time.Time

	sent, ok, throttled, rejected, unexpected atomic.Int64
	windowFull, queueFull, mo                 atomic.Int64
	dlrs, dlrUnmatched                        atomic.Int64

	mu       sync.Mutex
	resp     *hdrhistogram.Histogram // µs, whole run
	respIv   *hdrhistogram.Histogram // µs, current interval
	dlrDelay *hdrhistogram.Histogram // ms, whole run
	dlrStat  map[string]int64
	awaiting map[string]time.Time // message_id -> submit_sm_resp time
}

func newStats() *stats {
	return &stats{
		start:    time.Now(),
		resp:     hdrhistogram.New(1, 60_000_000, 3),
		respIv:   hdrhistogram.New(1, 60_000_000, 3),
		dlrDelay: hdrhistogram.New(1, 3_600_000, 3),
		dlrStat:  map[string]int64{},
		awaiting: map[string]time.Time{},
	}
}

func (s *stats) recordResp(d time.Duration) {
	us := max(d.Microseconds(), 1)
	s.mu.Lock()
	_ = s.resp.RecordValue(us)
	_ = s.respIv.RecordValue(us)
	s.mu.Unlock()
}

func (s *stats) trackDLR(id string, at time.Time) {
	s.mu.Lock()
	s.awaiting[id] = at
	s.mu.Unlock()
}

func (s *stats) recordDLR(id, stat string, at time.Time) {
	s.dlrs.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dlrStat[stat]++
	sentAt, ok := s.awaiting[id]
	if !ok {
		s.dlrUnmatched.Add(1)
		return
	}
	delete(s.awaiting, id)
	_ = s.dlrDelay.RecordValue(max(at.Sub(sentAt).Milliseconds(), 1))
}

// drained reports whether every submit has a response (and, with DLRs, every
// accepted message has its DLR).
func (s *stats) drained(dlr bool) bool {
	answered := s.ok.Load() + s.throttled.Load() + s.rejected.Load()
	if answered < s.sent.Load() {
		return false
	}
	if !dlr {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.awaiting) == 0
}

// reportLoop prints one line per interval until signalled on done.
func (s *stats) reportLoop(every time.Duration, done chan struct{}) {
	t := time.NewTicker(every)
	defer t.Stop()
	var lastSent, lastOK, lastDLR int64
	last := time.Now()
	for {
		select {
		case <-done:
			return
		case now := <-t.C:
			secs := now.Sub(last).Seconds()
			sent, ok, dlrs := s.sent.Load(), s.ok.Load(), s.dlrs.Load()
			s.mu.Lock()
			p50, p99, p999 := s.respIv.ValueAtQuantile(50), s.respIv.ValueAtQuantile(99), s.respIv.ValueAtQuantile(99.9)
			s.respIv.Reset()
			s.mu.Unlock()
			log.Printf("sent %6.0f/s  ok %6.0f/s  dlr %6.0f/s | resp p50 %s p99 %s p99.9 %s | throttled %d rejected %d window-full %d unexpected %d",
				float64(sent-lastSent)/secs, float64(ok-lastOK)/secs, float64(dlrs-lastDLR)/secs,
				us(p50), us(p99), us(p999),
				s.throttled.Load(), s.rejected.Load(), s.windowFull.Load()+s.queueFull.Load(), s.unexpected.Load())
			lastSent, lastOK, lastDLR, last = sent, ok, dlrs, now
		}
	}
}

func us(v int64) string { return (time.Duration(v) * time.Microsecond).String() }

type summary struct {
	Options       options          `json:"options"`
	Elapsed       string           `json:"elapsed"`
	Sent          int64            `json:"sent"`
	AchievedTPS   float64          `json:"achieved_tps"`
	OK            int64            `json:"ok"`
	Throttled     int64            `json:"throttled"`
	Rejected      int64            `json:"rejected"`
	Unanswered    int64            `json:"unanswered"`
	NotSent       int64            `json:"not_sent_window_or_queue_full"`
	Unexpected    int64            `json:"unexpected"`
	RespP50us     int64            `json:"resp_p50_us"`
	RespP99us     int64            `json:"resp_p99_us"`
	RespP999us    int64            `json:"resp_p999_us"`
	RespMaxUs     int64            `json:"resp_max_us"`
	DLRs          int64            `json:"dlrs"`
	DLRsMissing   int              `json:"dlrs_missing"`
	DLRUnmatched  int64            `json:"dlrs_unmatched"`
	DLRStatus     map[string]int64 `json:"dlr_status"`
	DLRDelayP50ms int64            `json:"dlr_delay_p50_ms"`
	DLRDelayP99ms int64            `json:"dlr_delay_p99_ms"`
}

func (s *stats) summary(o options) summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	sent := s.sent.Load()
	sum := summary{
		Options: o, Elapsed: time.Since(s.start).Round(time.Millisecond).String(),
		Sent: sent, AchievedTPS: float64(sent) / o.Duration.Seconds(),
		OK: s.ok.Load(), Throttled: s.throttled.Load(), Rejected: s.rejected.Load(),
		NotSent:   s.windowFull.Load() + s.queueFull.Load(),
		RespP50us: s.resp.ValueAtQuantile(50), RespP99us: s.resp.ValueAtQuantile(99),
		RespP999us: s.resp.ValueAtQuantile(99.9), RespMaxUs: s.resp.Max(),
		DLRs: s.dlrs.Load(), DLRUnmatched: s.dlrUnmatched.Load(), DLRStatus: s.dlrStat,
		DLRDelayP50ms: s.dlrDelay.ValueAtQuantile(50), DLRDelayP99ms: s.dlrDelay.ValueAtQuantile(99),
	}
	sum.Unanswered = sent - sum.OK - sum.Throttled - sum.Rejected
	if o.DLR {
		sum.DLRsMissing = len(s.awaiting)
	}
	sum.Unexpected = s.unexpected.Load() + sum.Unanswered + int64(sum.DLRsMissing)
	return sum
}

func (sum summary) print() {
	fmt.Println()
	fmt.Println("=== psg-loadgen summary ===")
	fmt.Printf("offered        %d TPS for %v over %d binds (window %d)\n", sum.Options.TPS, sum.Options.Duration, sum.Options.Binds, sum.Options.Window)
	fmt.Printf("sent           %d (%.0f TPS achieved), not sent %d\n", sum.Sent, sum.AchievedTPS, sum.NotSent)
	fmt.Printf("responses      ok %d, throttled %d, rejected %d, unanswered %d\n", sum.OK, sum.Throttled, sum.Rejected, sum.Unanswered)
	fmt.Printf("resp latency   p50 %s  p99 %s  p99.9 %s  max %s\n", us(sum.RespP50us), us(sum.RespP99us), us(sum.RespP999us), us(sum.RespMaxUs))
	if sum.Options.DLR {
		keys := make([]string, 0, len(sum.DLRStatus))
		for k := range sum.DLRStatus {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("DLRs           %d received, %d missing, %d unmatched\n", sum.DLRs, sum.DLRsMissing, sum.DLRUnmatched)
		for _, k := range keys {
			fmt.Printf("  %-12s %d\n", k, sum.DLRStatus[k])
		}
		fmt.Printf("DLR delay      p50 %dms  p99 %dms (from submit_sm_resp)\n", sum.DLRDelayP50ms, sum.DLRDelayP99ms)
	}
	fmt.Printf("unexpected     %d\n", sum.Unexpected)
}
