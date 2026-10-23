package client

import (
	"bufio"
	"bytes"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"github.com/akshit-yadav/smpp_simulator/worker/pkg/simulator"
	"github.com/akshit-yadav/smpp_simulator/worker/pkg/smpp/pdu"
)

// respTimeout is how long a submit_sm may go unanswered (a variable for tests).
var respTimeout = 30 * time.Second

// session is one bound SMPP connection. All writes go through out to a
// single writer (which applies the network delay); reads go through the
// reader (and, with a network delay, an inbound delay line).
type session struct {
	run      *run
	idx      int
	target   int
	bindType string // trx, tx, rx
	nc       net.Conn
	r        *pdu.Reader
	rtt      time.Duration // bind round trip

	queue  chan time.Time // submit_sm slots from the pacer (sending sessions)
	window chan struct{}
	seq    atomic.Uint32

	outMu   sync.Mutex // orders due times with channel order
	outLast time.Time
	out     chan outPDU
	stop    chan struct{} // ends the writer
	done    chan struct{} // closed when the reader ends
	closing atomic.Bool

	pmu     sync.Mutex
	pending map[uint32]time.Time

	delayedResp atomic.Int64 // deliver_sm_resp waiting for the response delay
}

type outPDU struct {
	due time.Time
	b   []byte
}

func (s *session) sends() bool { return s.bindType != "rx" }

func dial(r *run, idx, target int, bindType string) (*session, error) {
	t := r.spec.Targets[target]
	nc, err := net.DialTimeout("tcp", t.Addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	s := &session{
		run: r, idx: idx, target: target, bindType: bindType, nc: nc,
		r:       pdu.NewReader(nc),
		queue:   make(chan time.Time, r.spec.Window*4),
		window:  make(chan struct{}, r.spec.Window),
		out:     make(chan outPDU, r.spec.Window*4+64),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		pending: make(map[uint32]time.Time, r.spec.Window),
	}
	cmd := map[string]pdu.CommandID{"trx": pdu.BindTransceiver, "tx": pdu.BindTransmitter, "rx": pdu.BindReceiver}[bindType]
	bind := pdu.Bind{SystemID: t.SystemID, Password: t.Password, InterfaceVersion: 0x34}
	if r.spec.Simulator {
		bind.AddressRange = simulator.AddressRange
	}
	start := time.Now()
	_ = nc.SetDeadline(start.Add(5 * time.Second))
	if _, err := nc.Write(pdu.Encode(cmd, 0, s.nextSeq(), bind.Append(nil))); err != nil {
		nc.Close()
		return nil, err
	}
	p, err := s.r.Read()
	if err != nil {
		nc.Close()
		return nil, err
	}
	if p.CommandID != cmd.Resp() || p.Status != pdu.ESME_ROK {
		nc.Close()
		return nil, fmt.Errorf("bind refused: %v", p.Status)
	}
	s.rtt = time.Since(start)
	_ = nc.SetDeadline(time.Time{})
	go s.writeLoop()
	go s.readLoop()
	return s, nil
}

func (s *session) nextSeq() uint32 {
	for {
		n := s.seq.Add(1)
		if n > 0 && n <= 0x7FFFFFFF {
			return n
		}
		s.seq.CompareAndSwap(n, 0)
	}
}

// write queues a PDU for the writer, after the outbound network delay. Due
// times never go backwards, so PDUs keep their order on the wire.
func (s *session) write(b []byte) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	due := time.Time{}
	if d := s.run.spec.NetDelay; d.On() {
		due = time.Now().Add(d.pick())
		if due.Before(s.outLast) {
			due = s.outLast
		}
		s.outLast = due
	}
	select {
	case s.out <- outPDU{due, b}:
	case <-s.stop:
	}
}

func (s *session) writeLoop() {
	w := bufio.NewWriterSize(s.nc, 64<<10)
	defer w.Flush()
	for {
		select {
		case <-s.stop:
			return
		case o := <-s.out:
			if d := time.Until(o.due); d > 0 {
				_ = w.Flush()
				time.Sleep(d)
			}
			if _, err := w.Write(o.b); err != nil {
				return
			}
			if len(s.out) == 0 {
				if err := w.Flush(); err != nil {
					return
				}
			}
		}
	}
}

