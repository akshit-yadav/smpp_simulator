// Package server runs the SMPP listeners of every gateway assigned to this
// worker, from one process.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/msgid"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/outcome"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/ratelimit"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/timewheel"
)

type Options struct {
	WorkerID    uint8
	ListenHost  string // "" = all interfaces
	PortAllowed func(port int) error
	Redis       *redis.Client // nil: DLR queue is memory-only
	Registerer  prometheus.Registerer
	Log         *slog.Logger

	BindTimeout time.Duration // close if not bound in time
	IdleTimeout time.Duration // close a bound session with no traffic
	DLRTimeout  time.Duration // deliver_sm_resp timeout before retry
	DLRRetries  int
	QueueMaxAge time.Duration // drop queued DLRs older than this
}

func (o *Options) defaults() {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.PortAllowed == nil {
		o.PortAllowed = func(int) error { return nil }
	}
	if o.BindTimeout == 0 {
		o.BindTimeout = 30 * time.Second
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = 120 * time.Second
	}
	if o.DLRTimeout == 0 {
		o.DLRTimeout = 30 * time.Second
	}
	if o.DLRRetries == 0 {
		o.DLRRetries = 5
	}
	if o.QueueMaxAge == 0 {
		o.QueueMaxAge = 72 * time.Hour
	}
}

const wheelTick = 5 * time.Millisecond

type Manager struct {
	opt     Options
	log     *slog.Logger
	ids     *msgid.Generator
	wheel   *timewheel.Wheel[*dlrJob]
	queue   *dlrQueue
	metrics *Metrics

	mu       sync.Mutex // serialises Apply and shutdown
	gateways map[int64]*Gateway
	byID     atomic.Pointer[map[int64]*Gateway] // lock-free snapshot for the hot path
}

func NewManager(opt Options) *Manager {
	opt.defaults()
	m := &Manager{
		opt:      opt,
		log:      opt.Log,
		ids:      msgid.New(opt.WorkerID),
		wheel:    timewheel.New[*dlrJob](wheelTick, time.Now()),
		gateways: map[int64]*Gateway{},
	}
	m.byID.Store(&map[int64]*Gateway{})
	m.queue = newDLRQueue(opt.Redis, opt.Log)
	m.metrics = newMetrics(opt.Registerer, m)
	return m
}

// ApplyResult reports what happened to one gateway in Apply.
type ApplyResult struct {
	ID     int64
	Action string // started, updated, restarted, stopped, removed, unchanged
	Err    error
}

// Apply makes the running gateways match cfgs: starts new ones, updates
// changed ones in place (restarting the listener only if the port changed),
// and stops the ones that are gone or no longer active.
func (m *Manager) Apply(cfgs []gateway.Config) []ApplyResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.publish()

	var results []ApplyResult
	want := map[int64]bool{}
	for _, c := range cfgs {
		want[c.ID] = true
		results = append(results, m.applyOne(c))
	}
	for id, gw := range m.gateways {
		if !want[id] {
			gw.stop("removed")
			delete(m.gateways, id)
			m.metrics.dropGateway(id)
			results = append(results, ApplyResult{ID: id, Action: "removed"})
		}
	}
	for _, r := range results {
		if r.Err != nil {
			m.log.Error("gateway apply failed", "gateway_id", r.ID, "action", r.Action, "err", r.Err)
		} else if r.Action != "unchanged" {
			m.log.Info("gateway applied", "gateway_id", r.ID, "action", r.Action)
		}
	}
	return results
}

func (m *Manager) applyOne(c gateway.Config) ApplyResult {
	gw := m.gateways[c.ID]
	active := c.Status == gateway.StatusActive

	if gw != nil && reflect.DeepEqual(gw.config().Config, c) {
		return ApplyResult{ID: c.ID, Action: "unchanged"}
	}
	rc, err := m.compile(c, gw)
	if err != nil {
		return ApplyResult{ID: c.ID, Action: "invalid", Err: err}
	}

	switch {
	case gw == nil && !active:
		return ApplyResult{ID: c.ID, Action: "unchanged"}
	case gw == nil:
		gw, err = m.startGateway(rc)
		if err != nil {
			return ApplyResult{ID: c.ID, Action: "start", Err: err}
		}
		m.gateways[c.ID] = gw
		return ApplyResult{ID: c.ID, Action: "started"}
	case !active:
		gw.stop(c.Status)
		delete(m.gateways, c.ID)
		return ApplyResult{ID: c.ID, Action: "stopped"}
	case gw.config().Port != c.Port:
		if err := m.opt.PortAllowed(c.Port); err != nil {
			return ApplyResult{ID: c.ID, Action: "restart", Err: err}
		}
		ln, err := listen(m.opt.ListenHost, c.Port)
		if err != nil {
			return ApplyResult{ID: c.ID, Action: "restart", Err: err}
		}
		gw.stop("port changed")
		ngw := m.newGateway(rc, ln)
		m.gateways[c.ID] = ngw
		return ApplyResult{ID: c.ID, Action: "restarted"}
	default:
		gw.update(rc)
		return ApplyResult{ID: c.ID, Action: "updated"}
	}
}

