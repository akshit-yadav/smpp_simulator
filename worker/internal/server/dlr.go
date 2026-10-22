package server

import (
	"math/rand/v2"
	"time"
	"unicode/utf8"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/events"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/mo"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/msgid"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/outcome"
	"github.com/akshit-yadav/smpp_simulator/worker/pkg/smpp/pdu"
)

// dlrJob is one deliver_sm waiting to be sent: a delivery receipt or, with
// Kind jobMO, an MO message (part). Both share the window, retries and queue.
// Field names are short because queued jobs are persisted to Redis as JSON.
type dlrJob struct {
	GatewayID int64           `json:"g"`
	MsgID     uint64          `json:"m"`
	Src       string          `json:"s"` // original source = DLR destination
	SrcTON    byte            `json:"st"`
	SrcNPI    byte            `json:"sn"`
	Dst       string          `json:"d"` // original destination = DLR source
	DstTON    byte            `json:"dt"`
	DstNPI    byte            `json:"dn"`
	SubmitAt  time.Time       `json:"t"`
	Text      string          `json:"x,omitempty"`
	Outcome   outcome.Outcome `json:"o"`
	Attempts  int             `json:"a,omitempty"`
	QueuedAt  time.Time       `json:"q,omitzero"`
	Interim   bool            `json:"i,omitempty"` // intermediate DLR; the final one is a separate job

	// MO only. Src is the subscriber, Dst the short code; Text the start of
	// the whole message (for events).
	Kind    byte   `json:"k,omitempty"`
	Payload []byte `json:"p,omitempty"` // short_message octets, with UDH if ESM has 0x40
	Coding  byte   `json:"c,omitempty"`
	ESM     byte   `json:"e,omitempty"`
	Part    uint8  `json:"pn,omitempty"`
	Parts   uint8  `json:"pt,omitempty"`
	Test    bool   `json:"tst,omitempty"`
}

const jobMO byte = 1

func (j *dlrJob) isMO() bool { return j.Kind == jobMO }

// wantDLR applies registered_delivery: bits 0-1 = 01 always, 10 on failure
// only; intermediate statuses need bit 4.
func wantDLR(rd byte, o *outcome.Outcome) bool {
	if outcome.IsIntermediate(o.DLRStatus) {
		return rd&0x10 != 0
	}
	switch rd & 0x03 {
	case 1:
		return true
	case 2:
		return !o.Delivered()
	}
	return false
}

// wantIntermediate reports whether an intermediate DLR goes before the final
// one. Never when the outcome's own status is already intermediate.
func wantIntermediate(cfg gateway.DLR, rd byte, o *outcome.Outcome) bool {
	if outcome.IsIntermediate(o.DLRStatus) {
		return false
	}
	switch cfg.Intermediate {
	case gateway.IntermediateRequested:
		return rd&0x10 != 0
	case gateway.IntermediateAlways:
		return rd&0x13 != 0
	}
	return false
}

func dlrDelay(cfg gateway.DLR, o *outcome.Outcome) time.Duration {
	lo, hi := 0, 0
	switch {
	case o.DLRDelay != nil:
		lo, hi = o.DLRDelay.MinMS, o.DLRDelay.MaxMS
	case cfg.Timing == gateway.TimingFixed:
		lo, hi = cfg.DelayMS, cfg.DelayMS
	case cfg.Timing == gateway.TimingRandom:
		lo, hi = cfg.MinMS, cfg.MaxMS
	}
	ms := lo
	if hi > lo {
		ms += rand.IntN(hi - lo + 1)
	}
	return time.Duration(ms) * time.Millisecond
}

// receiptText returns up to 20 characters of the original message for the
// DLR "text:" field, for GSM/ASCII/Latin-1 data codings only.
func receiptText(sm *pdu.SM) string {
	if sm.DataCoding != 0 && sm.DataCoding != 1 && sm.DataCoding != 3 {
		return ""
	}
	msg := sm.Message()
	if sm.ESMClass&0x40 != 0 && len(msg) > 0 && int(msg[0])+1 <= len(msg) { // skip UDH
		msg = msg[int(msg[0])+1:]
	}
	b := make([]byte, 0, 20)
	for i := 0; i < len(msg) && len(b) < 20; i++ {
		if c := msg[i]; c >= 0x20 && c < 0x7F {
			b = append(b, c)
		}
	}
	return string(b)
}

