package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linxGnu/gosmpp"
	"github.com/linxGnu/gosmpp/data"
	gpdu "github.com/linxGnu/gosmpp/pdu"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/chtest"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/events"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/licensing"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/outcome"
	"github.com/akshit-yadav/smpp_simulator/worker/pkg/licence"
	"github.com/akshit-yadav/smpp_simulator/worker/pkg/simulator"
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
	return startManagerWith(t, Options{}, cfgs...)
}

func startManagerWith(t *testing.T, opt Options, cfgs ...gateway.Config) *Manager {
	t.Helper()
	opt.ListenHost, opt.DLRTimeout = "127.0.0.1", 2*time.Second
	m := NewManager(opt)
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

func TestEventsReachClickHouse(t *testing.T) {
	db := "psg_server_events_test"
	chURL := chtest.DB(t, db)
	rec, err := events.New(events.Options{WorkerID: 1, URL: chURL, Database: db, SpillDir: t.TempDir(), SpillMaxBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	recCtx, stopRec := context.WithCancel(context.Background())
	recDone := make(chan struct{})
	go func() { rec.Run(recCtx); close(recDone) }()

	c := testConfig(t)
	m := startManagerWith(t, Options{Events: rec}, c)
	if _, err := bind(t, c, "tx", c.SystemID, "wrong"); err == nil {
		t.Fatal("bind with a wrong password succeeded")
	}
	cl := mustBind(t, c, "trx")
	cl.submit(t, newSubmit(t, "hello", 1))
	r := waitResp(t, cl)
	waitDLR(t, cl, 2*time.Second)
	for deadline := time.Now().Add(3 * time.Second); m.StatsSnapshot()[1].DLRAck == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("deliver_sm_resp not received")
		}
	}
	_ = cl.sess.Close()
	for deadline := time.Now().Add(3 * time.Second); len(m.Sessions()) > 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("session still bound")
		}
	}
	stopRec()
	<-recDone

	id, _ := strconv.ParseUint(r.MessageID, 16, 64)
	want := map[string]string{
		"SELECT result, message_id, source_addr, dest_addr, system_id, outcome, dlr_status, sm_length FROM submit_events": fmt.Sprintf("accepted\t%d\tHEXWIRE\t919812345678\tvendora\tdelivered\tDELIVRD\t5", id),
		"SELECT result, message_id, attempt, dlr_status, delay_ms BETWEEN 100 AND 1000 FROM dlr_events":                   fmt.Sprintf("acknowledged\t%d\t1\tDELIVRD\t1", id),
		"SELECT groupArray(toString(event)), groupArray(status) FROM (SELECT * FROM session_events ORDER BY ts)":          "['bind_failed','bound','closed']\t[14,0,0]",
	}
	for q, w := range want {
		if got := chtest.Query(t, chURL, db, q); got != w {
			t.Errorf("%s\n got %q\nwant %q", q, got, w)
		}
	}
}

func dlrStat(t *testing.T, d *gpdu.DeliverSM) string {
	t.Helper()
	text, _ := d.Message.GetMessage()
	i := strings.Index(text, "stat:")
	if i < 0 {
		t.Fatalf("DLR text %q", text)
	}
	return text[i+5 : i+12]
}