// compile builds the runtime form of a config, reusing the previous picker
// (keeping an exact-mode deck's position) and gateway TPS bucket when possible.
func (m *Manager) compile(c gateway.Config, prev *Gateway) (*runtimeConfig, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	rc := &runtimeConfig{Config: c}
	if prev != nil {
		old := prev.config()
		if reflect.DeepEqual(old.Outcome, c.Outcome) {
			rc.picker = old.picker
		}
		rc.gwBucket = old.gwBucket
	}
	if rc.picker == nil {
		p, err := outcome.Compile(c.Outcome)
		if err != nil {
			return nil, err
		}
		rc.picker = p
	}
	if rc.gwBucket == nil {
		rc.gwBucket = ratelimit.New(c.TPS.Limit)
	} else {
		rc.gwBucket.SetRate(c.TPS.Limit)
	}
	return rc, nil
}

func (m *Manager) startGateway(rc *runtimeConfig) (*Gateway, error) {
	if err := m.opt.PortAllowed(rc.Port); err != nil {
		return nil, err
	}
	ln, err := listen(m.opt.ListenHost, rc.Port)
	if err != nil {
		return nil, err
	}
	gw := m.newGateway(rc, ln)
	go m.queue.restore(rc.ID)
	return gw, nil
}

// publish swaps in a copy of the gateway map for lock-free lookups. Caller holds m.mu.
func (m *Manager) publish() {
	snap := make(map[int64]*Gateway, len(m.gateways))
	for id, gw := range m.gateways {
		snap[id] = gw
	}
	m.byID.Store(&snap)
}

func (m *Manager) gateway(id int64) *Gateway { return (*m.byID.Load())[id] }

// Run drives the DLR timing wheel and the DLR queue until ctx is done, then
// stops every gateway.
func (m *Manager) Run(ctx context.Context) {
	tick := time.NewTicker(wheelTick)
	defer tick.Stop()
	drain := time.NewTicker(200 * time.Millisecond)
	defer drain.Stop()
	persist := time.NewTicker(time.Second)
	defer persist.Stop()

	for {
		select {
		case <-ctx.Done():
			m.shutdown()
			return
		case now := <-tick.C:
			m.wheel.Advance(now, m.dispatch)
		case <-drain.C:
			m.drainQueues()
		case <-persist.C:
			m.queue.persist(ctx)
		}
	}
}

func (m *Manager) shutdown() {
	m.mu.Lock()
	for id, gw := range m.gateways {
		gw.stop("shutdown")
		delete(m.gateways, id)
	}
	m.publish()
	m.mu.Unlock()
	// Keep undelivered DLRs: pending ones go to the queue so they survive a restart.
	m.wheel.Drain(func(j *dlrJob) { m.queue.push(j) })
	m.queue.persist(context.Background())
}

// drainQueues hands queued DLRs to gateways that now have a receiver bound.
func (m *Manager) drainQueues() {
	now := time.Now()
	for _, id := range m.queue.gatewaysWithItems() {
		gw := m.gateway(id)
		if gw == nil || !gw.hasReceivers() {
			continue
		}
		for _, j := range m.queue.pop(id, 5000) {
			if now.Sub(j.QueuedAt) > m.opt.QueueMaxAge {
				gw.metrics.dlrExpire.Inc()
				continue
			}
			m.dispatch(j)
		}
	}
}

// PortError is returned when a gateway's port fails validation.
type PortError struct {
	Port int
	Err  error
}

func (e *PortError) Error() string {
	return fmt.Sprintf("Port %d is already in use. Please choose another port. (%v)", e.Port, e.Err)
}

func (e *PortError) Unwrap() error { return e.Err }
