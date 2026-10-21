package main

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akshit-yadav/smpp_simulator/worker/pkg/smpp/pdu"
)

// conn is one transceiver session.
type conn struct {
	o     options
	st    *stats
	nc    net.Conn
	r     *pdu.Reader
	queue chan time.Time // scheduled send times from the scheduler

	wmu sync.Mutex
	w   *bufio.Writer

	seq     uint32
	window  chan struct{}
	pmu     sync.Mutex
	pending map[uint32]time.Time
	closed  chan struct{}
}

func dial(o options, st *stats) (*conn, error) {
	nc, err := net.DialTimeout("tcp", o.Addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	c := &conn{
		o: o, st: st, nc: nc,
		r:       pdu.NewReader(nc),
		w:       bufio.NewWriterSize(nc, 64*1024),
		queue:   make(chan time.Time, o.Window*4),
		window:  make(chan struct{}, o.Window),
		pending: make(map[uint32]time.Time, o.Window),
		closed:  make(chan struct{}),
	}
	c.seq++
	bind := pdu.Bind{SystemID: o.SystemID, Password: o.Password, InterfaceVersion: 0x34}
	c.write(pdu.Encode(pdu.BindTransceiver, 0, c.seq, bind.Append(nil)), true)
	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	p, err := c.r.Read()
	if err != nil {
		nc.Close()
		return nil, err
	}
	if p.CommandID != pdu.BindTransceiverResp || p.Status != pdu.ESME_ROK {
		nc.Close()
		return nil, fmt.Errorf("bind failed: %v %v", p.CommandID, p.Status)
	}
	_ = nc.SetReadDeadline(time.Time{})
	go c.readLoop()
	return c, nil
}

func (c *conn) write(b []byte, flush bool) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.w.Write(b); err == nil && flush {
		_ = c.w.Flush()
	}
}

func (c *conn) sendLoop() {
	rd := byte(0)
	if c.o.DLR {
		rd = 1
	}
	var n uint64
	for range c.queue {
		select {
		case c.window <- struct{}{}:
		default:
			c.st.windowFull.Add(1) // open loop: skip rather than wait
			continue
		}
		c.seq++
		if c.seq > 0x7FFFFFFF {
			c.seq = 1
		}
		n++
		sm := pdu.SM{
			SourceAddrTON: 5, SourceAddr: c.o.Src,
			DestAddrTON: 1, DestAddrNPI: 1, DestAddr: c.o.DstPrefix + strconv.Itoa(10_000_000+rand.IntN(90_000_000)),
			RegisteredDelivery: rd,
			ShortMessage:       []byte("PSG load test " + strconv.FormatUint(n, 10)),
		}
		now := time.Now()
		c.pmu.Lock()
		c.pending[c.seq] = now
		c.pmu.Unlock()
		c.write(pdu.Encode(pdu.SubmitSM, 0, c.seq, sm.Append(nil)), len(c.queue) == 0)
		c.st.sent.Add(1)
	}
	c.write(nil, true)
}

func (c *conn) readLoop() {
	defer close(c.closed)
	for {
		p, err := c.r.Read()
		if err != nil {
			return
		}
		switch p.CommandID {
		case pdu.SubmitSMResp:
			c.onSubmitResp(p)
		case pdu.DeliverSM:
			c.onDeliver(p)
		case pdu.EnquireLink:
			c.write(pdu.Encode(pdu.EnquireLinkResp, 0, p.Sequence, nil), true)
		case pdu.Unbind:
			c.write(pdu.Encode(pdu.UnbindResp, 0, p.Sequence, nil), true)
			return
		case pdu.GenericNack:
			c.st.unexpected.Add(1)
		}
	}
}

func (c *conn) onSubmitResp(p pdu.Packet) {
	now := time.Now()
	c.pmu.Lock()
	sentAt, ok := c.pending[p.Sequence]
	delete(c.pending, p.Sequence)
	c.pmu.Unlock()
	if !ok {
		return
	}
	<-c.window
	c.st.recordResp(now.Sub(sentAt))
	switch p.Status {
	case pdu.ESME_ROK:
		c.st.ok.Add(1)
		if c.o.DLR {
			r, _ := pdu.DecodeMessageIDResp(p.Body)
			c.st.trackDLR(r.MessageID, now)
		}
	case pdu.ESME_RTHROTTLED:
		c.st.throttled.Add(1)
	default:
		c.st.rejected.Add(1)
	}
}

func (c *conn) onDeliver(p pdu.Packet) {
	c.write(pdu.Encode(pdu.DeliverSMResp, 0, p.Sequence, []byte{0}), true)
	sm, err := pdu.DecodeSM(p.Body)
	if err != nil || sm.ESMClass&0x04 == 0 {
		c.st.mo.Add(1)
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
	c.st.recordDLR(id, stat, time.Now())
}

func (c *conn) unbind() {
	c.write(pdu.Encode(pdu.Unbind, 0, 0x7FFFFFFF, nil), true)
	select {
	case <-c.closed:
	case <-time.After(2 * time.Second):
	}
	c.nc.Close()
}
