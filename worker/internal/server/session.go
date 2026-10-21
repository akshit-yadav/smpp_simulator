package server

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/msgid"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/ratelimit"
	"github.com/akshit-yadav/smpp_simulator/worker/pkg/smpp/pdu"
)

const (
	outQueueLen = 4096 // encoded PDUs waiting for the writer
	dlrQueueLen = 8192 // DLRs waiting for a window slot on this session
	windowSize  = 1000 // max deliver_sm in flight per session
	smscName    = "PSG"
)

var sessionIDs atomic.Uint64

// Session is one SMPP connection. The read loop handles requests in order and
// queues responses; the write loop batches writes and flushes when idle.
type Session struct {
	gw     *Gateway
	m      *Manager
	conn   net.Conn
	id     uint64
	remote string

	out        chan []byte // nil entry = flush, then close the connection
	done       chan struct{}
	writerDone chan struct{}
	flushing   bool // read loop asked the writer to flush and close
	closeOnce  sync.Once

	bound    atomic.Bool
	bindType string
	systemID string
	boundAt  time.Time
	submits  atomic.Uint64
	bucket   *ratelimit.Bucket // per-bind TPS
	lastCfg  *runtimeConfig

	seq     atomic.Uint32
	dlrCh   chan *dlrJob
	window  chan struct{}
	pendMu  sync.Mutex
	pending map[uint32]*inflight
}

type inflight struct {
	job    *dlrJob
	sentAt time.Time
}

func newSession(gw *Gateway, conn net.Conn) *Session {
	return &Session{
		gw:         gw,
		m:          gw.m,
		conn:       conn,
		id:         sessionIDs.Add(1),
		remote:     conn.RemoteAddr().String(),
		out:        make(chan []byte, outQueueLen),
		done:       make(chan struct{}),
		writerDone: make(chan struct{}),
		dlrCh:      make(chan *dlrJob, dlrQueueLen),
		window:     make(chan struct{}, windowSize),
		pending:    map[uint32]*inflight{},
	}
}

func (s *Session) serve() {
	defer func() {
		if s.flushing { // let the final response (e.g. a failed bind_resp) reach the client
			select {
			case <-s.writerDone:
			case <-time.After(2 * time.Second):
			}
		}
		s.close()
	}()
	go s.writeLoop()

	r := pdu.NewReader(s.conn)
	for {
		timeout := s.m.opt.BindTimeout
		if s.bound.Load() {
			timeout = s.m.opt.IdleTimeout
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(timeout))
		p, err := r.Read()
		if err != nil {
			if errors.Is(err, pdu.ErrInvalidLength) {
				s.respond(pdu.GenericNack, pdu.ESME_RINVCMDLEN, p.Sequence, nil)
				s.flushAndClose()
			}
			return
		}
		if !s.handle(p) {
			return
		}
	}
}

// handle processes one PDU; false ends the read loop.
func (s *Session) handle(p pdu.Packet) bool {
	switch p.CommandID {
	case pdu.BindTransmitter, pdu.BindReceiver, pdu.BindTransceiver:
		return s.handleBind(p)
	case pdu.EnquireLink:
		s.respond(pdu.EnquireLinkResp, pdu.ESME_ROK, p.Sequence, nil)
		return true
	case pdu.Unbind:
		s.respond(pdu.UnbindResp, pdu.ESME_ROK, p.Sequence, nil)
		s.flushAndClose()
		return false
	case pdu.UnbindResp:
		return false
	case pdu.DeliverSMResp, pdu.GenericNack:
		s.handleDeliverResp(p)
		return true
	}

	if p.CommandID.IsResponse() {
		return true // unexpected response: ignore
	}
	if !s.bound.Load() {
		s.respond(p.CommandID.Resp(), pdu.ESME_RINVBNDSTS, p.Sequence, nil)
		return true
	}
	switch p.CommandID {
	case pdu.SubmitSM:
		s.handleSubmit(p)
	default:
		s.respond(pdu.GenericNack, pdu.ESME_RINVCMDID, p.Sequence, nil)
	}
	return true
}