func TestIntermediateDLRs(t *testing.T) {
	c := testConfig(t)
	c.DLR.Timing, c.DLR.DelayMS = gateway.TimingFixed, 200
	c.DLR.Intermediate, c.DLR.IntermediateStatus, c.DLR.IntermediateMS = gateway.IntermediateRequested, "ENROUTE", 50
	m := startManager(t, c)
	cl := mustBind(t, c, "trx")

	// "requested": only with registered_delivery bit 4.
	cl.submit(t, newSubmit(t, "final only", 0x01))
	waitResp(t, cl)
	if s := dlrStat(t, waitDLR(t, cl, time.Second)); s != "DELIVRD" {
		t.Fatalf("got %s, want only the final DLR", s)
	}
	noDLR(t, cl, 100*time.Millisecond)

	start := time.Now()
	cl.submit(t, newSubmit(t, "with intermediate", 0x11))
	r := waitResp(t, cl)
	first := waitDLR(t, cl, time.Second)
	atFirst := time.Since(start)
	second := waitDLR(t, cl, time.Second)
	if dlrStat(t, first) != "ENROUTE" || dlrStat(t, second) != "DELIVRD" {
		t.Fatalf("DLRs %s then %s", dlrStat(t, first), dlrStat(t, second))
	}
	if atFirst < 50*time.Millisecond || atFirst > 120*time.Millisecond {
		t.Errorf("intermediate DLR after %v, want ~50 ms", atFirst)
	}
	text, _ := first.Message.GetMessage()
	if !strings.Contains(text, "id:"+r.MessageID+" ") || !strings.Contains(text, "dlvrd:000") || !strings.Contains(text, "err:000") {
		t.Errorf("intermediate DLR text %q", text)
	}
	if f := first.OptionalParameters[gpdu.TagMessageStateOption]; len(f.Data) != 1 || f.Data[0] != 1 {
		t.Errorf("intermediate message_state TLV = %v, want 1 (ENROUTE)", f.Data)
	}

	// "always": for any requested DLR. An intermediate delay past the final
	// delay still sends the intermediate DLR first.
	c.DLR.Intermediate, c.DLR.IntermediateStatus, c.DLR.IntermediateMS = gateway.IntermediateAlways, "ACCEPTD", 300
	m.Apply([]gateway.Config{c})
	cl.submit(t, newSubmit(t, "always", 0x01))
	waitResp(t, cl)
	a, b := waitDLR(t, cl, time.Second), waitDLR(t, cl, time.Second)
	if dlrStat(t, a) != "ACCEPTD" || dlrStat(t, b) != "DELIVRD" {
		t.Fatalf("DLRs %s then %s", dlrStat(t, a), dlrStat(t, b))
	}
	cl.submit(t, newSubmit(t, "no receipt wanted", 0))
	waitResp(t, cl)
	noDLR(t, cl, 400*time.Millisecond)

	// With no receiver bound, both DLRs queue side by side and are delivered in order.
	c.DLR.Timing, c.DLR.IntermediateMS = gateway.TimingImmediate, 0
	m.Apply([]gateway.Config{c})
	_ = cl.sess.Close()
	tx := mustBind(t, c, "tx")
	tx.submit(t, newSubmit(t, "queued", 0x01))
	waitResp(t, tx)
	for deadline := time.Now().Add(2 * time.Second); m.queue.TotalLen() != 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("queued %d DLRs, want 2", m.queue.TotalLen())
		}
	}
	rx := mustBind(t, c, "rx")
	if s1, s2 := dlrStat(t, waitDLR(t, rx, 2*time.Second)), dlrStat(t, waitDLR(t, rx, 2*time.Second)); s1 != "ACCEPTD" || s2 != "DELIVRD" {
		t.Fatalf("queued DLRs %s then %s", s1, s2)
	}
}

func moConfig(runID int64) *gateway.MO {
	return &gateway.MO{
		Enabled: true, RunID: runID, Mode: gateway.MORate, Rate: 200,
		Sources:      gateway.MOSources{Kind: "list", List: []string{"919800000001"}},
		Destinations: []string{"56161"}, DestOrder: "round_robin",
		Templates: []gateway.MOText{{Text: "Hi {seq}", Weight: 1}},
		Encoding:  "gsm7", NoReceiver: gateway.MOQueue,
	}
}

// waitMOs collects n MO deliver_sm (esm_class without the 0x04 receipt bit).
func waitMOs(t *testing.T, cl *client, n int, within time.Duration) []*gpdu.DeliverSM {
	t.Helper()
	var got []*gpdu.DeliverSM
	deadline := time.After(within)
	for len(got) < n {
		select {
		case d := <-cl.dlrs:
			if d.EsmClass&0x04 == 0 {
				got = append(got, d)
			}
		case <-deadline:
			t.Fatalf("got %d MOs, want %d", len(got), n)
		}
	}
	return got
}

func waitMOState(t *testing.T, m *Manager, run int64, state string) MOSnapshot {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if s := m.StatsSnapshot()[1].MO; s.RunID == run && s.State == state {
			return s
		} else if time.Now().After(deadline) {
			t.Fatalf("MO run %d state %q, want run %d %q", s.RunID, s.State, run, state)
		}
	}
}

