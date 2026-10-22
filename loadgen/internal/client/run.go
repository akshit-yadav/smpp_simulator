package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"

	"github.com/akshit-yadav/smpp_simulator/worker/pkg/smpp/pdu"
)

// run is the state of one Run.
type run struct {
	spec    Spec
	hooks   Hooks
	start   time.Time
	targets []targetStats

	sent, ok, throttled, rejected, timeouts      atomic.Int64
	windowFull, notSent, unexpected, disconnects atomic.Int64
	dlrs, intermediate, mo, errsTotal            atomic.Int64
	msgSeq                                       atomic.Int64

	mu       sync.Mutex
	resp     *hdrhistogram.Histogram // µs, whole run
	respIv   *hdrhistogram.Histogram // µs, current second
	dlrDelay *hdrhistogram.Histogram // ms
	statuses map[string]int64
	dlrStat  map[string]int64
	awaiting map[string]time.Time // message_id -> submit_sm_resp time
	early    map[string]earlyDLR  // DLRs that overtook their submit_sm_resp (another session)
	errLog   []ErrorRecord
	errTaken int

	traceMu   sync.Mutex
	traceW    *bufio.Writer
	traceF    *os.File
	traceSize int64
	traceFull bool
}

type targetStats struct {
	sent, ok, throttled, rejected, timeouts, dlrs atomic.Int64
	bindFailed                                    int
	sessions                                      int
	mu                                            sync.Mutex
	resp                                          *hdrhistogram.Histogram
}

func newHist(max int64) *hdrhistogram.Histogram { return hdrhistogram.New(1, max, 3) }

// Run binds every session, sends for spec.Duration, drains, unbinds and
// returns the result. ctx ends the run early (Stopped = "stopped").
func Run(ctx context.Context, spec Spec, hooks Hooks) (*Result, error) {
	spec.defaults()
	if len(spec.Targets) == 0 || spec.TPS <= 0 || spec.Duration <= 0 {
		return nil, errors.New("a run needs targets, a TPS and a duration")
	}
	r := &run{
		spec: spec, hooks: hooks, start: time.Now(),
		targets:  make([]targetStats, len(spec.Targets)),
		resp:     newHist(60_000_000),
		respIv:   newHist(60_000_000),
		dlrDelay: newHist(3_600_000),
		statuses: map[string]int64{},
		dlrStat:  map[string]int64{},
		awaiting: map[string]time.Time{},
		early:    map[string]earlyDLR{},
	}
	for i := range r.targets {
		r.targets[i].resp = newHist(60_000_000)
	}
	if spec.TraceDir != "" {
		if err := r.openTrace(); err != nil {
			return nil, err
		}
		defer r.removeTrace()
	}

	r.progress("binding", Second{})
	sessions := r.bindAll()
	var senders []*session
	for _, s := range sessions {
		if s.sends() {
			senders = append(senders, s)
		}
	}
	if len(senders) == 0 {
		for _, s := range sessions { // e.g. only receivers bound
			s.unbind()
		}
		res := r.result(sessions, "no session could bind")
		r.flushErrors() // the bind failures
		return res, nil
	}
	var wg sync.WaitGroup
	for _, s := range senders {
		wg.Add(1)
		go func() { defer wg.Done(); s.sendLoop() }()
	}

	stopped := ""
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timeline []Second
	tickDone := make(chan struct{})
	phase := atomic.Value{}
	phase.Store("sending")
	go func() {
		defer close(tickDone)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		var prev [6]int64
		for {
			select {
			case <-runCtx.Done():
				return
			case now := <-t.C:
				sec := r.second(now, &prev)
				timeline = append(timeline, sec)
				r.progress(phase.Load().(string), sec)
				r.flushErrors()
				for _, s := range sessions {
					s.sweep(now)
				}
				if hooks.LowMemory != nil && hooks.LowMemory() {
					stopped = "host memory low"
					cancel()
					return
				}
			}
		}
	}()

	r.pace(runCtx, senders)
	for _, s := range senders {
		close(s.queue)
	}
	wg.Wait()

	phase.Store("draining")
	deadline := time.Now().Add(spec.Drain + 2*time.Duration(spec.NetDelay.MaxMS)*time.Millisecond)
	for runCtx.Err() == nil && time.Now().Before(deadline) && !r.drained(sessions) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-tickDone // stopped is only read after the ticker has ended
	if stopped == "" && ctx.Err() != nil {
		stopped = "stopped"
	}
	for _, s := range sessions {
		s.unbind()
	}
	res := r.result(sessions, stopped)
	res.Timeline = timeline
	r.flushErrors()
	return res, nil
}