func bindTypeOf(id pdu.CommandID) string {
	switch id {
	case pdu.BindTransmitter:
		return gateway.BindTX
	case pdu.BindReceiver:
		return gateway.BindRX
	}
	return gateway.BindTRX
}

func (s *Session) handleBind(p pdu.Packet) bool {
	resp := p.CommandID.Resp()
	if s.bound.Load() {
		s.respond(resp, pdu.ESME_RALYBND, p.Sequence, failedBindBody(resp))
		return true
	}
	rc := s.gw.config()
	bt := bindTypeOf(p.CommandID)
	b, err := pdu.DecodeBind(p.Body)

	status := pdu.ESME_ROK
	var fe *pdu.FieldError
	switch {
	case errors.As(err, &fe) && fe.Field == "system_id":
		status = pdu.ESME_RINVSYSID
	case errors.As(err, &fe) && fe.Field == "password":
		status = pdu.ESME_RINVPASWD
	case err != nil:
		status = pdu.ESME_RBINDFAIL
	case b.SystemID != rc.SystemID:
		status = pdu.ESME_RINVSYSID
	case subtle.ConstantTimeCompare([]byte(b.Password), []byte(rc.Password)) != 1:
		status = pdu.ESME_RINVPASWD
	case !rc.AllowsBind(bt):
		status = pdu.ESME_RINVBNDSTS
	case !s.gw.reserveBind(rc.MaxBinds):
		status = pdu.ESME_RBINDFAIL
	}

	log := s.m.log.With("gateway_id", s.gw.id, "session", s.id, "remote", s.remote, "system_id", b.SystemID, "bind_type", bt)
	if status != pdu.ESME_ROK {
		s.gw.stats.BindFail.Add(1)
		log.Warn("bind rejected", "status", status.String())
		s.respond(resp, status, p.Sequence, failedBindBody(resp))
		s.flushAndClose()
		return false
	}

	s.bindType, s.systemID, s.boundAt = bt, b.SystemID, time.Now().UTC()
	s.bucket = ratelimit.New(rc.TPS.Limit)
	s.lastCfg = rc
	s.bound.Store(true)
	s.gw.stats.BindOK.Add(1)
	s.gw.stats.Sessions(bt).Add(1)
	body := pdu.BindResp{SystemID: smscName, TLVs: pdu.TLVs{pdu.ByteTLV(pdu.TagSCInterfaceVersion, 0x34)}}.Append(nil)
	s.respond(resp, pdu.ESME_ROK, p.Sequence, body)
	if bt != gateway.BindTX {
		go s.dlrLoop()
		s.gw.addReceiver(s)
	}
	log.Info("bound")
	return true
}

// failedBindBody follows SMPP 3.4 (no body on error) except for
// bind_transceiver_resp, where common clients (e.g. gosmpp) require system_id.
func failedBindBody(resp pdu.CommandID) []byte {
	if resp == pdu.BindTransceiverResp {
		return pdu.BindResp{SystemID: smscName}.Append(nil)
	}
	return nil
}

func (s *Session) handleSubmit(p pdu.Packet) {
	s.submits.Add(1)
	if s.bindType == gateway.BindRX {
		s.respond(pdu.SubmitSMResp, pdu.ESME_RINVBNDSTS, p.Sequence, nil)
		return
	}
	rc := s.gw.config()
	if rc != s.lastCfg {
		s.bucket.SetRate(rc.TPS.Limit)
		s.lastCfg = rc
	}

	sm, err := pdu.DecodeSM(p.Body)
	if err != nil {
		s.gw.stats.Invalid.Add(1)
		s.respond(pdu.SubmitSMResp, submitDecodeStatus(err), p.Sequence, nil)
		return
	}

	bucket := s.bucket
	if rc.TPS.Scope == gateway.TPSPerGateway {
		bucket = rc.gwBucket
	}
	if !bucket.Allow() {
		s.gw.stats.Throttled.Add(1)
		s.respond(pdu.SubmitSMResp, pdu.ESME_RTHROTTLED, p.Sequence, nil)
		return
	}

	o := rc.picker.Pick()
	if !o.Accepted() {
		s.gw.stats.Rejected.Add(1)
		s.respond(pdu.SubmitSMResp, o.CommandStatus, p.Sequence, nil)
		return
	}

	now := time.Now()
	id := s.m.ids.Next()
	hexID := msgid.Hex(id)
	s.respond(pdu.SubmitSMResp, pdu.ESME_ROK, p.Sequence, pdu.MessageIDResp{MessageID: hexID}.Append(nil))
	s.gw.stats.Accepted.Add(1)
	s.m.scheduleDLR(rc, &sm, id, o, now)
}

