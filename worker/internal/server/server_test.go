package server

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linxGnu/gosmpp"
	"github.com/linxGnu/gosmpp/data"
	gpdu "github.com/linxGnu/gosmpp/pdu"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/outcome"
	"github.com/akshit-yadav/smpp_simulator/worker/pkg/smpp/pdu"
)

// ---- harness -------------------------------------------------------------------

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func testConfig(t *testing.T) gateway.Config {
	c := gateway.Builtin
	c.ID, c.Name, c.Port = 1, "gw1", freePort(t)
	c.SystemID, c.Password = "vendora", "secret1"
	c.DLR.Timing, c.DLR.DelayMS = gateway.TimingFixed, 100
	return c
}

func startManager(t *testing.T, cfgs ...gateway.Config) *Manager {
	t.Helper()
	m := NewManager(Options{ListenHost: "127.0.0.1", DLRTimeout: 2 * time.Second})
	for _, r := range m.Apply(cfgs) {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return m
}

func addr(c gateway.Config) string { return "127.0.0.1:" + strconv.Itoa(c.Port) }

// client wraps a gosmpp session and collects what it receives.
type client struct {
	sess  *gosmpp.Session
	resps chan *gpdu.SubmitSMResp
	dlrs  chan *gpdu.DeliverSM
}

func bind(t *testing.T, c gateway.Config, kind, systemID, password string) (*client, error) {
	t.Helper()
	cl := &client{resps: make(chan *gpdu.SubmitSMResp, 10000), dlrs: make(chan *gpdu.DeliverSM, 10000)}
	auth := gosmpp.Auth{SMSC: addr(c), SystemID: systemID, Password: password}
	var conn gosmpp.Connector
	switch kind {
	case "tx":
		conn = gosmpp.TXConnector(gosmpp.NonTLSDialer, auth)
	case "rx":
		conn = gosmpp.RXConnector(gosmpp.NonTLSDialer, auth)
	default:
		conn = gosmpp.TRXConnector(gosmpp.NonTLSDialer, auth)
	}
	sess, err := gosmpp.NewSession(conn, gosmpp.Settings{
		EnquireLink: 200 * time.Millisecond, // exercise enquire_link
		ReadTimeout: 5 * time.Second,
		OnPDU: func(p gpdu.PDU, _ bool) {
			switch pd := p.(type) {
			case *gpdu.SubmitSMResp:
				cl.resps <- pd
			case *gpdu.DeliverSM:
				cl.dlrs <- pd
			}
		},
	}, -1)
	if err != nil {
		return nil, err
	}
	cl.sess = sess
	t.Cleanup(func() { _ = sess.Close() })
	return cl, nil
}

func mustBind(t *testing.T, c gateway.Config, kind string) *client {
	t.Helper()
	cl, err := bind(t, c, kind, c.SystemID, string(c.Password))
	if err != nil {
		t.Fatalf("bind %s: %v", kind, err)
	}
	return cl
}

func newSubmit(t *testing.T, text string, rd byte) *gpdu.SubmitSM {
	t.Helper()
	sm := gpdu.NewSubmitSM().(*gpdu.SubmitSM)
	src, dst := gpdu.NewAddress(), gpdu.NewAddress()
	src.SetTon(5)
	_ = src.SetAddress("HEXWIRE")
	dst.SetTon(1)
	dst.SetNpi(1)
	_ = dst.SetAddress("919812345678")
	sm.SourceAddr, sm.DestAddr = src, dst
	if err := sm.Message.SetMessageWithEncoding(text, data.GSM7BIT); err != nil {
		t.Fatal(err)
	}
	sm.RegisteredDelivery = rd
	return sm
}

func (cl *client) submit(t *testing.T, sm *gpdu.SubmitSM) {
	t.Helper()
	var err error
	if tx := cl.sess.Transmitter(); tx != nil {
		err = tx.Submit(sm)
	} else {
		err = cl.sess.Transceiver().Submit(sm)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func waitResp(t *testing.T, cl *client) *gpdu.SubmitSMResp {
	t.Helper()
	select {
	case r := <-cl.resps:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("no submit_sm_resp")
		return nil
	}
}

func waitDLR(t *testing.T, cl *client, within time.Duration) *gpdu.DeliverSM {
	t.Helper()
	select {
	case d := <-cl.dlrs:
		return d
	case <-time.After(within):
		t.Fatal("no DLR")
		return nil
	}
}

func noDLR(t *testing.T, cl *client, wait time.Duration) {
	t.Helper()
	select {
	case d := <-cl.dlrs:
		msg, _ := d.Message.GetMessage()
		t.Fatalf("unexpected DLR: %s", msg)
	case <-time.After(wait):
	}
}

func bindStatus(err error) data.CommandStatusType {
	var be gosmpp.BindError
	if errors.As(err, &be) {
		return be.CommandStatus
	}
	return -1
}

// ---- tests ---------------------------------------------------------------------

func TestBindAuthentication(t *testing.T) {
	c := testConfig(t)
	c.BindTypes = []string{gateway.BindTX, gateway.BindTRX}
	c.MaxBinds = 2
	startManager(t, c)

	cases := []struct {
		name, kind, sys, pw string
		want                pdu.Status
	}{
		{"wrong password", "trx", c.SystemID, "nope", pdu.ESME_RINVPASWD},
		{"wrong system id", "trx", "other", string(c.Password), pdu.ESME_RINVSYSID},
		{"bind type not allowed", "rx", c.SystemID, string(c.Password), pdu.ESME_RINVBNDSTS},
	}
	for _, tc := range cases {
		_, err := bind(t, c, tc.kind, tc.sys, tc.pw)
		if got := bindStatus(err); uint32(got) != uint32(tc.want) {
			t.Errorf("%s: got %v (%v), want %v", tc.name, got, err, tc.want)
		}
	}

	mustBind(t, c, "tx")
	mustBind(t, c, "trx")
	_, err := bind(t, c, "trx", c.SystemID, string(c.Password))
	if got := bindStatus(err); uint32(got) != uint32(pdu.ESME_RBINDFAIL) {
		t.Fatalf("third bind with max_binds=2: got %v", err)
	}
}

func TestSubmitAndDLRTiming(t *testing.T) {
	c := testConfig(t)
	c.DLR.DelayMS = 300
	startManager(t, c)
	cl := mustBind(t, c, "trx")

	start := time.Now()
	cl.submit(t, newSubmit(t, "Your OTP is 123456 - do not share", 1))
	r := waitResp(t, cl)
	if r.CommandStatus != data.ESME_ROK || r.MessageID == "" {
		t.Fatalf("resp: status %v id %q", r.CommandStatus, r.MessageID)
	}

	d := waitDLR(t, cl, 2*time.Second)
	elapsed := time.Since(start)
	if elapsed < 300*time.Millisecond || elapsed > 340*time.Millisecond {
		t.Errorf("DLR after %v, want 300ms ±20ms (+client overhead)", elapsed)
	}
	if d.EsmClass != 0x04 {
		t.Errorf("esm_class 0x%02X", d.EsmClass)
	}
	text, _ := d.Message.GetMessage()
	for _, want := range []string{"id:" + r.MessageID + " ", "sub:001 dlvrd:001", "stat:DELIVRD", "err:000", "text:Your OTP is 123456 -"} {
		if !strings.Contains(text, want) {
			t.Errorf("DLR text %q missing %q", text, want)
		}
	}
	if f, ok := d.OptionalParameters[gpdu.TagReceiptedMessageID]; !ok || strings.TrimRight(string(f.Data), "\x00") != r.MessageID {
		t.Errorf("receipted_message_id TLV = %q", f.Data)
	}
	if f := d.OptionalParameters[gpdu.TagMessageStateOption]; len(f.Data) != 1 || f.Data[0] != 2 {
		t.Errorf("message_state TLV = %v", f.Data)
	}
	if d.SourceAddr.Address() != "919812345678" || d.DestAddr.Address() != "HEXWIRE" {
		t.Errorf("DLR addresses %s -> %s", d.SourceAddr.Address(), d.DestAddr.Address())
	}
}

func TestRegisteredDeliveryRules(t *testing.T) {
	c := testConfig(t)
	c.DLR.Timing = gateway.TimingImmediate
	m := startManager(t, c)
	cl := mustBind(t, c, "trx")

	cl.submit(t, newSubmit(t, "no dlr", 0))
	waitResp(t, cl)
	noDLR(t, cl, 200*time.Millisecond)

	cl.submit(t, newSubmit(t, "failure only, delivered", 2))
	waitResp(t, cl)
	noDLR(t, cl, 200*time.Millisecond)

	c.Outcome = outcome.Profile{Mode: outcome.ModeFixed, Outcomes: []outcome.Outcome{
		{DLRStatus: "UNDELIV", DLRError: "T01", NetworkError: &outcome.NetworkError{Type: 3, Code: 11}},
	}}
	m.Apply([]gateway.Config{c})
	cl.submit(t, newSubmit(t, "failure only, failed", 2))
	waitResp(t, cl)
	d := waitDLR(t, cl, time.Second)
	text, _ := d.Message.GetMessage()
	if !strings.Contains(text, "stat:UNDELIV err:T01") || !strings.Contains(text, "dlvrd:000") {
		t.Fatalf("DLR text %q", text)
	}
	if f := d.OptionalParameters[gpdu.TagNetworkErrorCode]; string(f.Data) != "\x03\x00\x0b" {
		t.Fatalf("network_error_code = %v", f.Data)
	}

	c.DLR.Enabled = false
	c.Outcome = outcome.Default
	m.Apply([]gateway.Config{c})
	cl.submit(t, newSubmit(t, "dlr off", 1))
	waitResp(t, cl)
	noDLR(t, cl, 200*time.Millisecond)
}

func TestRejectedOutcomeGetsNoIDOrDLR(t *testing.T) {
	c := testConfig(t)
	c.DLR.Timing = gateway.TimingImmediate
	c.Outcome = outcome.Profile{Mode: outcome.ModeFixed, Outcomes: []outcome.Outcome{{CommandStatus: 0x401}}}
	startManager(t, c)
	cl := mustBind(t, c, "trx")

	cl.submit(t, newSubmit(t, "rejected", 1))
	r := waitResp(t, cl)
	if r.CommandStatus != 0x401 || r.MessageID != "" {
		t.Fatalf("status %v id %q", r.CommandStatus, r.MessageID)
	}
	noDLR(t, cl, 200*time.Millisecond)
}

func TestTPSLimitThrottles(t *testing.T) {
	c := testConfig(t)
	c.DLR.Enabled = false
	c.TPS = gateway.TPS{Limit: 20, Scope: gateway.TPSPerBind}
	startManager(t, c)
	cl := mustBind(t, c, "trx")

	for range 100 {
		cl.submit(t, newSubmit(t, "x", 0))
	}
	ok, throttled := 0, 0
	for range 100 {
		switch r := waitResp(t, cl); r.CommandStatus {
		case data.ESME_ROK:
			ok++
		case data.ESME_RTHROTTLED:
			throttled++
		default:
			t.Fatalf("unexpected status %v", r.CommandStatus)
		}
	}
	if ok < 20 || ok > 25 || throttled != 100-ok {
		t.Fatalf("ok %d throttled %d", ok, throttled)
	}
}

func TestDLRQueuedUntilReceiverBinds(t *testing.T) {
	c := testConfig(t)
	c.DLR.Timing = gateway.TimingImmediate
	m := startManager(t, c)
	tx := mustBind(t, c, "tx")

	for range 5 {
		tx.submit(t, newSubmit(t, "queued", 1))
		waitResp(t, tx)
	}
	time.Sleep(100 * time.Millisecond)
	if n := m.queue.TotalLen(); n != 5 {
		t.Fatalf("queued %d, want 5", n)
	}

	rx := mustBind(t, c, "rx")
	for range 5 {
		waitDLR(t, rx, 2*time.Second)
	}
	if n := m.queue.TotalLen(); n != 0 {
		t.Fatalf("queue not drained: %d", n)
	}
}

func TestDLRRoundRobinAcrossReceivers(t *testing.T) {
	c := testConfig(t)
	c.DLR.Timing = gateway.TimingImmediate
	startManager(t, c)
	tx := mustBind(t, c, "tx")
	rx1, rx2 := mustBind(t, c, "rx"), mustBind(t, c, "rx")

	for range 20 {
		tx.submit(t, newSubmit(t, "rr", 1))
		waitResp(t, tx)
	}
	time.Sleep(300 * time.Millisecond)
	if len(rx1.dlrs) != 10 || len(rx2.dlrs) != 10 {
		t.Fatalf("receiver split %d/%d, want 10/10", len(rx1.dlrs), len(rx2.dlrs))
	}
}

func TestLiveConfigChanges(t *testing.T) {
	c := testConfig(t)
	m := startManager(t, c)
	mustBind(t, c, "trx")

	// Password change applies to new binds.
	c.Password = "newpw"
	m.Apply([]gateway.Config{c})
	if _, err := bind(t, c, "trx", c.SystemID, "secret1"); err == nil {
		t.Fatal("old password still accepted")
	}
	mustBind(t, c, "trx")

	// Port change moves the listener.
	oldPort := c.Port
	c.Port = freePort(t)
	start := time.Now()
	if r := m.Apply([]gateway.Config{c}); r[0].Action != "restarted" || r[0].Err != nil {
		t.Fatalf("apply: %+v", r)
	}
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(oldPort), 200*time.Millisecond); err == nil {
		t.Fatal("old port still listening")
	}
	mustBind(t, c, "trx")

	// Disabling stops the listener.
	c.Status = gateway.StatusDisabled
	m.Apply([]gateway.Config{c})
	if _, err := net.DialTimeout("tcp", addr(c), 200*time.Millisecond); err == nil {
		t.Fatal("disabled gateway still listening")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("changes took %v", time.Since(start))
	}

	// Removing everything is fine too.
	m.Apply(nil)
}

func TestPortConflict(t *testing.T) {
	c := testConfig(t)
	ln, err := net.Listen("tcp", addr(c))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	m := NewManager(Options{ListenHost: "127.0.0.1"})
	r := m.Apply([]gateway.Config{c})
	var pe *PortError
	if !errors.As(r[0].Err, &pe) || !strings.Contains(pe.Error(), "is already in use") {
		t.Fatalf("got %+v", r)
	}
}

// Raw-protocol tests for cases a well-behaved client never produces.

func rawConn(t *testing.T, c gateway.Config) (net.Conn, *pdu.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr(c))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	return conn, pdu.NewReader(conn)
}

func TestRawProtocolEdgeCases(t *testing.T) {
	c := testConfig(t)
	startManager(t, c)

	conn, r := rawConn(t, c)
	// submit_sm before bind
	conn.Write(pdu.Encode(pdu.SubmitSM, 0, 7, pdu.SM{DestAddr: "1"}.Append(nil)))
	p, err := r.Read()
	if err != nil || p.CommandID != pdu.SubmitSMResp || p.Status != pdu.ESME_RINVBNDSTS || p.Sequence != 7 {
		t.Fatalf("pre-bind submit: %+v %v", p, err)
	}
	// bind, then an unknown command
	conn.Write(pdu.Encode(pdu.BindTransceiver, 0, 8, pdu.Bind{SystemID: c.SystemID, Password: string(c.Password), InterfaceVersion: 0x34}.Append(nil)))
	if p, _ = r.Read(); p.Status != pdu.ESME_ROK {
		t.Fatalf("bind: %v", p.Status)
	}
	conn.Write(pdu.Encode(0x00001234, 0, 9, nil))
	if p, _ = r.Read(); p.CommandID != pdu.GenericNack || p.Status != pdu.ESME_RINVCMDID {
		t.Fatalf("unknown command: %v %v", p.CommandID, p.Status)
	}
	// malformed submit_sm body
	conn.Write(pdu.Encode(pdu.SubmitSM, 0, 10, []byte("CMT\x00\x01\x01ABC")))
	if p, _ = r.Read(); p.CommandID != pdu.SubmitSMResp || p.Status != pdu.ESME_RINVSRCADR {
		t.Fatalf("malformed submit: %v %v", p.CommandID, p.Status)
	}
	// bind again on a bound session
	conn.Write(pdu.Encode(pdu.BindTransceiver, 0, 11, pdu.Bind{SystemID: c.SystemID}.Append(nil)))
	if p, _ = r.Read(); p.Status != pdu.ESME_RALYBND {
		t.Fatalf("rebind: %v", p.Status)
	}
	// unbind
	conn.Write(pdu.Encode(pdu.Unbind, 0, 12, nil))
	if p, _ = r.Read(); p.CommandID != pdu.UnbindResp {
		t.Fatalf("unbind: %v", p.CommandID)
	}
	if _, err := r.Read(); err == nil {
		t.Fatal("connection not closed after unbind")
	}

	// invalid command_length: generic_nack then close
	conn2, r2 := rawConn(t, c)
	conn2.Write([]byte{0, 0, 0, 4, 0, 0, 0, 0x15, 0, 0, 0, 0, 0, 0, 0, 1})
	if p, _ = r2.Read(); p.CommandID != pdu.GenericNack || p.Status != pdu.ESME_RINVCMDLEN {
		t.Fatalf("bad length: %v %v", p.CommandID, p.Status)
	}
}

func TestDLRRetriedAfterErrorResponse(t *testing.T) {
	c := testConfig(t)
	c.DLR.Timing = gateway.TimingImmediate
	startManager(t, c)

	conn, r := rawConn(t, c)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write(pdu.Encode(pdu.BindTransceiver, 0, 1, pdu.Bind{SystemID: c.SystemID, Password: string(c.Password)}.Append(nil)))
	r.Read()
	conn.Write(pdu.Encode(pdu.SubmitSM, 0, 2, pdu.SM{SourceAddr: "A", DestAddr: "B", RegisteredDelivery: 1, ShortMessage: []byte("hi")}.Append(nil)))

	var mu sync.Mutex
	deliveries := 0
	start := time.Now()
	for deliveries < 2 {
		p, err := r.Read()
		if err != nil {
			t.Fatal(err)
		}
		if p.CommandID != pdu.DeliverSM {
			continue
		}
		mu.Lock()
		deliveries++
		status := pdu.ESME_RSYSERR // fail the first attempt
		if deliveries == 2 {
			status = pdu.ESME_ROK
		}
		mu.Unlock()
		conn.Write(pdu.Encode(pdu.DeliverSMResp, status, p.Sequence, []byte{0}))
	}
	if d := time.Since(start); d < 900*time.Millisecond || d > 1500*time.Millisecond {
		t.Fatalf("retry after %v, want ~1s backoff", d)
	}
}

func TestSessionsKillAndStats(t *testing.T) {
	c := testConfig(t)
	c.DLR.Enabled = false
	m := startManager(t, c)
	cl := mustBind(t, c, "trx")
	for range 3 {
		cl.submit(t, newSubmit(t, "x", 0))
		waitResp(t, cl)
	}

	ss := m.Sessions()
	if len(ss) != 1 || ss[0].GatewayID != 1 || ss[0].BindType != "trx" || ss[0].SystemID != "vendora" || ss[0].Submits != 3 {
		t.Fatalf("sessions %+v", ss)
	}
	st := m.StatsSnapshot()[1]
	if st.Accepted != 3 || st.BindOK != 1 || st.Sessions["trx"] != 1 {
		t.Fatalf("stats %+v", st)
	}

	if m.KillSession(999999) {
		t.Fatal("killed a session that does not exist")
	}
	if !m.KillSession(ss[0].ID) {
		t.Fatal("kill failed")
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(m.Sessions()) != 0 || m.StatsSnapshot()[1].Sessions["trx"] != 0 {
		if time.Now().After(deadline) {
			t.Fatal("session still bound after kill")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