func (m *Manager) scheduleDLR(rc *runtimeConfig, sm *pdu.SM, id uint64, o *outcome.Outcome, now time.Time) {
	if !rc.DLR.Enabled {
		return
	}
	final, interim := wantDLR(sm.RegisteredDelivery, o), wantIntermediate(rc.DLR, sm.RegisteredDelivery, o)
	if !final && !interim {
		return
	}
	j := &dlrJob{
		GatewayID: rc.ID, MsgID: id,
		Src: sm.SourceAddr, SrcTON: sm.SourceAddrTON, SrcNPI: sm.SourceAddrNPI,
		Dst: sm.DestAddr, DstTON: sm.DestAddrTON, DstNPI: sm.DestAddrNPI,
		SubmitAt: now, Outcome: *o,
	}
	if rc.DLR.IncludeText {
		j.Text = receiptText(sm)
	}
	var interimAt time.Time
	if interim {
		ij := *j
		ij.Interim = true
		ij.Outcome = outcome.Outcome{Name: o.Name, DLRStatus: rc.DLR.IntermediateStatus, DLRError: "000"}
		interimAt = now.Add(time.Duration(rc.DLR.IntermediateMS) * time.Millisecond)
		m.scheduleAt(&ij, interimAt, now)
	}
	if final {
		at := now.Add(dlrDelay(rc.DLR, o))
		if interim && !at.After(interimAt) {
			at = interimAt.Add(wheelTick) // the final DLR never overtakes the intermediate one
		}
		m.scheduleAt(j, at, now)
	}
}

func (m *Manager) scheduleAt(j *dlrJob, at, now time.Time) {
	if at.After(now) {
		m.wheel.Add(at, j)
		return
	}
	m.dispatch(j)
}

// dispatch hands a due DLR or MO to a bound receiver, or queues it. An MO
// with no receiver bound is dropped instead if the generator says so.
func (m *Manager) dispatch(j *dlrJob) {
	gw := m.gateway(j.GatewayID)
	if gw == nil {
		return // gateway deleted
	}
	for range 3 {
		s := gw.nextReceiver()
		if s == nil {
			break
		}
		select {
		case s.dlrCh <- j:
			return
		default: // that session is backed up; try the next one
		}
	}
	if j.isMO() && !gw.hasReceivers() {
		if mc := gw.config().MO; mc != nil && mc.NoReceiver == gateway.MODrop {
			gw.stats.countDelivery(j, events.MODropped)
			m.recordQueued(j, events.MODropped)
			return
		}
	}
	gw.stats.countDelivery(j, events.DLRQueued)
	if j.QueuedAt.IsZero() {
		j.QueuedAt = time.Now()
	}
	m.recordQueued(j, events.DLRQueued)
	m.queue.push(j)
}

// recordQueued emits an event for a job entering, expiring from or (MO)
// dropped instead of the queue. Call it while no other goroutine can change j.
func (m *Manager) recordQueued(j *dlrJob, result events.DLRResult) {
	m.record(j, events.DLR{
		TS: time.Now(), GatewayID: j.GatewayID, MessageID: j.MsgID, Src: j.Src, Dst: j.Dst, Result: result,
		DLRStatus: j.Outcome.DLRStatus, DLRError: j.Outcome.DLRError, Attempt: attempt(j), SubmitTS: j.SubmitAt,
	})
}

// record emits e as a dlr_events row, or as an mo_events row for an MO job.
func (m *Manager) record(j *dlrJob, e events.DLR) {
	if !j.isMO() {
		m.events.DLR(e)
		return
	}
	m.events.MO(events.MO{
		TS: e.TS, GatewayID: e.GatewayID, SessionID: e.SessionID, MessageID: e.MessageID, Src: j.Src, Dst: j.Dst,
		Result: e.Result, Test: j.Test, UCS2: j.Coding == mo.CodingUCS2, Part: j.Part, Parts: j.Parts,
		SMLength: uint16(len(j.Payload)), Text: j.Text, Attempt: e.Attempt, RespMS: e.RespMS, Status: e.Status,
	})
}