func submitDecodeStatus(err error) pdu.Status {
	var fe *pdu.FieldError
	if errors.As(err, &fe) {
		switch fe.Field {
		case "source_addr":
			return pdu.ESME_RINVSRCADR
		case "destination_addr":
			return pdu.ESME_RINVDSTADR
		case "short_message", "sm_length":
			return pdu.ESME_RINVMSGLEN
		case "service_type":
			return pdu.ESME_RINVSERTYP
		}
	}
	return pdu.ESME_RINVCMDLEN
}

func (s *Session) respond(id pdu.CommandID, status pdu.Status, seq uint32, body []byte) {
	s.send(pdu.Encode(id, status, seq, body))
}

// send queues an encoded PDU, blocking while the writer is behind (back
// pressure on the reader). It drops the PDU if the session is closed.
func (s *Session) send(b []byte) bool {
	select {
	case s.out <- b:
		return true
	case <-s.done:
		return false
	}
}

func (s *Session) writeLoop() {
	defer close(s.writerDone)
	w := bufio.NewWriterSize(s.conn, 64*1024)
	for {
		select {
		case b := <-s.out:
			if b == nil {
				_ = w.Flush()
				s.conn.Close()
				return
			}
			if _, err := w.Write(b); err != nil {
				s.close()
				return
			}
			if len(s.out) == 0 {
				if err := w.Flush(); err != nil {
					s.close()
					return
				}
			}
		case <-s.done:
			return
		}
	}
}

// flushAndClose closes the connection after everything queued is written.
// Only called from the read loop.
func (s *Session) flushAndClose() {
	s.flushing = true
	s.send(nil)
}

// unbindAndClose asks the client to unbind, then closes after a grace period.
func (s *Session) unbindAndClose() {
	if s.bound.Load() {
		s.send(pdu.Encode(pdu.Unbind, pdu.ESME_ROK, s.nextSeq(), nil))
		time.AfterFunc(2*time.Second, s.close)
		return
	}
	s.close()
}

func (s *Session) nextSeq() uint32 {
	for {
		n := s.seq.Add(1)
		if n > 0 && n <= 0x7FFFFFFF {
			return n
		}
		s.seq.CompareAndSwap(n, 0)
	}
}

func (s *Session) close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.conn.Close()
		wasBound := s.bound.Load()
		s.gw.removeSession(s, wasBound)
		if wasBound {
			s.gw.stats.Sessions(s.bindType).Add(-1)
		}
		s.requeueDLRs()
		s.m.log.Info("session closed", "gateway_id", s.gw.id, "session", s.id, "remote", s.remote, "bound", wasBound)
	})
}

// SessionInfo describes one bound session for the control plane.
type SessionInfo struct {
	ID        uint64    `json:"id"`
	GatewayID int64     `json:"gateway_id"`
	BindType  string    `json:"bind_type"`
	SystemID  string    `json:"system_id"`
	Remote    string    `json:"remote"`
	BoundAt   time.Time `json:"bound_at"`
	Submits   uint64    `json:"submits"`
	DLRsOut   int       `json:"dlrs_in_flight"`
}

func (s *Session) info() SessionInfo {
	s.pendMu.Lock()
	inflight := len(s.pending)
	s.pendMu.Unlock()
	return SessionInfo{
		ID: s.id, GatewayID: s.gw.id, BindType: s.bindType, SystemID: s.systemID,
		Remote: s.remote, BoundAt: s.boundAt, Submits: s.submits.Load(), DLRsOut: inflight,
	}
}