func TestMOGenerator(t *testing.T) {
	c := testConfig(t)
	c.DLR.Enabled = false
	m := startManager(t, c)
	cl := mustBind(t, c, "trx")

	// A count-limited run at 200/s.
	c.MO = moConfig(1)
	c.MO.StopAfterCount = 100
	start := time.Now()
	m.Apply([]gateway.Config{c})
	mos := waitMOs(t, cl, 100, 3*time.Second)
	if el := time.Since(start); el < 450*time.Millisecond || el > 800*time.Millisecond {
		t.Errorf("100 MOs at 200/s took %v", el)
	}
	if mos[0].SourceAddr.Address() != "919800000001" || mos[0].DestAddr.Address() != "56161" {
		t.Fatalf("MO %s -> %s", mos[0].SourceAddr.Address(), mos[0].DestAddr.Address())
	}
	if text, _ := mos[0].Message.GetMessage(); text != "Hi 1" {
		t.Fatalf("first MO text %q", text)
	}
	if s := waitMOState(t, m, 1, "finished"); s.RunSent != 100 || s.Ack < 100 {
		t.Fatalf("snapshot %+v", s)
	}
	noDLR(t, cl, 100*time.Millisecond) // finished runs stay finished

	// Rate accuracy (brief: within ±2%): 500/s for 2 s.
	c.MO = moConfig(2)
	c.MO.Rate, c.MO.StopAfterS = 500, 2
	m.Apply([]gateway.Config{c})
	waitMOState(t, m, 2, "finished")
	if n := m.StatsSnapshot()[1].MO.RunSent; n < 980 || n > 1020 {
		t.Errorf("500/s for 2 s sent %d", n)
	}
	waitMOs(t, cl, int(m.StatsSnapshot()[1].MO.RunSent), 3*time.Second)

	// Burst of long UCS2 messages: every part has a UDH; parts count toward the burst.
	c.MO = moConfig(3)
	c.MO.Mode, c.MO.Burst, c.MO.Encoding, c.MO.LongPct = gateway.MOBurst, 12, "ucs2", 100
	c.MO.Templates = []gateway.MOText{{Text: "नमस्ते {seq}", Weight: 1}}
	m.Apply([]gateway.Config{c})
	burst := waitMOState(t, m, 3, "finished")
	if burst.RunSent < 12 || burst.RunSent > 14 { // the last message may add parts past 12
		t.Fatalf("burst of 12 sent %d parts", burst.RunSent)
	}
	for _, d := range waitMOs(t, cl, int(burst.RunSent), 3*time.Second) {
		if d.Message.Encoding().DataCoding() != 0x08 || d.EsmClass != 0x40 || len(d.Message.UDH()) == 0 {
			t.Fatalf("part: data_coding %#x esm_class %#x udh %v", d.Message.Encoding().DataCoding(), d.EsmClass, d.Message.UDH())
		}
	}

	// Test MO: needs a receiver.
	id, err := m.SendTestMO(1, "919811111111", "56161", "TEST hello", false)
	if err != nil || id == 0 {
		t.Fatalf("test MO: %v", err)
	}
	if d := waitMOs(t, cl, 1, time.Second)[0]; d.SourceAddr.Address() != "919811111111" {
		t.Fatalf("test MO from %s", d.SourceAddr.Address())
	}
	_ = cl.sess.Close()
	for deadline := time.Now().Add(3 * time.Second); len(m.Sessions()) > 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("session still bound")
		}
	}
	if _, err := m.SendTestMO(1, "1", "2", "x", false); !errors.Is(err, ErrNoReceiver) {
		t.Fatalf("test MO without receiver: %v", err)
	}

	// No receiver: "drop" drops, "queue" queues until a receiver binds.
	c.MO = moConfig(4)
	c.MO.Mode, c.MO.Burst, c.MO.NoReceiver = gateway.MOBurst, 5, gateway.MODrop
	m.Apply([]gateway.Config{c})
	waitMOState(t, m, 4, "finished")
	if s := m.StatsSnapshot()[1].MO; s.Dropped != 5 || m.queue.TotalLen() != 0 {
		t.Fatalf("drop: %+v, queued %d", s, m.queue.TotalLen())
	}
	c.MO = moConfig(5)
	c.MO.Mode, c.MO.Burst = gateway.MOBurst, 5
	m.Apply([]gateway.Config{c})
	waitMOState(t, m, 5, "finished")
	if m.queue.TotalLen() != 5 {
		t.Fatalf("queued %d, want 5", m.queue.TotalLen())
	}
	rx := mustBind(t, c, "rx")
	waitMOs(t, rx, 5, 2*time.Second)

	// Disabling stops the generator. (A new struct: the worker holds the old one.)
	off := *c.MO
	off.Enabled = false
	c.MO = &off
	m.Apply([]gateway.Config{c})
	waitMOState(t, m, 5, "stopped")
}

func TestMORunResumesAfterRestart(t *testing.T) {
	rdb := testRedis(t)
	c := testConfig(t)
	c.MO = moConfig(7)
	c.MO.Rate, c.MO.StopAfterCount, c.MO.NoReceiver = 100, 60, gateway.MODrop

	m1 := NewManager(Options{ListenHost: "127.0.0.1", Redis: rdb})
	m1.Apply([]gateway.Config{c})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m1.Run(ctx); close(done) }()
	for deadline := time.Now().Add(3 * time.Second); m1.StatsSnapshot()[1].MO.RunSent < 20; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("run did not start")
		}
	}
	stop() // worker restart: progress is saved when the gateway stops
	<-done
	saved, err := rdb.HGet(context.Background(), moProgressKey(1), "sent").Int64()
	if err != nil || saved < 20 || saved >= 60 {
		t.Fatalf("saved progress %d (%v)", saved, err)
	}

	m2 := startManagerWith(t, Options{Redis: rdb}, c)
	s := waitMOState(t, m2, 7, "finished")
	if s.RunSent != 60 || int64(s.Dropped) != 60-saved {
		t.Fatalf("after restart: run sent %d, this worker sent %d; want 60 and %d", s.RunSent, s.Dropped, 60-saved)
	}
}

