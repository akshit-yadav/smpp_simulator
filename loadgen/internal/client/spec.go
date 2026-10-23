// Package client is PSG's SMPP client simulator: it binds sessions to one or
// more gateways, offers submit_sm at a fixed rate whatever the responses do
// (open loop, windowed), and measures responses, DLRs and errors. It can add
// network delay to every PDU and delay its answers to deliver_sm, to behave
// like a real platform. psg-loadgen (CLI) and psg-tester (panel-driven agent)
// are built on it.
package client

import (
	"math/rand/v2"
	"time"
)

// Target is one gateway to test.
type Target struct {
	GatewayID int64  `json:"gateway_id"`
	Name      string `json:"name"`
	Addr      string `json:"addr"` // host:port
	SystemID  string `json:"system_id"`
	Password  string `json:"-"`
	Sessions  int    `json:"sessions"`
	// BindType "trx": Sessions transceivers, each sending and receiving.
	// "tx_rx": Sessions transmitters that send, plus Sessions receivers.
	BindType string `json:"bind_type"`
}

// Delay is a fixed (Min == Max) or uniformly random delay in ms; 0/0 is off.
type Delay struct {
	MinMS int `json:"min_ms"`
	MaxMS int `json:"max_ms"`
}

func (d Delay) On() bool { return d.MaxMS > 0 }

func (d Delay) pick() time.Duration {
	ms := d.MinMS
	if d.MaxMS > d.MinMS {
		ms += rand.IntN(d.MaxMS - d.MinMS + 1)
	}
	return time.Duration(ms) * time.Millisecond
}

// Spec describes a run.
type Spec struct {
	Targets            []Target      `json:"targets"`
	TPS                float64       `json:"tps"` // total, split evenly over the sending sessions
	Duration           time.Duration `json:"duration"`
	RampUp             time.Duration `json:"ramp_up"` // linear from 0 to TPS
	Window             int           `json:"window"`  // unanswered submit_sm per session
	RegisteredDelivery byte          `json:"registered_delivery"`
	Text               string        `json:"text"` // {seq} becomes the message number
	UCS2               bool          `json:"ucs2"`
	Src                string        `json:"src"`
	DstPrefix          string        `json:"dst_prefix"` // 8 random digits are appended
	NetDelay           Delay         `json:"net_delay"`  // every PDU, each way
	RespDelay          Delay         `json:"resp_delay"` // before answering deliver_sm
	Drain              time.Duration `json:"drain"`      // after sending, wait for responses and DLRs
	Simulator          bool          `json:"simulator"`  // mark binds so PSG tags the traffic

	// TraceDir, if set, holds a temporary per-PDU trace (at most TraceMax
	// bytes), read once at the end for per-session results and then deleted.
	TraceDir string `json:"-"`
	TraceMax int64  `json:"-"`
}

func (s *Spec) defaults() {
	if s.Window <= 0 {
		s.Window = 100
	}
	if s.Text == "" {
		s.Text = "PSG test {seq}"
	}
	if s.Src == "" {
		s.Src = "PSGTEST"
	}
	if s.DstPrefix == "" {
		s.DstPrefix = "9198"
	}
	if s.Drain <= 0 {
		s.Drain = 15 * time.Second
	}
	if s.TraceMax <= 0 {
		s.TraceMax = 500 << 20
	}
	for i := range s.Targets {
		if s.Targets[i].BindType == "" {
			s.Targets[i].BindType = "trx"
		}
		s.Targets[i].Sessions = max(s.Targets[i].Sessions, 1)
	}
}

// wantsDLR reports whether every accepted message should get a final DLR.
func (s *Spec) wantsDLR() bool { return s.RegisteredDelivery&0x03 == 1 }

// Latency percentiles of submit_sm_resp, in µs.
type Latency struct {
	P50  int64 `json:"p50_us"`
	P90  int64 `json:"p90_us"`
	P99  int64 `json:"p99_us"`
	P999 int64 `json:"p999_us"`
	Max  int64 `json:"max_us"`
}

// Second is one second of the timeline.
type Second struct {
	T         int64 `json:"t"` // Unix ms
	Sent      int64 `json:"sent"`
	OK        int64 `json:"ok"`
	Throttled int64 `json:"throttled"`
	Rejected  int64 `json:"rejected"`
	Errors    int64 `json:"errors"`
	DLRs      int64 `json:"dlrs"`
	P50us     int64 `json:"p50_us"`
	P99us     int64 `json:"p99_us"`
}

