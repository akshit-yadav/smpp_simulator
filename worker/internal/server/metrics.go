package server

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are registered once per Manager. Per-gateway children are resolved
// when a gateway starts, so the hot path only increments a counter.
type Metrics struct {
	submits  *prometheus.CounterVec
	dlrs     *prometheus.CounterVec
	binds    *prometheus.CounterVec
	sessions *prometheus.GaugeVec
	pending  prometheus.GaugeFunc
	queued   prometheus.GaugeFunc
}

func newMetrics(reg prometheus.Registerer, m *Manager) *Metrics {
	mt := &Metrics{
		submits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fsg_submit_total", Help: "submit_sm received, by result.",
		}, []string{"gateway", "result"}),
		dlrs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fsg_dlr_total", Help: "DLR events, by result.",
		}, []string{"gateway", "result"}),
		binds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fsg_bind_total", Help: "Bind attempts, by result.",
		}, []string{"gateway", "result"}),
		sessions: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "fsg_sessions", Help: "Bound SMPP sessions.",
		}, []string{"gateway", "bind_type"}),
		pending: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "fsg_dlr_pending", Help: "DLRs waiting in the timing wheel.",
		}, func() float64 { return float64(m.wheel.Len()) }),
		queued: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "fsg_dlr_queued", Help: "DLRs queued because no receiver is bound.",
		}, func() float64 { return float64(m.queue.TotalLen()) }),
	}
	if reg != nil {
		reg.MustRegister(mt.submits, mt.dlrs, mt.binds, mt.sessions, mt.pending, mt.queued)
	}
	return mt
}

// gwMetrics are one gateway's pre-resolved counters.
type gwMetrics struct {
	accepted, rejected, throttled, invalid                      prometheus.Counter
	dlrSent, dlrOK, dlrRetried, dlrFailed, dlrQueued, dlrExpire prometheus.Counter
	bindOK, bindFail                                            prometheus.Counter
	sessions                                                    map[string]prometheus.Gauge
}

func (mt *Metrics) forGateway(id int64) *gwMetrics {
	g := strconv.FormatInt(id, 10)
	return &gwMetrics{
		accepted:   mt.submits.WithLabelValues(g, "accepted"),
		rejected:   mt.submits.WithLabelValues(g, "rejected"),
		throttled:  mt.submits.WithLabelValues(g, "throttled"),
		invalid:    mt.submits.WithLabelValues(g, "invalid"),
		dlrSent:    mt.dlrs.WithLabelValues(g, "sent"),
		dlrOK:      mt.dlrs.WithLabelValues(g, "acknowledged"),
		dlrRetried: mt.dlrs.WithLabelValues(g, "retried"),
		dlrFailed:  mt.dlrs.WithLabelValues(g, "failed"),
		dlrQueued:  mt.dlrs.WithLabelValues(g, "queued"),
		dlrExpire:  mt.dlrs.WithLabelValues(g, "expired"),
		bindOK:     mt.binds.WithLabelValues(g, "ok"),
		bindFail:   mt.binds.WithLabelValues(g, "failed"),
		sessions: map[string]prometheus.Gauge{
			"tx":  mt.sessions.WithLabelValues(g, "tx"),
			"rx":  mt.sessions.WithLabelValues(g, "rx"),
			"trx": mt.sessions.WithLabelValues(g, "trx"),
		},
	}
}

func (mt *Metrics) dropGateway(id int64) {
	g := prometheus.Labels{"gateway": strconv.FormatInt(id, 10)}
	mt.submits.DeletePartialMatch(g)
	mt.dlrs.DeletePartialMatch(g)
	mt.binds.DeletePartialMatch(g)
	mt.sessions.DeletePartialMatch(g)
}