// dlrEvent describes a deliver_sm this session sent. Build it before a retry
// changes the job.
func (s *Session) dlrEvent(f *inflight, now time.Time, status pdu.Status, answered bool) events.DLR {
	j := f.job
	e := events.DLR{
		TS: now, GatewayID: j.GatewayID, SessionID: s.id, MessageID: j.MsgID, Src: j.Src, Dst: j.Dst,
		DLRStatus: j.Outcome.DLRStatus, DLRError: j.Outcome.DLRError, Attempt: attempt(j),
		SubmitTS: j.SubmitAt, DelayMS: events.Millis(f.sentAt.Sub(j.SubmitAt)), Status: uint32(status),
	}
	if answered {
		e.RespMS = events.Millis(now.Sub(f.sentAt))
	}
	return e
}

func attempt(j *dlrJob) uint8 { return uint8(min(j.Attempts+1, 0xFF)) }

// retryDLR re-schedules a DLR after a deliver_sm_resp error or timeout, with
// exponential backoff, up to Options.DLRRetries times. It returns
// DLRRetried, or DLRFailed once the retries are used up.
func (m *Manager) retryDLR(gw *Gateway, j *dlrJob) events.DLRResult {
	j.Attempts++
	if j.Attempts > m.opt.DLRRetries {
		gw.stats.countDelivery(j, events.DLRFailed)
		return events.DLRFailed
	}
	gw.stats.countDelivery(j, events.DLRRetried)
	m.wheel.Add(time.Now().Add(time.Second<<(j.Attempts-1)), j)
	return events.DLRRetried
}

// dlrLoop sends this session's DLRs, keeping at most windowSize in flight and
// retrying any that time out.
func (s *Session) dlrLoop() {
	sweep := time.NewTicker(time.Second)
	defer sweep.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-sweep.C:
			s.sweepTimeouts()
		case j := <-s.dlrCh:
			if !s.acquireWindow(sweep.C) {
				s.m.dispatch(j)
				return
			}
			seq := s.nextSeq()
			now := time.Now()
			s.pendMu.Lock()
			s.pending[seq] = &inflight{job: j, sentAt: now}
			s.pendMu.Unlock()
			var b []byte
			if j.isMO() {
				b = encodeMO(j, seq)
			} else {
				b = encodeDLR(s.gw.config(), j, seq, now)
			}
			if s.send(b) {
				s.gw.stats.countSent(j)
			}
		}
	}
}

func (s *Session) acquireWindow(sweep <-chan time.Time) bool {
	for {
		select {
		case s.window <- struct{}{}:
			return true
		case <-sweep:
			s.sweepTimeouts()
		case <-s.done:
			return false
		}
	}
}

func (s *Session) handleDeliverResp(p pdu.Packet) {
	s.pendMu.Lock()
	f, ok := s.pending[p.Sequence]
	delete(s.pending, p.Sequence)
	s.pendMu.Unlock()
	if !ok {
		return
	}
	<-s.window
	e := s.dlrEvent(f, time.Now(), p.Status, true)
	if p.CommandID == pdu.DeliverSMResp && p.Status == pdu.ESME_ROK {
		s.gw.stats.countDelivery(f.job, events.DLRAcknowledged)
		e.Result = events.DLRAcknowledged
	} else {
		e.Result = s.m.retryDLR(s.gw, f.job)
	}
	s.m.record(f.job, e)
}

func (s *Session) sweepTimeouts() {
	now := time.Now()
	cutoff := now.Add(-s.m.opt.DLRTimeout)
	var expired []*inflight
	s.pendMu.Lock()
	for seq, f := range s.pending {
		if f.sentAt.Before(cutoff) {
			expired = append(expired, f)
			delete(s.pending, seq)
		}
	}
	s.pendMu.Unlock()
	for _, f := range expired {
		<-s.window
		e := s.dlrEvent(f, now, pdu.ESME_ROK, false)
		e.Result = s.m.retryDLR(s.gw, f.job)
		s.m.record(f.job, e)
	}
}

