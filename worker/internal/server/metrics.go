package server

import (
	"strconv"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/events"
)

// Stats are one gateway's counters. The Manager keeps them by gateway ID, so
// they survive listener restarts. They feed both Prometheus and the live
// counters published to the control plane; the hot path only does atomic adds.
type Stats struct {
	Accepted, Rejected, Throttled, Invalid            atomic.Uint64
	DLRSent, DLRAck, DLRRetried, DLRFailed, DLRQueued atomic.Uint64
	DLRExpired, BindOK, BindFail                      atomic.Uint64
	SessionsTX, SessionsRX, SessionsTRX               atomic.Int64

	MOSent, MOAck, MORetried, MOFailed, MOQueued, MOExpired, MODropped atomic.Uint64
	MOState                                                            atomic.Int32 // moStopped...moFinished
	MORunID, MORunSent                                                 atomic.Int64 // current run and its parts sent
}

// countDelivery bumps the DLR or MO counter for a delivery result.
func (s *Stats) countDelivery(j *dlrJob, r events.DLRResult) {
	mo := j.isMO()
	pick := func(m, d *atomic.Uint64) *atomic.Uint64 {
		if mo {
			return m
		}
		return d
	}
	switch r {
	case events.DLRAcknowledged:
		pick(&s.MOAck, &s.DLRAck).Add(1)
	case events.DLRRetried:
		pick(&s.MORetried, &s.DLRRetried).Add(1)
	case events.DLRFailed:
		pick(&s.MOFailed, &s.DLRFailed).Add(1)
	case events.DLRQueued:
		pick(&s.MOQueued, &s.DLRQueued).Add(1)
	case events.DLRExpired:
		pick(&s.MOExpired, &s.DLRExpired).Add(1)
	case events.MODropped:
		s.MODropped.Add(1)
	}
}

func (s *Stats) countSent(j *dlrJob) {
	if j.isMO() {
		s.MOSent.Add(1)
	} else {
		s.DLRSent.Add(1)
	}
}

// Sessions returns the bound-session gauge for a bind type.
func (s *Stats) Sessions(bindType string) *atomic.Int64 {
	switch bindType {
	case "tx":
		return &s.SessionsTX
	case "rx":
		return &s.SessionsRX
	}
	return &s.SessionsTRX
}

// StatsSnapshot is a point-in-time copy of Stats (cumulative counters).
type StatsSnapshot struct {
	Accepted   uint64           `json:"accepted"`
	Rejected   uint64           `json:"rejected"`
	Throttled  uint64           `json:"throttled"`
	Invalid    uint64           `json:"invalid"`
	DLRSent    uint64           `json:"dlr_sent"`
	DLRAck     uint64           `json:"dlr_acknowledged"`
	DLRRetried uint64           `json:"dlr_retried"`
	DLRFailed  uint64           `json:"dlr_failed"`
	DLRQueued  uint64           `json:"dlr_queued"`
	DLRExpired uint64           `json:"dlr_expired"`
	BindOK     uint64           `json:"bind_ok"`
	BindFail   uint64           `json:"bind_failed"`
	Sessions   map[string]int64 `json:"sessions"`
	MO         MOSnapshot       `json:"mo"`
}

// MOSnapshot is the MO generator's state and cumulative MO counters.
type MOSnapshot struct {
	State   string `json:"state"` // stopped | scheduled | running | finished
	RunID   int64  `json:"run_id"`
	RunSent int64  `json:"run_sent"` // parts sent in the current run
	Sent    uint64 `json:"sent"`
	Ack     uint64 `json:"acknowledged"`
	Retried uint64 `json:"retried"`
	Failed  uint64 `json:"failed"`
	Queued  uint64 `json:"queued"`
	Expired uint64 `json:"expired"`
	Dropped uint64 `json:"dropped"`
}