func (r *run) progress(phase string, last Second) {
	if r.hooks.Progress == nil {
		return
	}
	r.hooks.Progress(Progress{
		Phase: phase, Elapsed: time.Since(r.start).Milliseconds(), Last: last,
		Sent: r.sent.Load(), OK: r.ok.Load(), Errors: r.errsTotal.Load(), DLRs: r.dlrs.Load(),
	})
}

// bindAll opens every session, all targets in parallel.
func (r *run) bindAll() []*session {
	type job struct {
		target, idx int
		bt          string
	}
	var jobs []job
	for ti, t := range r.spec.Targets {
		for range t.Sessions {
			if t.BindType == "tx_rx" {
				jobs = append(jobs, job{ti, len(jobs), "tx"}, job{ti, len(jobs) + 1, "rx"})
			} else {
				jobs = append(jobs, job{ti, len(jobs), "trx"})
			}
		}
	}
	out := make([]*session, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s, err := dial(r, j.idx, j.target, j.bt)
			if err != nil {
				r.mu.Lock()
				r.targets[j.target].bindFailed++
				r.mu.Unlock()
				r.logError(&session{idx: j.idx, target: j.target}, "bind_failed", "bind_"+j.bt, 0, "", err.Error())
				return
			}
			out[j.idx] = s
		}()
	}
	wg.Wait()
	return slices.DeleteFunc(out, func(s *session) bool { return s == nil })
}

// pace releases submit_sm slots at the target rate (linear ramp-up first),
// 1 ms at a time, round-robin over the sending sessions. Open loop: it never
// waits for responses.
func (r *run) pace(ctx context.Context, senders []*session) {
	start := time.Now()
	ramp := r.spec.RampUp.Seconds()
	due := func(t float64) int64 {
		if ramp > 0 && t < ramp {
			return int64(r.spec.TPS * t * t / (2 * ramp))
		}
		return int64(r.spec.TPS * (t - ramp/2))
	}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	end := start.Add(r.spec.Duration)
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
			for n := due(now.Sub(start).Seconds()) - sent; n > 0; n-- {
				select {
				case senders[next].queue <- now:
				default:
					r.notSent.Add(1) // that session's sender is behind
				}
				sent++
				next = (next + 1) % len(senders)
			}
		}
	}
}

// second closes one second of the timeline.
func (r *run) second(now time.Time, prev *[6]int64) Second {
	cur := [6]int64{r.sent.Load(), r.ok.Load(), r.throttled.Load(), r.rejected.Load(), r.errsTotal.Load(), r.dlrs.Load()}
	r.mu.Lock()
	p50, p99 := r.respIv.ValueAtQuantile(50), r.respIv.ValueAtQuantile(99)
	r.respIv.Reset()
	r.mu.Unlock()
	sec := Second{
		T: now.UnixMilli(), Sent: cur[0] - prev[0], OK: cur[1] - prev[1], Throttled: cur[2] - prev[2],
		Rejected: cur[3] - prev[3], Errors: cur[4] - prev[4], DLRs: cur[5] - prev[5], P50us: p50, P99us: p99,
	}
	*prev = cur
	return sec
}

func (r *run) recordResp(t *targetStats, d time.Duration) {
	us := max(d.Microseconds(), 1)
	r.mu.Lock()
	_ = r.resp.RecordValue(us)
	_ = r.respIv.RecordValue(us)
	r.mu.Unlock()
	t.mu.Lock()
	_ = t.resp.RecordValue(us)
	t.mu.Unlock()
}

func (r *run) status(st pdu.Status) {
	key := fmt.Sprintf("0x%02X %s", uint32(st), st)
	r.mu.Lock()
	r.statuses[key]++
	r.mu.Unlock()
}

type earlyDLR struct {
	at      time.Time
	session *session
	stat    string
}

func (r *run) trackDLR(id string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.early[id]; ok { // its DLR came first, on another session
		delete(r.early, id)
		_ = r.dlrDelay.RecordValue(max(e.at.Sub(at).Milliseconds(), 1))
		return
	}
	r.awaiting[id] = at
}