func TestLicenceLimits(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	lic := licensing.New(licensing.Options{PublicKey: pub}) // no licence: free tier
	lic.SetShare(20)

	// The backstop runs at most FreeGateways active gateways, lowest IDs first.
	var cfgs []gateway.Config
	for i := range licence.FreeGateways + 1 {
		c := testConfig(t)
		c.ID, c.Name = int64(i+1), fmt.Sprintf("gw%d", i+1)
		cfgs = append(cfgs, c)
	}
	m := startManagerWith(t, Options{Licence: lic}, cfgs...)
	st := m.Status()
	if st[1].State != "listening" || st[int64(licence.FreeGateways)].State != "listening" || st[int64(licence.FreeGateways+1)].State != "stopped" {
		t.Fatalf("status %+v", st)
	}

	// Over the worker's share of the licence TPS cap: ESME_RTHROTTLED, counted apart.
	cl := mustBind(t, cfgs[0], "trx")
	for range 60 {
		cl.submit(t, newSubmit(t, "x", 0))
	}
	var throttled int
	for range 60 {
		if waitResp(t, cl).CommandStatus == data.ESME_RTHROTTLED {
			throttled++
		}
	}
	s := m.StatsSnapshot()[1]
	if throttled < 35 || s.LicenceThrottled != uint64(throttled) || s.Throttled != 0 || s.Accepted != uint64(60-throttled) {
		t.Fatalf("throttled %d, stats %+v", throttled, s)
	}
}

func TestSimulatorTrafficIsTagged(t *testing.T) {
	db := "psg_server_simulator_test"
	chURL := chtest.DB(t, db)
	rec, err := events.New(events.Options{WorkerID: 1, URL: chURL, Database: db, SpillDir: t.TempDir(), SpillMaxBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	recCtx, stopRec := context.WithCancel(context.Background())
	recDone := make(chan struct{})
	go func() { rec.Run(recCtx); close(recDone) }()

	c := testConfig(t)
	c.DLR.Timing = gateway.TimingImmediate
	startManagerWith(t, Options{Events: rec}, c)

	// The simulator's bind carries the address_range marker.
	conn, r := rawConn(t, c)
	conn.Write(pdu.Encode(pdu.BindTransceiver, 0, 1, pdu.Bind{
		SystemID: c.SystemID, Password: string(c.Password), InterfaceVersion: 0x34, AddressRange: simulator.AddressRange,
	}.Append(nil)))
	if p, _ := r.Read(); p.Status != pdu.ESME_ROK {
		t.Fatalf("bind: %v", p.Status)
	}
	conn.Write(pdu.Encode(pdu.SubmitSM, 0, 2, pdu.SM{SourceAddr: "SIM", DestAddr: "919800000001", RegisteredDelivery: 1, ShortMessage: []byte("hi")}.Append(nil)))
	for got := 0; got < 2; got++ { // submit_sm_resp and the DLR, in either order
		p, err := r.Read()
		if err != nil {
			t.Fatal(err)
		}
		if p.CommandID == pdu.DeliverSM {
			conn.Write(pdu.Encode(pdu.DeliverSMResp, 0, p.Sequence, []byte{0}))
		}
	}
	// An ordinary client on the same gateway is not tagged.
	cl := mustBind(t, c, "trx")
	cl.submit(t, newSubmit(t, "x", 0))
	waitResp(t, cl)
	time.Sleep(200 * time.Millisecond) // let the DLR acknowledgement be recorded
	_ = cl.sess.Close()
	conn.Close()
	time.Sleep(200 * time.Millisecond)
	stopRec()
	<-recDone

	for q, want := range map[string]string{
		"SELECT groupArray(simulator) FROM (SELECT simulator FROM submit_events ORDER BY simulator)":      "[0,1]",
		"SELECT simulator, result FROM dlr_events":                                                        "1\tacknowledged",
		"SELECT sum(messages) FROM submit_minute WHERE simulator = 1":                                     "1",
		"SELECT countIf(simulator = 1), countIf(simulator = 0) FROM session_events WHERE event = 'bound'": "1\t1",
	} {
		if got := chtest.Query(t, chURL, db, q); got != want {
			t.Errorf("%s\n got %q\nwant %q", q, got, want)
		}
	}
}
