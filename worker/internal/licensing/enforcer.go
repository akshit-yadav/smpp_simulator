// Package licensing enforces the licence on a worker, as a backstop to the
// control plane: it re-verifies the licence file it receives with the
// gateway config, keeps a global TPS token bucket at the share of the
// licence's TPS cap the control plane assigns it, and caps the number of
// active gateways. Without a valid licence for this machine, the free tier
// applies.
package licensing

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/ratelimit"
	"github.com/akshit-yadav/smpp_simulator/worker/pkg/licence"
)

type Options struct {
	PublicKey   ed25519.PublicKey
	Fingerprint licence.Fingerprint
	ClockPath   string // high-water mark of the clock, survives restarts; "" = memory only
	RequireBoth bool   // refuse a partial machine match
	Log         *slog.Logger
	Now         func() time.Time
}

// Status is what the worker enforces, for the panel and logs.
type Status struct {
	Licensed    bool    `json:"licensed"`
	LicenceID   string  `json:"licence_id,omitempty"`
	Reason      string  `json:"reason,omitempty"` // why the free tier applies
	MaxGateways int     `json:"max_gateways"`
	MaxTPS      int     `json:"max_tps"`
	ShareTPS    float64 `json:"share_tps"` // this worker's bucket rate
}

type Enforcer struct {
	opt    Options
	log    *slog.Logger
	bucket *ratelimit.Bucket

	mu        sync.Mutex
	file      string
	workers   int
	share     float64 // from the control plane; 0 = none yet
	highWater time.Time
	status    Status
}

func New(opt Options) *Enforcer {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	e := &Enforcer{opt: opt, log: opt.Log, bucket: ratelimit.New(licence.FreeTPS), workers: 1}
	if b, err := os.ReadFile(opt.ClockPath); err == nil {
		if sec, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
			e.highWater = time.Unix(sec, 0)
		}
	}
	e.mu.Lock()
	e.recompute()
	e.mu.Unlock()
	return e
}

// Allow takes n tokens from the licence TPS bucket. A nil Enforcer allows all.
func (e *Enforcer) Allow(n int) bool {
	if e == nil {
		return true
	}
	return e.bucket.AllowN(n)
}

// Apply takes the licence file and the number of active workers from the
// worker config ("" = no licence).
func (e *Enforcer) Apply(file string, workers int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.file, e.workers = file, max(workers, 1)
	e.recompute()
}

// SetShare sets this worker's share of the TPS cap, as assigned by the
// control plane (it never exceeds the cap this worker verified itself).
func (e *Enforcer) SetShare(tps float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.share = tps
	e.recompute()
}

// MaxGateways is the most active gateways this worker will run.
func (e *Enforcer) MaxGateways() int {
	if e == nil {
		return -1 // unlimited
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status.MaxGateways
}

func (e *Enforcer) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

// Run re-checks expiry and advances the clock high-water mark every minute.
func (e *Enforcer) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.mu.Lock()
			e.recompute()
			e.mu.Unlock()
		}
	}
}

// recompute re-verifies the licence and sets the bucket rate. Caller holds mu.
func (e *Enforcer) recompute() {
	now := e.opt.Now()
	if now.After(e.highWater) {
		e.highWater = now
		if e.opt.ClockPath != "" {
			_ = os.WriteFile(e.opt.ClockPath, []byte(strconv.FormatInt(now.Unix(), 10)), 0o644)
		}
	}
	st := Status{MaxGateways: licence.FreeGateways, MaxTPS: licence.FreeTPS, Reason: "no licence"}
	if e.file != "" {
		l, err := licence.Parse([]byte(e.file), e.opt.PublicKey)
		if err == nil {
			err = l.Check(e.opt.Fingerprint, now, e.highWater, e.opt.RequireBoth)
		}
		if err == nil {
			st = Status{Licensed: true, LicenceID: l.LicenceID, MaxGateways: l.MaxGateways, MaxTPS: l.MaxTPS}
		} else {
			st.Reason = err.Error()
		}
	}
	st.ShareTPS = float64(st.MaxTPS) / float64(e.workers)
	if e.share > 0 {
		st.ShareTPS = min(e.share, float64(st.MaxTPS))
	}
	st.ShareTPS = max(st.ShareTPS, 1)
	if st.Licensed != e.status.Licensed || st.Reason != e.status.Reason || st.LicenceID != e.status.LicenceID {
		if st.Licensed {
			e.log.Info("licence applied", "licence_id", st.LicenceID, "max_gateways", st.MaxGateways, "max_tps", st.MaxTPS)
		} else {
			e.log.Warn("free tier applies", "reason", st.Reason, "max_gateways", st.MaxGateways, "max_tps", st.MaxTPS)
		}
	}
	if st.ShareTPS != e.status.ShareTPS {
		e.bucket.SetRate(st.ShareTPS)
	}
	e.status = st
}
