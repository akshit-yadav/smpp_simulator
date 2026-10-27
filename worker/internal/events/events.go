// Package events records submit, DLR and session events and writes them to
// ClickHouse in batches (every second or every 10,000 rows per table), as
// RowBinary over HTTP. If ClickHouse is down, batches spill to local disk and
// are replayed in order once it is back.
//
// The hot path only appends a row to an in-memory buffer under a mutex. A
// full buffer drops the row (counted in psg_events_total{result="dropped"})
// rather than block SMPP traffic.
package events

import (
	"encoding/binary"
	"strings"
	"sync"
	"time"
)

type SubmitResult uint8

// Values match the Enum8s in control/clickhouse/001_events.sql.
const (
	SubmitAccepted SubmitResult = iota + 1
	SubmitRejected
	SubmitThrottled
	SubmitInvalid
	SubmitLicenceThrottled
)

type DLRResult uint8

const (
	DLRAcknowledged DLRResult = iota + 1
	DLRRetried
	DLRFailed
	DLRQueued
	DLRExpired
	MODropped // MO only: no receiver bound and the generator drops
)

type SessionEvent uint8

const (
	SessionBound SessionEvent = iota + 1
	SessionBindFailed
	SessionClosed
	SessionKilled
)

// Submit is one submit_sm and its answer.
type Submit struct {
	TS                 time.Time
	GatewayID          int64
	SessionID          uint64
	SystemID           string
	MessageID          uint64 // 0 unless accepted
	Src, Dst           string
	Result             SubmitResult
	Status             uint32
	Outcome            string
	DLRStatus          string
	DLRError           string
	RegisteredDelivery byte
	DataCoding         byte
	SMLength           uint16
	Simulator          bool // from PSG's own client simulator (psg-tester)
}

// DLR is one step in a delivery receipt's life: acknowledged, retried,
// failed, queued (no receiver bound) or expired (queued too long).
type DLR struct {
	TS        time.Time
	GatewayID int64
	SessionID uint64
	MessageID uint64
	Src, Dst  string // of the original submit_sm
	Result    DLRResult
	DLRStatus string
	DLRError  string
	Attempt   uint8
	SubmitTS  time.Time
	DelayMS   uint32 // submit_sm accepted -> deliver_sm sent
	RespMS    uint32 // deliver_sm sent -> deliver_sm_resp
	Status    uint32 // deliver_sm_resp command_status
	Simulator bool
}

// MO is one step in an MO deliver_sm's life. Result uses the DLR values plus
// MODropped.
type MO struct {
	TS        time.Time
	GatewayID int64
	SessionID uint64
	MessageID uint64 // assigned by PSG for search; MO has no SMSC message ID
	Src, Dst  string
	Result    DLRResult
	Test      bool // sent with "Send test MO"
	UCS2      bool
	Part      uint8
	Parts     uint8
	SMLength  uint16
	Text      string // the start of the whole message's text
	Attempt   uint8
	RespMS    uint32
	Status    uint32
	Simulator bool
}

// Session is a bind, failed bind, close or admin kill.
type Session struct {
	TS        time.Time
	GatewayID int64
	SessionID uint64
	Event     SessionEvent
	BindType  string
	SystemID  string
	Remote    string
	Status    uint32
	Submits   uint64
	Simulator bool
}

// table is one ClickHouse table: its column list and a row encoder. The
// simulator column (layout 2) is always last, so a batch spilled with layout
// 1 is the same rows without it; see legacyColumns.
type table[T any] struct {
	name    string
	columns string
	encode  func(b []byte, worker uint8, e *T) []byte
}

var submitTable = table[Submit]{
	name: "submit_events",
	columns: "ts, worker_id, gateway_id, session_id, system_id, message_id, source_addr, dest_addr, " +
		"result, command_status, outcome, dlr_status, dlr_error, registered_delivery, data_coding, sm_length, simulator",
	encode: func(b []byte, worker uint8, e *Submit) []byte {
		b = appendTime(b, e.TS)
		b = append(b, worker)
		b = binary.LittleEndian.AppendUint32(b, uint32(e.GatewayID))
		b = binary.LittleEndian.AppendUint64(b, e.SessionID)
		b = appendString(b, e.SystemID)
		b = binary.LittleEndian.AppendUint64(b, e.MessageID)
		b = appendString(b, e.Src)
		b = appendString(b, e.Dst)
		b = append(b, byte(e.Result))
		b = binary.LittleEndian.AppendUint32(b, e.Status)
		b = appendString(b, e.Outcome)
		b = appendString(b, e.DLRStatus)
		b = appendString(b, e.DLRError)
		b = append(b, e.RegisteredDelivery, e.DataCoding)
		b = binary.LittleEndian.AppendUint16(b, e.SMLength)
		return append(b, boolByte(e.Simulator))
	},
}