// requeueDLRs re-dispatches DLRs stuck on a closed session: queued ones and
// in-flight ones without a response (the client may see those twice). It runs
// twice to catch jobs handed over while the session was closing.
func (s *Session) requeueDLRs() {
	collect := func() []*dlrJob {
		var jobs []*dlrJob
		for {
			select {
			case j := <-s.dlrCh:
				jobs = append(jobs, j)
				continue
			default:
			}
			break
		}
		s.pendMu.Lock()
		for seq, f := range s.pending {
			jobs = append(jobs, f.job)
			delete(s.pending, seq)
		}
		s.pendMu.Unlock()
		return jobs
	}
	go func() {
		for _, j := range collect() {
			s.m.dispatch(j)
		}
		time.Sleep(200 * time.Millisecond)
		for _, j := range collect() {
			s.m.dispatch(j)
		}
	}()
}

// encodeDLR builds the deliver_sm receipt:
//
//	id:… sub:001 dlvrd:… submit date:… done date:… stat:… err:… text:…
func encodeDLR(rc *runtimeConfig, j *dlrJob, seq uint32, now time.Time) []byte {
	o := &j.Outcome
	cfg := rc.DLR

	id := msgid.Hex(j.MsgID)
	if cfg.IDFormat == gateway.IDDecimal {
		id = msgid.Decimal(j.MsgID)
	}
	layout := "0601021504"
	if cfg.DateSeconds {
		layout = "060102150405"
	}
	dlvrd := "000"
	if o.Delivered() {
		dlvrd = "001"
	}
	errCode := o.DLRError
	if errCode == "" {
		errCode = "000"
	}

	t := make([]byte, 0, 160)
	t = append(t, "id:"...)
	t = append(t, id...)
	t = append(t, " sub:001 dlvrd:"...)
	t = append(t, dlvrd...)
	t = append(t, " submit date:"...)
	t = j.SubmitAt.AppendFormat(t, layout)
	t = append(t, " done date:"...)
	t = now.AppendFormat(t, layout)
	t = append(t, " stat:"...)
	t = append(t, o.DLRStatus...)
	t = append(t, " err:"...)
	t = append(t, errCode...)
	if cfg.IncludeText {
		text := j.Text
		if o.DLRText != "" {
			text = o.DLRText
		}
		t = append(t, " text:"...)
		t = append(t, truncate(text, 20)...)
	}

	sm := pdu.SM{
		SourceAddrTON: j.DstTON, SourceAddrNPI: j.DstNPI, SourceAddr: j.Dst,
		DestAddrTON: j.SrcTON, DestAddrNPI: j.SrcNPI, DestAddr: j.Src,
		ESMClass:     0x04,
		ShortMessage: t,
	}
	if cfg.IncludeTLVs {
		sm.TLVs = pdu.TLVs{
			pdu.CStringTLV(pdu.TagReceiptedMessageID, msgid.Hex(j.MsgID)),
			pdu.ByteTLV(pdu.TagMessageState, outcome.MessageStates[o.DLRStatus]),
		}
		if ne := o.NetworkError; ne != nil {
			sm.TLVs = append(sm.TLVs, pdu.NetworkErrorCodeTLV(ne.Type, ne.Code))
		}
	}
	return pdu.Encode(pdu.DeliverSM, pdu.ESME_ROK, seq, sm.Append(make([]byte, 0, 64+len(t))))
}

// encodeMO builds an MO deliver_sm (esm_class 0x00, or 0x40 with a UDH).
func encodeMO(j *dlrJob, seq uint32) []byte {
	sm := pdu.SM{
		SourceAddrTON: 1, SourceAddrNPI: 1, SourceAddr: j.Src,
		DestAddrTON: 0, DestAddrNPI: 1, DestAddr: j.Dst,
		ESMClass: j.ESM, DataCoding: j.Coding, ShortMessage: j.Payload,
	}
	return pdu.Encode(pdu.DeliverSM, pdu.ESME_ROK, seq, sm.Append(make([]byte, 0, 48+len(j.Payload))))
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