// recordDLR matches a DLR with its message. Intermediate ones (ENROUTE,
// ACCEPTD) are counted apart and keep the message waiting for its final DLR.
func (r *run) recordDLR(s *session, id, stat string, at time.Time) {
	if stat == "ENROUTE" || stat == "ACCEPTD" {
		r.intermediate.Add(1)
		r.mu.Lock()
		r.dlrStat[stat]++
		r.mu.Unlock()
		return
	}
	r.dlrs.Add(1)
	r.targets[s.target].dlrs.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dlrStat[stat]++
	if sentAt, ok := r.awaiting[id]; ok {
		delete(r.awaiting, id)
		_ = r.dlrDelay.RecordValue(max(at.Sub(sentAt).Milliseconds(), 1))
	} else if r.spec.wantsDLR() {
		// Its submit_sm_resp may still be on its way on another session;
		// still unmatched at the end of the run, it counts as unmatched.
		r.early[id] = earlyDLR{at: at, session: s, stat: stat}
	}
}

// drained reports whether every submit_sm is answered and, with DLRs
// requested, every accepted message has its final DLR.
func (r *run) drained(sessions []*session) bool {
	for _, s := range sessions {
		if s.unanswered() > 0 {
			return false
		}
	}
	if !r.spec.wantsDLR() {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.awaiting) == 0
}

func (r *run) logError(s *session, kind, cmd string, seq uint32, status, detail string) {
	r.errsTotal.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.errLog) >= maxErrors {
		return
	}
	r.errLog = append(r.errLog, ErrorRecord{
		TS: time.Now().UTC(), Gateway: r.spec.Targets[s.target].Name, Session: s.idx,
		Kind: kind, Command: cmd, Seq: seq, Status: status, Detail: detail,
	})
}

// flushErrors hands the error log entries added since the last call to the hook.
func (r *run) flushErrors() {
	if r.hooks.Errors == nil {
		return
	}
	r.mu.Lock()
	batch := slices.Clone(r.errLog[r.errTaken:])
	r.errTaken = len(r.errLog)
	r.mu.Unlock()
	if len(batch) > 0 {
		r.hooks.Errors(batch)
	}
}

func latency(h *hdrhistogram.Histogram) Latency {
	return Latency{
		P50: h.ValueAtQuantile(50), P90: h.ValueAtQuantile(90), P99: h.ValueAtQuantile(99),
		P999: h.ValueAtQuantile(99.9), Max: h.Max(),
	}
}

func (r *run) result(sessions []*session, stopped string) *Result {
	finished := time.Now()
	res := &Result{
		Started: r.start.UTC(), Finished: finished.UTC(), Stopped: stopped,
		OfferedTPS: r.spec.TPS, Sessions: len(sessions),
		Sent: r.sent.Load(), OK: r.ok.Load(), Throttled: r.throttled.Load(), Rejected: r.rejected.Load(),
		Timeouts: r.timeouts.Load(), NotSent: r.windowFull.Load() + r.notSent.Load(),
		Disconnects: r.disconnects.Load(), DLRs: r.dlrs.Load(),
		Intermediate: r.intermediate.Load(), MO: r.mo.Load(),
	}
	sendSecs := min(r.spec.Duration, finished.Sub(r.start)).Seconds()
	res.AchievedTPS = float64(res.Sent) / max(sendSecs, 0.001)
	var rtts []time.Duration
	for _, s := range sessions {
		res.Unanswered += s.unanswered()
		rtts = append(rtts, s.rtt)
	}
	res.Unanswered += res.Timeouts
	if len(rtts) > 0 {
		slices.Sort(rtts)
		res.BindRTTms = float64(rtts[len(rtts)/2].Microseconds()) / 1000
	}

	r.mu.Lock()
	res.Statuses, res.DLRStatus = r.statuses, r.dlrStat
	res.Latency = latency(r.resp)
	res.DLRDelayP50, res.DLRDelayP99 = r.dlrDelay.ValueAtQuantile(50), r.dlrDelay.ValueAtQuantile(99)
	if r.spec.wantsDLR() {
		res.DLRsMissing = len(r.awaiting)
		n := 0
		for id := range r.awaiting {
			if len(r.errLog) >= maxErrors || n >= 100 {
				break
			}
			r.errLog = append(r.errLog, ErrorRecord{TS: finished.UTC(), Kind: "dlr_missing", Detail: "no final DLR for message " + id})
			n++
		}
		r.errsTotal.Add(int64(len(r.awaiting)))
		for id, e := range r.early {
			res.DLRUnmatched++
			r.errsTotal.Add(1)
			if len(r.errLog) < maxErrors {
				r.errLog = append(r.errLog, ErrorRecord{
					TS: e.at.UTC(), Gateway: r.spec.Targets[e.session.target].Name, Session: e.session.idx,
					Kind: "dlr_unmatched", Command: "deliver_sm", Status: e.stat, Detail: "receipt for unknown message " + id,
				})
			}
		}
	}
	res.ErrorsLogged = len(r.errLog)
	r.mu.Unlock()
	res.Errors = r.errsTotal.Load()
	res.Unexpected = r.unexpected.Load() + res.Unanswered + int64(res.DLRsMissing)

	for i, t := range r.spec.Targets {
		ts := &r.targets[i]
		ts.mu.Lock()
		lat := latency(ts.resp)
		ts.mu.Unlock()
		n := 0
		for _, s := range sessions {
			if s.target == i {
				n++
			}
		}
		res.BindFailed += ts.bindFailed
		res.Targets = append(res.Targets, TargetResult{
			GatewayID: t.GatewayID, Name: t.Name, Addr: t.Addr, Sessions: n, BindFailed: ts.bindFailed,
			Sent: ts.sent.Load(), OK: ts.ok.Load(), Throttled: ts.throttled.Load(), Rejected: ts.rejected.Load(),
			Timeouts: ts.timeouts.Load(), DLRs: ts.dlrs.Load(), Latency: lat,
		})
	}
	if r.traceF != nil {
		res.PerSession, res.TraceFull = r.evaluateTrace(sessions)
	}
	return res
}