type inPDU struct {
	due time.Time
	p   pdu.Packet
}

func (s *session) readLoop() {
	defer close(s.done)
	var in chan inPDU
	if d := s.run.spec.NetDelay; d.On() {
		in = make(chan inPDU, 4096)
		go s.inLoop(in)
		defer close(in)
	}
	var last time.Time
	for {
		p, err := s.r.Read()
		if err != nil {
			if !s.closing.Load() {
				s.run.disconnects.Add(1)
				s.run.logError(s, "disconnect", "", 0, "", err.Error())
			}
			return
		}
		if in == nil {
			s.handle(p, time.Now())
			continue
		}
		due := time.Now().Add(s.run.spec.NetDelay.pick())
		if due.Before(last) {
			due = last
		}
		last = due
		p.Body = bytes.Clone(p.Body) // the reader reuses its buffer on the next Read
		in <- inPDU{due, p}
	}
}

// inLoop handles received PDUs once their inbound network delay has passed.
func (s *session) inLoop(in chan inPDU) {
	for x := range in {
		if d := time.Until(x.due); d > 0 {
			time.Sleep(d)
		}
		s.handle(x.p, time.Now())
	}
}

func (s *session) handle(p pdu.Packet, now time.Time) {
	s.run.trace(s, 'R', p.CommandID, p.Sequence, p.Status, 0)
	switch p.CommandID {
	case pdu.SubmitSMResp:
		s.onSubmitResp(p, now)
	case pdu.DeliverSM:
		s.onDeliver(p, now)
	case pdu.EnquireLink:
		s.write(pdu.Encode(pdu.EnquireLinkResp, 0, p.Sequence, nil))
	case pdu.Unbind:
		s.closing.Store(true)
		s.write(pdu.Encode(pdu.UnbindResp, 0, p.Sequence, nil))
	case pdu.UnbindResp, pdu.EnquireLinkResp:
	default:
		s.run.unexpected.Add(1)
		s.run.logError(s, "unexpected", p.CommandID.String(), p.Sequence, p.Status.String(), "")
	}
}

// sendLoop sends a submit_sm for every slot the pacer gives it. Open loop: a
// full window skips the slot (counted) rather than waiting.
func (s *session) sendLoop() {
	rd := s.run.spec.RegisteredDelivery
	t := &s.run.targets[s.target]
	for range s.queue {
		select {
		case <-s.done: // disconnected
			s.run.notSent.Add(1)
			continue
		default:
		}
		select {
		case s.window <- struct{}{}:
		default:
			s.run.windowFull.Add(1)
			continue
		}
		n := s.run.msgSeq.Add(1)
		sm := pdu.SM{
			SourceAddrTON: 5, SourceAddr: s.run.spec.Src,
			DestAddrTON: 1, DestAddrNPI: 1,
			DestAddr:           s.run.spec.DstPrefix + strconv.Itoa(10_000_000+rand.IntN(90_000_000)),
			RegisteredDelivery: rd,
		}
		text := strings.ReplaceAll(s.run.spec.Text, "{seq}", strconv.FormatInt(n, 10))
		if s.run.spec.UCS2 {
			sm.DataCoding = 0x08
			for _, u := range utf16.Encode([]rune(text)) {
				sm.ShortMessage = append(sm.ShortMessage, byte(u>>8), byte(u))
			}
		} else {
			sm.ShortMessage = []byte(text)
		}
		if len(sm.ShortMessage) > 254 {
			sm.ShortMessage = sm.ShortMessage[:254]
		}
		seq := s.nextSeq()
		now := time.Now()
		s.pmu.Lock()
		s.pending[seq] = now
		s.pmu.Unlock()
		s.write(pdu.Encode(pdu.SubmitSM, 0, seq, sm.Append(nil)))
		s.run.sent.Add(1)
		t.sent.Add(1)
		s.run.trace(s, 'S', pdu.SubmitSM, seq, 0, 0)
	}
}

