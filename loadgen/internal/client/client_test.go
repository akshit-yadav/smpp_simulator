package client

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akshit-yadav/smpp_simulator/worker/pkg/simulator"
	"github.com/akshit-yadav/smpp_simulator/worker/pkg/smpp/pdu"
)

// fakeSMSC is a small SMPP server: it answers binds and submit_sm, sends a
// DLR to a receiver after dlrAfter, and measures how long the client takes
// to answer each deliver_sm.
type fakeSMSC struct {
	t        *testing.T
	ln       net.Listener
	dlrAfter time.Duration
	reject   func(n int64) pdu.Status // status for the n-th submit_sm (1-based)
	silent   func(n int64) bool       // never answer the n-th submit_sm

	submits   atomic.Int64
	marked    atomic.Int64 // binds carrying the simulator marker
	disorder  atomic.Int64 // submit_sm sequence numbers that went backwards
	mu        sync.Mutex
	receivers []*fakeConn
	rr        int
	respWait  []time.Duration // deliver_sm -> deliver_sm_resp
}

type fakeConn struct {
	nc   net.Conn
	wmu  sync.Mutex
	smu  sync.Mutex
	sent map[uint32]time.Time // deliver_sm seq -> sent at
	seq  uint32
}

func (c *fakeConn) write(b []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.nc.Write(b)
}

func newSMSC(t *testing.T) *fakeSMSC {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMSC{t: t, ln: ln, dlrAfter: 20 * time.Millisecond}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(nc)
		}
	}()
	return f
}

func (f *fakeSMSC) addr() string { return f.ln.Addr().String() }

func (f *fakeSMSC) serve(nc net.Conn) {
	defer nc.Close()
	c := &fakeConn{nc: nc, sent: map[uint32]time.Time{}}
	r := pdu.NewReader(nc)
	var lastSeq uint32
	for {
		p, err := r.Read()
		if err != nil {
			return
		}
		switch p.CommandID {
		case pdu.BindTransceiver, pdu.BindTransmitter, pdu.BindReceiver:
			b, _ := pdu.DecodeBind(p.Body)
			if b.Password != "pw" {
				c.write(pdu.Encode(p.CommandID.Resp(), pdu.ESME_RINVPASWD, p.Sequence, nil))
				return
			}
			if b.AddressRange == simulator.AddressRange {
				f.marked.Add(1)
			}
			if p.CommandID != pdu.BindTransmitter {
				f.mu.Lock()
				f.receivers = append(f.receivers, c)
				f.mu.Unlock()
			}
			c.write(pdu.Encode(p.CommandID.Resp(), 0, p.Sequence, pdu.BindResp{SystemID: "FAKE"}.Append(nil)))
		case pdu.SubmitSM:
			if p.Sequence < lastSeq {
				f.disorder.Add(1)
			}
			lastSeq = p.Sequence
			n := f.submits.Add(1)
			if f.silent != nil && f.silent(n) {
				continue
			}
			if f.reject != nil {
				if st := f.reject(n); st != 0 {
					c.write(pdu.Encode(pdu.SubmitSMResp, st, p.Sequence, nil))
					continue
				}
			}
			id := strconv.FormatInt(n, 16)
			c.write(pdu.Encode(pdu.SubmitSMResp, 0, p.Sequence, pdu.MessageIDResp{MessageID: id}.Append(nil)))
			sm, _ := pdu.DecodeSM(p.Body)
			if sm.RegisteredDelivery&1 == 1 {
				time.AfterFunc(f.dlrAfter, func() { f.sendDLR(id) })
			}
		case pdu.DeliverSMResp:
			c.smu.Lock()
			if at, ok := c.sent[p.Sequence]; ok {
				f.mu.Lock()
				f.respWait = append(f.respWait, time.Since(at))
				f.mu.Unlock()
				delete(c.sent, p.Sequence)
			}
			c.smu.Unlock()
		case pdu.Unbind:
			c.write(pdu.Encode(pdu.UnbindResp, 0, p.Sequence, nil))
			return
		}
	}
}

func (f *fakeSMSC) sendDLR(id string) {
	f.mu.Lock()
	if len(f.receivers) == 0 {
		f.mu.Unlock()
		return
	}
	c := f.receivers[f.rr%len(f.receivers)]
	f.rr++
	f.mu.Unlock()
	sm := pdu.SM{ESMClass: 0x04, ShortMessage: []byte("id:" + id + " sub:001 dlvrd:001 stat:DELIVRD err:000")}
	c.smu.Lock()
	c.seq++
	seq := c.seq
	c.sent[seq] = time.Now()
	c.smu.Unlock()
	c.write(pdu.Encode(pdu.DeliverSM, 0, seq, sm.Append(nil)))
}

func spec(f *fakeSMSC, sessions int) Spec {
	return Spec{
		Targets:  []Target{{Name: "fake", Addr: f.addr(), SystemID: "sys", Password: "pw", Sessions: sessions}},
		TPS:      500,
		Duration: 2 * time.Second, RegisteredDelivery: 1, Drain: 3 * time.Second, Simulator: true,
	}
}