var dlrTable = table[DLR]{
	name: "dlr_events",
	columns: "ts, worker_id, gateway_id, session_id, message_id, source_addr, dest_addr, result, " +
		"dlr_status, dlr_error, attempt, submit_ts, delay_ms, resp_ms, command_status, simulator",
	encode: func(b []byte, worker uint8, e *DLR) []byte {
		b = appendTime(b, e.TS)
		b = append(b, worker)
		b = binary.LittleEndian.AppendUint32(b, uint32(e.GatewayID))
		b = binary.LittleEndian.AppendUint64(b, e.SessionID)
		b = binary.LittleEndian.AppendUint64(b, e.MessageID)
		b = appendString(b, e.Src)
		b = appendString(b, e.Dst)
		b = append(b, byte(e.Result))
		b = appendString(b, e.DLRStatus)
		b = appendString(b, e.DLRError)
		b = append(b, e.Attempt)
		b = appendTime(b, e.SubmitTS)
		b = binary.LittleEndian.AppendUint32(b, e.DelayMS)
		b = binary.LittleEndian.AppendUint32(b, e.RespMS)
		b = binary.LittleEndian.AppendUint32(b, e.Status)
		return append(b, boolByte(e.Simulator))
	},
}

var moTable = table[MO]{
	name: "mo_events",
	columns: "ts, worker_id, gateway_id, session_id, message_id, source_addr, dest_addr, result, test, " +
		"encoding, part, parts, sm_length, text, attempt, resp_ms, command_status, simulator",
	encode: func(b []byte, worker uint8, e *MO) []byte {
		b = appendTime(b, e.TS)
		b = append(b, worker)
		b = binary.LittleEndian.AppendUint32(b, uint32(e.GatewayID))
		b = binary.LittleEndian.AppendUint64(b, e.SessionID)
		b = binary.LittleEndian.AppendUint64(b, e.MessageID)
		b = appendString(b, e.Src)
		b = appendString(b, e.Dst)
		b = append(b, byte(e.Result), boolByte(e.Test))
		if e.UCS2 {
			b = appendString(b, "ucs2")
		} else {
			b = appendString(b, "gsm7")
		}
		b = append(b, e.Part, e.Parts)
		b = binary.LittleEndian.AppendUint16(b, e.SMLength)
		b = appendString(b, e.Text)
		b = append(b, e.Attempt)
		b = binary.LittleEndian.AppendUint32(b, e.RespMS)
		b = binary.LittleEndian.AppendUint32(b, e.Status)
		return append(b, boolByte(e.Simulator))
	},
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

var sessionTable = table[Session]{
	name:    "session_events",
	columns: "ts, worker_id, gateway_id, session_id, event, bind_type, system_id, remote, status, submits, simulator",
	encode: func(b []byte, worker uint8, e *Session) []byte {
		b = appendTime(b, e.TS)
		b = append(b, worker)
		b = binary.LittleEndian.AppendUint32(b, uint32(e.GatewayID))
		b = binary.LittleEndian.AppendUint64(b, e.SessionID)
		b = append(b, byte(e.Event))
		b = appendString(b, e.BindType)
		b = appendString(b, e.SystemID)
		b = appendString(b, e.Remote)
		b = binary.LittleEndian.AppendUint32(b, e.Status)
		b = binary.LittleEndian.AppendUint64(b, e.Submits)
		return append(b, boolByte(e.Simulator))
	},
}

// appendTime encodes a DateTime64(3): milliseconds since the Unix epoch.
func appendTime(b []byte, t time.Time) []byte {
	return binary.LittleEndian.AppendUint64(b, uint64(t.UnixMilli()))
}

func appendString(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// Millis converts a duration to whole milliseconds for DelayMS and RespMS,
// clamped to the column's range.
func Millis(d time.Duration) uint32 {
	return uint32(min(max(d.Milliseconds(), 0), 1<<32-1))
}

// buffer collects one table's rows until the writer takes them. The writer
// hands each taken slice back once encoded, so at steady load the two slices
// are reused instead of reallocated every second.
type buffer[T any] struct {
	mu    sync.Mutex
	rows  []T
	spare []T
}

// add appends e and returns the new length, or -1 if the buffer is full.
func (b *buffer[T]) add(e *T, max int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.rows) >= max {
		return -1
	}
	b.rows = append(b.rows, *e)
	return len(b.rows)
}

func (b *buffer[T]) take() []T {
	b.mu.Lock()
	defer b.mu.Unlock()
	rows := b.rows
	b.rows, b.spare = b.spare, nil
	return rows
}

// giveBack returns a slice from take for reuse. Slices that grew past a
// batch are dropped, so a burst does not pin memory.
func (b *buffer[T]) giveBack(rows []T) {
	if cap(rows) > batchRows {
		return
	}
	clear(rows) // drop the strings
	b.mu.Lock()
	b.spare = rows[:0]
	b.mu.Unlock()
}

// layout is the row layout written to spill files: 2 added the simulator
// column at the end of every table.
const layout = 2

// legacyColumns is the column list of a table in layout 1.
func legacyColumns(columns string) string { return strings.TrimSuffix(columns, ", simulator") }
