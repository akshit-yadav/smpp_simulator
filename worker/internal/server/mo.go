package server

import (
	"context"
	"errors"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/mo"
)

const (
	moStopped int32 = iota
	moScheduled
	moRunning
	moFinished
)

var moStateNames = map[int32]string{moStopped: "stopped", moScheduled: "scheduled", moRunning: "running", moFinished: "finished"}

const (
	moTick       = 10 * time.Millisecond
	moTextEvents = 64 // runes of the text kept in mo_events
)

func moProgressKey(gatewayID int64) string { return "psg:moprog:" + strconv.FormatInt(gatewayID, 10) }

// moRun is one run of a gateway's MO generator (one Start in the panel).
type moRun struct {
	id       int64
	cfg      *gateway.MO // the config the builder was made from
	builder  *mo.Builder
	started  time.Time // zero until the start time is reached
	sent     int64     // parts dispatched
	seq      int64     // messages built ({seq})
	finished bool

	// Rate pacing: sent-baseSent parts are due (now-baseAt)*rate after baseAt.
	// Rebased on a rate change or a resume, so a pause is not caught up.
	baseAt   time.Time
	baseSent int64
	rate     float64
}

// moLoop runs the gateway's MO generator until the gateway stops. It sleeps
// (waking each second, and on config changes) unless a run is active, then
// ticks every 10 ms.
func (gw *Gateway) moLoop() {
	defer close(gw.moDone)
	idle := time.NewTicker(time.Second)
	defer idle.Stop()
	var tick *time.Ticker
	var tickC <-chan time.Time
	var run *moRun
	for {
		select {
		case <-gw.done:
			gw.saveRun(run)
			if tick != nil {
				tick.Stop()
			}
			return
		case <-idle.C:
			gw.saveRun(run)
		case <-gw.moWake:
		case <-tickC:
		}
		run = gw.moStep(run, time.Now())
		switch active := gw.stats.MOState.Load() == moRunning; {
		case active && tick == nil:
			tick = time.NewTicker(moTick)
			tickC = tick.C
		case !active && tick != nil:
			tick.Stop()
			tick, tickC = nil, nil
		}
	}
}

func (gw *Gateway) moStep(run *moRun, now time.Time) *moRun {
	cfg := gw.config().MO
	if cfg == nil || !cfg.Enabled {
		if run != nil {
			gw.saveRun(run)
		}
		gw.stats.MOState.Store(moStopped)
		return nil
	}
	if run == nil || run.id != cfg.RunID {
		gw.saveRun(run)
		run = gw.loadRun(cfg.RunID)
		gw.stats.MORunID.Store(run.id)
	}
	if run.cfg != cfg { // a new run, or settings edited while running
		b, err := mo.NewBuilder(builderConfig(cfg))
		if err != nil {
			gw.m.log.Error("mo generator config rejected", "gateway_id", gw.id, "err", err)
			gw.stats.MOState.Store(moStopped)
			return nil
		}
		run.cfg, run.builder = cfg, b
	}
	gw.stats.MORunSent.Store(run.sent)

	switch {
	case run.finished:
		gw.stats.MOState.Store(moFinished)
		return run
	case cfg.StartAt != nil && now.Before(*cfg.StartAt):
		gw.stats.MOState.Store(moScheduled)
		return run
	}
	if run.started.IsZero() {
		run.started = now
	}
	if run.baseAt.IsZero() || run.rate != cfg.Rate {
		run.baseAt, run.baseSent, run.rate = now, run.sent, cfg.Rate
	}
	if cfg.StopAfterS > 0 && now.Sub(run.started) >= time.Duration(cfg.StopAfterS)*time.Second ||
		cfg.StopAfterCount > 0 && run.sent >= cfg.StopAfterCount {
		gw.finishRun(run)
		return run
	}
	gw.stats.MOState.Store(moRunning)

	var due int64
	if cfg.Mode == gateway.MOBurst {
		due = cfg.Burst - run.sent
		if due <= 0 {
			gw.finishRun(run)
			return run
		}
		due = min(due, 1000) // per tick, so the loop stays responsive
	} else {
		due = int64(now.Sub(run.baseAt).Seconds()*cfg.Rate) - (run.sent - run.baseSent)
	}
	if cfg.StopAfterCount > 0 {
		due = min(due, cfg.StopAfterCount-run.sent)
	}
	for due > 0 {
		run.seq++
		m := run.builder.Next(run.seq, now)
		gw.m.sendMO(gw, m, false)
		run.sent += int64(len(m.Parts))
		due -= int64(len(m.Parts))
	}
	gw.stats.MORunSent.Store(run.sent)
	return run
}