func TestRunPacingDLRsAndTimeline(t *testing.T) {
	f := newSMSC(t)
	var progress atomic.Int64
	res, err := Run(context.Background(), spec(f, 2), Hooks{Progress: func(Progress) { progress.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent < 980 || res.Sent > 1020 { // 500/s for 2 s, within 2%
		t.Errorf("sent %d", res.Sent)
	}
	if res.OK != res.Sent || res.Unanswered != 0 || res.DLRs != res.OK || res.DLRsMissing != 0 || res.Unexpected != 0 {
		t.Errorf("result %+v", res)
	}
	if f.marked.Load() != 2 || res.Sessions != 2 || res.BindRTTms <= 0 {
		t.Errorf("marked binds %d, sessions %d, bind rtt %v", f.marked.Load(), res.Sessions, res.BindRTTms)
	}
	if len(res.Timeline) < 2 || progress.Load() < 2 || res.Targets[0].Sent != res.Sent {
		t.Errorf("timeline %d, progress %d, target %+v", len(res.Timeline), progress.Load(), res.Targets[0])
	}
}

func TestNetworkAndResponseDelays(t *testing.T) {
	f := newSMSC(t)
	s := spec(f, 2)
	s.TPS, s.Duration = 200, time.Second
	s.NetDelay = Delay{MinMS: 10, MaxMS: 40}
	s.RespDelay = Delay{MinMS: 150, MaxMS: 150}
	res, err := Run(context.Background(), s, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Latency.P50 < 20_000 || res.Latency.P50 > 120_000 { // two one-way delays of 10-40 ms
		t.Errorf("p50 %d µs with a 10-40 ms network delay each way", res.Latency.P50)
	}
	if f.disorder.Load() != 0 {
		t.Errorf("%d submit_sm arrived out of order", f.disorder.Load())
	}
	if res.DLRs != res.OK || res.DLRsMissing != 0 {
		t.Errorf("DLRs %d of %d, missing %d", res.DLRs, res.OK, res.DLRsMissing)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.respWait {
		if w < 150*time.Millisecond+20*time.Millisecond { // response delay plus both network delays
			t.Fatalf("deliver_sm answered after %v, want at least 170 ms", w)
		}
	}
	if len(f.respWait) == 0 {
		t.Fatal("no deliver_sm_resp seen")
	}
}

func TestErrorsTimeoutsAndTraceCleanup(t *testing.T) {
	old := respTimeout
	respTimeout = 500 * time.Millisecond
	t.Cleanup(func() { respTimeout = old })
	f := newSMSC(t)
	f.reject = func(n int64) pdu.Status {
		if n%4 == 0 {
			return pdu.ESME_RTHROTTLED
		}
		if n%5 == 0 {
			return 0x45
		}
		return 0
	}
	f.silent = func(n int64) bool { return n%50 == 0 }
	s := spec(f, 1)
	s.Targets[0].BindType = "tx_rx"
	s.TraceDir = t.TempDir() + "/trace"
	var logged atomic.Int64
	res, err := Run(context.Background(), s, Hooks{Errors: func(e []ErrorRecord) { logged.Add(int64(len(e))) }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Throttled == 0 || res.Rejected == 0 || res.Timeouts == 0 || res.Statuses["0x45 ESME_RSUBMITFAIL"] == 0 {
		t.Errorf("throttled %d, rejected %d, timeouts %d, statuses %v", res.Throttled, res.Rejected, res.Timeouts, res.Statuses)
	}
	if logged.Load() != int64(res.ErrorsLogged) || res.Errors < res.Throttled+res.Rejected+res.Timeouts {
		t.Errorf("errors %d, logged %d, hook got %d", res.Errors, res.ErrorsLogged, logged.Load())
	}
	if len(res.PerSession) != 2 || res.PerSession[0].Sent == 0 || res.PerSession[1].Deliver == 0 {
		t.Errorf("per session %+v", res.PerSession) // TX sends, RX receives the DLRs
	}
	if _, err := os.Stat(s.TraceDir); !os.IsNotExist(err) {
		t.Errorf("trace directory not removed: %v", err)
	}
}

func TestLowMemoryStopsTheRun(t *testing.T) {
	f := newSMSC(t)
	s := spec(f, 1)
	s.Duration = time.Minute
	start := time.Now()
	res, err := Run(context.Background(), s, Hooks{LowMemory: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != "host memory low" || time.Since(start) > 10*time.Second {
		t.Fatalf("stopped %q after %v", res.Stopped, time.Since(start))
	}
}

func TestBindFailure(t *testing.T) {
	f := newSMSC(t)
	s := spec(f, 2)
	s.Targets[0].Password = "wrong"
	var errs []ErrorRecord
	res, err := Run(context.Background(), s, Hooks{Errors: func(e []ErrorRecord) { errs = append(errs, e...) }})
	if err != nil {
		t.Fatal(err)
	}
	if res.BindFailed != 2 || res.Stopped != "no session could bind" || res.Sent != 0 {
		t.Fatalf("result %+v", res)
	}
	if len(errs) != 2 || errs[0].Kind != "bind_failed" {
		t.Fatalf("errors %+v", errs)
	}
}

func TestTraceLineRoundTrip(t *testing.T) {
	in := traceRec{ts: 1761200000123456, session: 42, kind: 'L', cmd: uint32(pdu.SubmitSMResp), seq: 4294967295, status: 0x58, lat: 1234}
	line := string(in.append(nil))
	if out, ok := parseTraceLine(strings.TrimSuffix(line, "\n")); !ok || out != in {
		t.Fatalf("%q read back as %+v (ok %v)", line, out, ok)
	}
	for _, bad := range []string{"", "1 2 S", "1 2 SS 4 5 6 7", "x 2 S 4 5 6 7", "1 2 S 4 5 6 7 8"} {
		if _, ok := parseTraceLine(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func BenchmarkTraceRead(b *testing.B) {
	line := string(traceRec{ts: 1761200000123456, session: 7, kind: 'L', cmd: uint32(pdu.SubmitSMResp), seq: 123456, lat: 1234}.append(nil))
	line = strings.TrimSuffix(line, "\n")
	for b.Loop() {
		parseTraceLine(line)
	}
}