// ---- temporary PDU trace -------------------------------------------------------

func (r *run) openTrace() error {
	if err := os.MkdirAll(r.spec.TraceDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(r.spec.TraceDir, "trace-*.log")
	if err != nil {
		return err
	}
	r.traceF, r.traceW = f, bufio.NewWriterSize(f, 1<<20)
	return nil
}

// trace appends one line per PDU: unix µs, session, kind (S sent, R
// received, L submit_sm_resp latency), command, sequence, status, latency µs.
func (r *run) trace(s *session, kind byte, cmd pdu.CommandID, seq uint32, st pdu.Status, lat time.Duration) {
	if r.traceF == nil {
		return
	}
	line := fmt.Sprintf("%d %d %c %08x %d %d %d\n", time.Now().UnixMicro(), s.idx, kind, uint32(cmd), seq, uint32(st), lat.Microseconds())
	r.traceMu.Lock()
	defer r.traceMu.Unlock()
	if r.traceW == nil || r.traceFull {
		return
	}
	if r.traceSize+int64(len(line)) > r.spec.TraceMax {
		r.traceFull = true
		return
	}
	r.traceSize += int64(len(line))
	_, _ = r.traceW.WriteString(line)
}

// evaluateTrace reads the trace back for per-session results.
func (r *run) evaluateTrace(sessions []*session) ([]SessionResult, bool) {
	r.traceMu.Lock()
	_ = r.traceW.Flush()
	full := r.traceFull
	r.traceW = nil // no more lines
	r.traceMu.Unlock()
	f, err := os.Open(r.traceF.Name())
	if err != nil {
		return nil, full
	}
	defer f.Close()
	type acc struct {
		res  SessionResult
		hist *hdrhistogram.Histogram
	}
	per := map[int]*acc{}
	for _, s := range sessions {
		per[s.idx] = &acc{res: SessionResult{Session: s.idx, Gateway: r.spec.Targets[s.target].Name, BindType: s.bindType}, hist: newHist(60_000_000)}
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ts, lat int64
		var idx int
		var kind byte
		var cmd, seq, st uint32
		if n, _ := fmt.Sscanf(sc.Text(), "%d %d %c %x %d %d %d", &ts, &idx, &kind, &cmd, &seq, &st, &lat); n != 7 {
			continue
		}
		a := per[idx]
		if a == nil {
			continue
		}
		switch {
		case kind == 'S':
			a.res.Sent++
		case kind == 'L' && st == 0:
			a.res.OK++
			_ = a.hist.RecordValue(max(lat, 1))
		case kind == 'L':
			a.res.NotOK++
			_ = a.hist.RecordValue(max(lat, 1))
		case kind == 'R' && pdu.CommandID(cmd) == pdu.DeliverSM:
			a.res.Deliver++
		}
	}
	out := make([]SessionResult, 0, len(per))
	for _, a := range per {
		a.res.P50us, a.res.P99us = a.hist.ValueAtQuantile(50), a.hist.ValueAtQuantile(99)
		out = append(out, a.res)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Session < out[j].Session })
	return out, full
}

// removeTrace deletes the trace: per-PDU data never outlives the run.
func (r *run) removeTrace() {
	r.traceMu.Lock()
	r.traceW = nil
	r.traceMu.Unlock()
	name := r.traceF.Name()
	r.traceF.Close()
	_ = os.Remove(name)
	_ = os.Remove(filepath.Dir(name)) // only if empty
}