func (gw *Gateway) finishRun(run *moRun) {
	run.finished = true
	gw.stats.MOState.Store(moFinished)
	gw.stats.MORunSent.Store(run.sent)
	gw.saveRun(run)
	gw.m.log.Info("mo run finished", "gateway_id", gw.id, "run", run.id, "parts", run.sent)
}

// loadRun resumes a run's progress from Redis (after a worker restart), or
// starts it fresh.
func (gw *Gateway) loadRun(id int64) *moRun {
	run := &moRun{id: id}
	rdb := gw.m.opt.Redis
	if rdb == nil {
		return run
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := rdb.HGetAll(ctx, moProgressKey(gw.id)).Result()
	if err != nil || h["run"] != strconv.FormatInt(id, 10) {
		return run
	}
	run.sent, _ = strconv.ParseInt(h["sent"], 10, 64)
	run.seq, _ = strconv.ParseInt(h["seq"], 10, 64)
	if ms, _ := strconv.ParseInt(h["started"], 10, 64); ms > 0 {
		run.started = time.UnixMilli(ms)
	}
	run.finished = h["finished"] == "1"
	gw.m.log.Info("mo run resumed", "gateway_id", gw.id, "run", id, "parts", run.sent, "finished", run.finished)
	return run
}

func (gw *Gateway) saveRun(run *moRun) {
	rdb := gw.m.opt.Redis
	if run == nil || rdb == nil {
		return
	}
	var started int64
	if !run.started.IsZero() {
		started = run.started.UnixMilli()
	}
	finished := "0"
	if run.finished {
		finished = "1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	key := moProgressKey(gw.id)
	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, key, "run", run.id, "sent", run.sent, "seq", run.seq, "started", started, "finished", finished)
	pipe.Expire(ctx, key, 7*24*time.Hour)
	_, _ = pipe.Exec(ctx) // best effort: losing it only means a restart re-sends part of a run
}

func builderConfig(c *gateway.MO) mo.Config {
	tpls := make([]mo.Template, len(c.Templates))
	for i, t := range c.Templates {
		tpls[i] = mo.Template{Text: t.Text, Weight: t.Weight}
	}
	return mo.Config{
		Sources: mo.Sources{
			Kind: c.Sources.Kind, List: c.Sources.List, From: c.Sources.From, To: c.Sources.To,
			Prefix: c.Sources.Prefix, Digits: c.Sources.Digits,
		},
		Destinations: c.Destinations,
		RandomDest:   c.DestOrder == "random",
		Templates:    tpls,
		Keywords:     c.Keywords,
		UCS2:         c.Encoding == "ucs2",
		LongPct:      c.LongPct,
	}
}

// sendMO dispatches each part of m as its own deliver_sm (each part counts as
// one message) and returns the first part's message ID.
func (m *Manager) sendMO(gw *Gateway, msg mo.Message, test bool) uint64 {
	text := msg.Text
	if utf8.RuneCountInString(text) > moTextEvents {
		text = string([]rune(text)[:moTextEvents])
	}
	var first uint64
	now := time.Now()
	for i, p := range msg.Parts {
		id := m.ids.Next()
		if i == 0 {
			first = id
		}
		m.dispatch(&dlrJob{
			GatewayID: gw.id, MsgID: id, Src: msg.Src, Dst: msg.Dst, SubmitAt: now, Text: text,
			Kind: jobMO, Payload: p, Coding: msg.Coding, ESM: msg.ESMClass,
			Part: uint8(i + 1), Parts: uint8(len(msg.Parts)), Test: test,
		})
	}
	return first
}

var (
	ErrGatewayNotHere = errors.New("the gateway is not running on this worker")
	ErrNoReceiver     = errors.New("no RX or TRX session is bound to this gateway")
)

// SendTestMO sends one MO now, split into parts if long. A test MO is never
// queued: it fails if no receiver is bound. Returns the first part's ID.
func (m *Manager) SendTestMO(gatewayID int64, src, dst, text string, ucs2 bool) (uint64, error) {
	gw := m.gateway(gatewayID)
	if gw == nil {
		return 0, ErrGatewayNotHere
	}
	if !gw.hasReceivers() {
		return 0, ErrNoReceiver
	}
	return m.sendMO(gw, mo.NewEncoder(ucs2).Build(src, dst, text), true), nil
}
