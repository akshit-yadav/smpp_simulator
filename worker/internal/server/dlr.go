package server

import (
	"math/rand/v2"
	"time"
	"unicode/utf8"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/msgid"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/outcome"
	"github.com/akshit-yadav/smpp_simulator/worker/pkg/smpp/pdu"
)

// dlrJob is one delivery receipt waiting to be sent. Field names are short
// because queued jobs are persisted to Redis as JSON.
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
}

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
	if !rc.DLR.Enabled || !wantDLR(sm.RegisteredDelivery, o) {
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
	if d := dlrDelay(rc.DLR, o); d > 0 {
		m.wheel.Add(now.Add(d), j)
		return
	}
	m.dispatch(j)
}

// dispatch hands a due DLR to a bound receiver, or queues it.
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
	gw.metrics.dlrQueued.Inc()
	if j.QueuedAt.IsZero() {
		j.QueuedAt = time.Now()
	}
	m.queue.push(j)
}

// retryDLR re-schedules a DLR after a deliver_sm_resp error or timeout, with
// exponential backoff, up to Options.DLRRetries times.
func (m *Manager) retryDLR(gw *Gateway, j *dlrJob) {
	j.Attempts++
	if j.Attempts > m.opt.DLRRetries {
		gw.metrics.dlrFailed.Inc()
		return
	}
	gw.metrics.dlrRetried.Inc()
	m.wheel.Add(time.Now().Add(time.Second<<(j.Attempts-1)), j)
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
			if s.send(encodeDLR(s.gw.config(), j, seq, now)) {
				s.gw.metrics.dlrSent.Inc()
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
	if p.CommandID == pdu.DeliverSMResp && p.Status == pdu.ESME_ROK {
		s.gw.metrics.dlrOK.Inc()
		return
	}
	s.m.retryDLR(s.gw, f.job)
}

func (s *Session) sweepTimeouts() {
	cutoff := time.Now().Add(-s.m.opt.DLRTimeout)
	var expired []*dlrJob
	s.pendMu.Lock()
	for seq, f := range s.pending {
		if f.sentAt.Before(cutoff) {
			expired = append(expired, f.job)
			delete(s.pending, seq)
		}
	}
	s.pendMu.Unlock()
	for _, j := range expired {
		<-s.window
		s.m.retryDLR(s.gw, j)
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

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
