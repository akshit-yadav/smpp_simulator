package events

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/chtest"
)

// fakeCH is a ClickHouse HTTP endpoint that fails while down is set.
type fakeCH struct {
	mu      sync.Mutex
	down    bool
	status  int // reply while down; 0 = drop the connection
	inserts []fakeInsert
}

type fakeInsert struct {
	table, token, query string
	size                int
}

func (f *fakeCH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		if f.status == 0 {
			panic(http.ErrAbortHandler)
		}
		http.Error(w, "Code: 999. simulated", f.status)
		return
	}
	q := r.URL.Query()
	table := strings.Fields(q.Get("query"))[2]
	f.inserts = append(f.inserts, fakeInsert{table: table, token: q.Get("insert_deduplication_token"), query: q.Get("query"), size: len(body)})
}

func (f *fakeCH) set(down bool, status int) {
	f.mu.Lock()
	f.down, f.status = down, status
	f.mu.Unlock()
}

func (f *fakeCH) got() []fakeInsert {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.inserts)
}

func newTestRecorder(t *testing.T, url, dir string, spillMax int64) *Recorder {
	t.Helper()
	r, err := New(Options{WorkerID: 7, URL: url, Database: "psg", SpillDir: dir, SpillMaxBytes: spillMax, Log: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSpillAndReplayInOrder(t *testing.T) {
	ch := &fakeCH{}
	srv := httptest.NewServer(ch)
	defer srv.Close()
	dir := t.TempDir()
	r := newTestRecorder(t, srv.URL, dir, 1<<30)
	ctx := context.Background()

	r.Submit(Submit{TS: time.Now(), GatewayID: 1, Result: SubmitAccepted})
	r.flush(ctx)
	if got := ch.got(); len(got) != 1 || got[0].table != "submit_events" || !strings.HasPrefix(got[0].token, "psg-w7-") {
		t.Fatalf("inserts %+v", got)
	}

	ch.set(true, http.StatusServiceUnavailable)
	for i := range 3 {
		r.DLR(DLR{TS: time.Now(), GatewayID: 1, MessageID: uint64(i), Result: DLRAcknowledged})
		r.flush(ctx)
		r.replay(ctx) // fails; files stay
	}
	r.Session(Session{TS: time.Now(), GatewayID: 1, Event: SessionBound})
	r.flush(ctx) // spilled behind the DLR batches without trying ClickHouse
	if len(r.spill.files) != 4 || r.isUp {
		t.Fatalf("spill has %d files, up=%v", len(r.spill.files), r.isUp)
	}
	var tokens []string
	for _, f := range r.spill.files {
		tokens = append(tokens, r.token(f.seq))
	}

	// A new recorder (worker restart) finds the spill and replays it in order.
	r2 := newTestRecorder(t, srv.URL, dir, 1<<30)
	ch.set(false, 0)
	r2.replay(ctx)
	got := ch.got()[1:]
	if len(got) != 4 || got[3].table != "session_events" {
		t.Fatalf("replayed %+v", got)
	}
	for i, in := range got {
		if in.token != tokens[i] {
			t.Fatalf("replay %d token %s, want %s (same token as the first attempt)", i, in.token, tokens[i])
		}
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 || r2.spill.bytes.Load() != 0 {
		t.Fatalf("spill not emptied: %d files, %d bytes", len(left), r2.spill.bytes.Load())
	}
}

// A batch spilled by a worker from before the simulator column (layout 1, no
// .v part in its name) is replayed with the old column list.
func TestLegacySpillReplaysWithOldColumns(t *testing.T) {
	ch := &fakeCH{}
	srv := httptest.NewServer(ch)
	defer srv.Close()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "00000000000000000001.submit_events.1.rb"), []byte("x"), 0o640)
	r := newTestRecorder(t, srv.URL, dir, 1<<30)
	r.Submit(Submit{TS: time.Now(), Simulator: true})
	r.flush(context.Background()) // queued behind the old file, so spilled as layout 2
	r.replay(context.Background())
	got := ch.got()
	if len(got) != 2 || strings.Contains(got[0].query, "simulator") || !strings.Contains(got[1].query, "sm_length, simulator)") {
		t.Fatalf("inserts %+v", got)
	}
}

func TestConnectionErrorsSpillAndBadBatchesAreSetAside(t *testing.T) {
	ch := &fakeCH{}
	srv := httptest.NewServer(ch)
	defer srv.Close()
	dir := t.TempDir()
	r := newTestRecorder(t, srv.URL, dir, 1<<30)
	ctx := context.Background()

	ch.set(true, 0) // connection dropped
	r.Submit(Submit{TS: time.Now()})
	r.flush(ctx)
	ch.set(true, http.StatusBadRequest) // ClickHouse rejects the data
	r.replay(ctx)
	if len(r.spill.files) != 0 {
		t.Fatalf("bad batch not set aside")
	}
	if bad, _ := os.ReadDir(dir); len(bad) != 1 || !strings.HasSuffix(bad[0].Name(), ".rb.bad") {
		t.Fatalf("dir %v", bad)
	}
}

func TestSpillFullDrops(t *testing.T) {
	ch := &fakeCH{down: true, status: http.StatusServiceUnavailable}
	srv := httptest.NewServer(ch)
	defer srv.Close()
	r := newTestRecorder(t, srv.URL, t.TempDir(), 10) // smaller than one row
	r.Submit(Submit{TS: time.Now(), Src: "12345", Dst: "67890"})
	r.flush(context.Background())
	if r.dropped.Load() != 1 || len(r.spill.files) != 0 {
		t.Fatalf("dropped %d, spilled %d", r.dropped.Load(), len(r.spill.files))
	}
}

func TestClickHouseRoundTrip(t *testing.T) {
	db := "psg_events_test"
	chURL := chtest.DB(t, db)

	r, err := New(Options{WorkerID: 3, URL: chURL, Database: db, SpillDir: t.TempDir(), SpillMaxBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 10, 22, 10, 0, 0, 123e6, time.UTC)
	for i := range 25_000 { // three batches
		r.Submit(Submit{
			TS: ts, GatewayID: 5, SessionID: 9, SystemID: "sys", MessageID: uint64(i + 1), Src: "Héllo", Dst: "919800000000",
			Result: SubmitAccepted, Outcome: "delivered", DLRStatus: "DELIVRD", DLRError: "000", RegisteredDelivery: 1, SMLength: 160,
		})
	}
	r.DLR(DLR{TS: ts, GatewayID: 5, SessionID: 9, MessageID: 1, Dst: "919800000000", Result: DLRAcknowledged,
		DLRStatus: "DELIVRD", DLRError: "000", Attempt: 1, SubmitTS: ts.Add(-1500 * time.Millisecond), DelayMS: 1500, RespMS: 4})
	r.MO(MO{TS: ts, GatewayID: 5, SessionID: 9, MessageID: 77, Src: "919800000001", Dst: "56161", Result: DLRAcknowledged,
		Test: true, UCS2: true, Part: 1, Parts: 2, SMLength: 140, Text: "नमस्ते", Attempt: 1, RespMS: 3})
	r.MO(MO{TS: ts, GatewayID: 5, MessageID: 78, Result: MODropped})
	r.Session(Session{TS: ts, GatewayID: 5, SessionID: 9, Event: SessionClosed, BindType: "trx", SystemID: "sys", Remote: "1.2.3.4:5", Submits: 25_000})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Run(ctx) // final flush only

	want := map[string]string{
		"SELECT count(), uniqExact(message_id), any(worker_id), any(source_addr), toString(min(ts)) FROM submit_events": "25000\t25000\t3\tHéllo\t2026-10-22 10:00:00.123",
		"SELECT sum(messages) FROM submit_minute WHERE result = 'accepted' AND outcome = 'delivered'":                   "25000",
		"SELECT result, delay_ms, resp_ms, attempt, toString(submit_ts) FROM dlr_events":                                "acknowledged\t1500\t4\t1\t2026-10-22 09:59:58.623",
		"SELECT delay_bucket, dlrs FROM dlr_minute":                                                                     "1000\t1",
		"SELECT result, test, encoding, part, parts, text, resp_ms FROM mo_events WHERE message_id = 77":                "acknowledged\t1\tucs2\t1\t2\tनमस्ते\t3",
		"SELECT groupArray(toString(result)) FROM (SELECT result FROM mo_minute ORDER BY result)":                       "['acknowledged','dropped']",
		"SELECT event, remote, submits FROM session_events":                                                             "closed\t1.2.3.4:5\t25000",
	}
	for q, w := range want {
		if got := chtest.Query(t, chURL, db, q); got != w {
			t.Errorf("%s\n got %q\nwant %q", q, got, w)
		}
	}

	// A batch resent with the same token (its first reply was lost) is not
	// written twice, in the table or in the roll-up.
	data := submitTable.encode(nil, 3, &Submit{TS: ts, GatewayID: 6, Result: SubmitThrottled})
	for range 2 {
		if err := r.insert(context.Background(), "submit_events", submitTable.columns, "psg-w3-42", data); err != nil {
			t.Fatal(err)
		}
	}
	if got := chtest.Query(t, chURL, db, "SELECT count() FROM submit_events WHERE gateway_id = 6"); got != "1" {
		t.Errorf("resent batch counted %s times", got)
	}
	if got := chtest.Query(t, chURL, db, "SELECT sum(messages) FROM submit_minute WHERE gateway_id = 6"); got != "1" {
		t.Errorf("resent batch rolled up %s times", got)
	}
}