func (s *session) onSubmitResp(p pdu.Packet, now time.Time) {
	s.pmu.Lock()
	sentAt, ok := s.pending[p.Sequence]
	delete(s.pending, p.Sequence)
	s.pmu.Unlock()
	if !ok { // answered after its timeout: counted as one, but its DLR is still this run's
		if p.Status == pdu.ESME_ROK && s.run.spec.wantsDLR() {
			r, _ := pdu.DecodeMessageIDResp(p.Body)
			s.run.trackDLR(r.MessageID, now)
		}
		return
	}
	<-s.window
	lat := now.Sub(sentAt)
	t := &s.run.targets[s.target]
	s.run.recordResp(t, lat)
	s.run.trace(s, 'L', pdu.SubmitSMResp, p.Sequence, p.Status, lat)
	switch p.Status {
	case pdu.ESME_ROK:
		s.run.ok.Add(1)
		t.ok.Add(1)
		if s.run.spec.wantsDLR() {
			r, _ := pdu.DecodeMessageIDResp(p.Body)
			s.run.trackDLR(r.MessageID, now)
		}
	case pdu.ESME_RTHROTTLED:
		s.run.throttled.Add(1)
		t.throttled.Add(1)
		s.run.status(p.Status)
		s.run.logError(s, "throttled", "submit_sm_resp", p.Sequence, p.Status.String(), "")
	default:
		s.run.rejected.Add(1)
		t.rejected.Add(1)
		s.run.status(p.Status)
		s.run.logError(s, "rejected", "submit_sm_resp", p.Sequence, p.Status.String(), "")
	}
}

// onDeliver records a DLR or MO and answers it, after the response delay.
func (s *session) onDeliver(p pdu.Packet, now time.Time) {
	resp := pdu.Encode(pdu.DeliverSMResp, 0, p.Sequence, []byte{0})
	if d := s.run.spec.RespDelay; d.On() {
		s.delayedResp.Add(1)
		time.AfterFunc(d.pick(), func() {
			s.write(resp)
			s.delayedResp.Add(-1)
		})
	} else {
		s.write(resp)
	}
	sm, err := pdu.DecodeSM(p.Body)
	if err != nil || sm.ESMClass&0x04 == 0 {
		s.run.mo.Add(1)
		return
	}
	id := ""
	if v, ok := sm.TLVs.Get(pdu.TagReceiptedMessageID); ok {
		id = strings.TrimRight(string(v), "\x00")
	} else if i := strings.Index(string(sm.ShortMessage), "id:"); i >= 0 {
		id, _, _ = strings.Cut(string(sm.ShortMessage[i+3:]), " ")
	}
	stat := ""
	if i := strings.Index(string(sm.ShortMessage), "stat:"); i >= 0 {
		stat, _, _ = strings.Cut(string(sm.ShortMessage[i+5:]), " ")
	}
	s.run.recordDLR(s, id, stat, now)
}

// sweep times out submit_sm unanswered for 30 s, freeing their window slots.
func (s *session) sweep(now time.Time) {
	var expired []uint32
	s.pmu.Lock()
	for seq, at := range s.pending {
		if now.Sub(at) > respTimeout {
			expired = append(expired, seq)
			delete(s.pending, seq)
		}
	}
	s.pmu.Unlock()
	for _, seq := range expired {
		<-s.window
		s.run.timeouts.Add(1)
		s.run.targets[s.target].timeouts.Add(1)
		s.run.logError(s, "timeout", "submit_sm", seq, "", "no submit_sm_resp within 30 s")
	}
}

func (s *session) unanswered() int64 {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	return int64(len(s.pending))
}

// unbind ends the session politely, then closes it. Answers still waiting
// for the response delay go out first, so PSG sees every DLR acknowledged.
func (s *session) unbind() {
	wait := time.Now().Add(time.Duration(s.run.spec.RespDelay.MaxMS)*time.Millisecond + time.Second)
	for s.delayedResp.Load() > 0 && time.Now().Before(wait) {
		time.Sleep(10 * time.Millisecond)
	}
	s.closing.Store(true)
	s.write(pdu.Encode(pdu.Unbind, 0, s.nextSeq(), nil))
	select {
	case <-s.done:
	case <-time.After(2*time.Second + 2*time.Duration(s.run.spec.NetDelay.MaxMS)*time.Millisecond):
	}
	close(s.stop)
	s.nc.Close()
}