// TargetResult is one gateway's share of a run.
type TargetResult struct {
	GatewayID  int64   `json:"gateway_id"`
	Name       string  `json:"name"`
	Addr       string  `json:"addr"`
	Sessions   int     `json:"sessions"`
	BindFailed int     `json:"bind_failed"`
	Sent       int64   `json:"sent"`
	OK         int64   `json:"ok"`
	Throttled  int64   `json:"throttled"`
	Rejected   int64   `json:"rejected"`
	Timeouts   int64   `json:"timeouts"`
	DLRs       int64   `json:"dlrs"`
	Latency    Latency `json:"latency"`
}

// SessionResult is one session's share, from the temporary trace.
type SessionResult struct {
	Session  int    `json:"session"`
	Gateway  string `json:"gateway"`
	BindType string `json:"bind_type"`
	Sent     int64  `json:"sent"`
	OK       int64  `json:"ok"`
	NotOK    int64  `json:"not_ok"`
	Deliver  int64  `json:"deliver_sm"`
	P50us    int64  `json:"p50_us"`
	P99us    int64  `json:"p99_us"`
}

// Result is a finished run. sent, ok, unanswered, unexpected, dlrs and
// dlrs_missing keep psg-loadgen's JSON names.
type Result struct {
	Started      time.Time        `json:"started"`
	Finished     time.Time        `json:"finished"`
	Stopped      string           `json:"stopped,omitempty"` // why the run ended early
	OfferedTPS   float64          `json:"offered_tps"`
	AchievedTPS  float64          `json:"achieved_tps"`
	Sessions     int              `json:"sessions"`
	BindFailed   int              `json:"bind_failed"`
	BindRTTms    float64          `json:"bind_rtt_ms"` // median bind round trip: the real network
	Sent         int64            `json:"sent"`
	OK           int64            `json:"ok"`
	Throttled    int64            `json:"throttled"`
	Rejected     int64            `json:"rejected"`
	Timeouts     int64            `json:"timeouts"`
	Unanswered   int64            `json:"unanswered"`
	NotSent      int64            `json:"not_sent_window_or_queue_full"`
	Disconnects  int64            `json:"disconnects"`
	Unexpected   int64            `json:"unexpected"`
	Statuses     map[string]int64 `json:"statuses"` // command_status (hex) of non-OK submit_sm_resp
	Latency      Latency          `json:"latency"`
	DLRs         int64            `json:"dlrs"` // final DLRs received
	DLRsMissing  int              `json:"dlrs_missing"`
	DLRUnmatched int64            `json:"dlrs_unmatched"`
	Intermediate int64            `json:"intermediate_dlrs"`
	DLRStatus    map[string]int64 `json:"dlr_status"`
	DLRDelayP50  int64            `json:"dlr_delay_p50_ms"`
	DLRDelayP99  int64            `json:"dlr_delay_p99_ms"`
	MO           int64            `json:"mo"`
	Errors       int64            `json:"errors"`        // all errors seen
	ErrorsLogged int              `json:"errors_logged"` // kept in the error log (capped)
	Targets      []TargetResult   `json:"targets"`
	PerSession   []SessionResult  `json:"per_session,omitempty"`
	TraceFull    bool             `json:"trace_full,omitempty"` // the trace misses PDUs (size cap, or the disk fell behind): per-session figures are partial
	Timeline     []Second         `json:"timeline"`
}

// ErrorRecord is one entry of the error log.
type ErrorRecord struct {
	TS      time.Time `json:"ts"`
	Gateway string    `json:"gateway"`
	Session int       `json:"session"`
	Kind    string    `json:"kind"` // bind_failed, rejected, throttled, timeout, disconnect, unexpected, dlr_unmatched, dlr_missing
	Command string    `json:"command,omitempty"`
	Seq     uint32    `json:"seq,omitempty"`
	Status  string    `json:"status,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

// Progress is reported every second while a run is going.
type Progress struct {
	Phase   string `json:"phase"` // binding, sending, draining
	Elapsed int64  `json:"elapsed_ms"`
	Last    Second `json:"last"`
	Sent    int64  `json:"sent"`
	OK      int64  `json:"ok"`
	Errors  int64  `json:"errors"`
	DLRs    int64  `json:"dlrs"`
	Bound   int    `json:"bound"`
}

// Hooks let the caller follow a run. All are optional.
type Hooks struct {
	Progress  func(Progress)
	Errors    func([]ErrorRecord) // new log entries, every second
	LowMemory func() bool         // checked every second; true stops the run
}

const maxErrors = 10_000