func (s *Stats) Snapshot() StatsSnapshot {
	return StatsSnapshot{
		Accepted: s.Accepted.Load(), Rejected: s.Rejected.Load(), Throttled: s.Throttled.Load(),
		Invalid: s.Invalid.Load(), DLRSent: s.DLRSent.Load(), DLRAck: s.DLRAck.Load(),
		DLRRetried: s.DLRRetried.Load(), DLRFailed: s.DLRFailed.Load(), DLRQueued: s.DLRQueued.Load(),
		DLRExpired: s.DLRExpired.Load(), BindOK: s.BindOK.Load(), BindFail: s.BindFail.Load(),
		Sessions: map[string]int64{
			"tx": s.SessionsTX.Load(), "rx": s.SessionsRX.Load(), "trx": s.SessionsTRX.Load(),
		},
		MO: MOSnapshot{
			State: moStateNames[s.MOState.Load()], RunID: s.MORunID.Load(), RunSent: s.MORunSent.Load(),
			Sent: s.MOSent.Load(), Ack: s.MOAck.Load(), Retried: s.MORetried.Load(), Failed: s.MOFailed.Load(),
			Queued: s.MOQueued.Load(), Expired: s.MOExpired.Load(), Dropped: s.MODropped.Load(),
		},
	}
}

// collector exposes every gateway's Stats to Prometheus at scrape time.
type collector struct {
	m                              *Manager
	submits, dlrs, mos, binds, ses *prometheus.Desc
}

func newCollector(m *Manager) *collector {
	labels := []string{"gateway", "result"}
	return &collector{
		m:       m,
		submits: prometheus.NewDesc("psg_submit_total", "submit_sm received, by result.", labels, nil),
		dlrs:    prometheus.NewDesc("psg_dlr_total", "DLR events, by result.", labels, nil),
		mos:     prometheus.NewDesc("psg_mo_total", "MO deliver_sm events, by result.", labels, nil),
		binds:   prometheus.NewDesc("psg_bind_total", "Bind attempts, by result.", labels, nil),
		ses:     prometheus.NewDesc("psg_sessions", "Bound SMPP sessions.", []string{"gateway", "bind_type"}, nil),
	}
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.submits
	ch <- c.dlrs
	ch <- c.mos
	ch <- c.binds
	ch <- c.ses
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	for id, s := range c.m.StatsSnapshot() {
		g := strconv.FormatInt(id, 10)
		count := func(d *prometheus.Desc, v uint64, result string) {
			ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, float64(v), g, result)
		}
		count(c.submits, s.Accepted, "accepted")
		count(c.submits, s.Rejected, "rejected")
		count(c.submits, s.Throttled, "throttled")
		count(c.submits, s.Invalid, "invalid")
		count(c.dlrs, s.DLRSent, "sent")
		count(c.dlrs, s.DLRAck, "acknowledged")
		count(c.dlrs, s.DLRRetried, "retried")
		count(c.dlrs, s.DLRFailed, "failed")
		count(c.dlrs, s.DLRQueued, "queued")
		count(c.dlrs, s.DLRExpired, "expired")
		count(c.mos, s.MO.Sent, "sent")
		count(c.mos, s.MO.Ack, "acknowledged")
		count(c.mos, s.MO.Retried, "retried")
		count(c.mos, s.MO.Failed, "failed")
		count(c.mos, s.MO.Queued, "queued")
		count(c.mos, s.MO.Expired, "expired")
		count(c.mos, s.MO.Dropped, "dropped")
		count(c.binds, s.BindOK, "ok")
		count(c.binds, s.BindFail, "failed")
		for bt, n := range s.Sessions {
			ch <- prometheus.MustNewConstMetric(c.ses, prometheus.GaugeValue, float64(n), g, bt)
		}
	}
}

func registerMetrics(reg prometheus.Registerer, m *Manager) {
	if reg == nil {
		return
	}
	reg.MustRegister(
		newCollector(m),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "psg_dlr_pending", Help: "DLRs waiting in the timing wheel.",
		}, func() float64 { return float64(m.wheel.Len()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "psg_dlr_queued", Help: "DLRs queued because no receiver is bound.",
		}, func() float64 { return float64(m.queue.TotalLen()) }),
	)
}
