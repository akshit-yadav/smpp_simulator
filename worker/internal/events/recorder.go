package events

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	batchRows     = 10_000
	maxBuffered   = 500_000 // rows per table waiting for the writer (~100 MB at worst)
	insertTimeout = 5 * time.Second
	replayBudget  = 800 * time.Millisecond // per tick, so buffers keep draining
	dropLogEvery  = 10 * time.Second
)

type Options struct {
	WorkerID      uint8
	URL           string // ClickHouse HTTP interface, e.g. http://127.0.0.1:8123
	Database      string
	User          string
	Password      string
	SpillDir      string
	SpillMaxBytes int64
	Registerer    prometheus.Registerer
	Log           *slog.Logger
}

// Recorder buffers events and writes them to ClickHouse. A nil *Recorder
// records nothing, so callers need no checks when events are off.
type Recorder struct {
	opt   Options
	log   *slog.Logger
	http  *http.Client
	spill *spill

	submits  buffer[Submit]
	dlrs     buffer[DLR]
	mos      buffer[MO]
	sessions buffer[Session]
	notify   chan struct{} // a buffer reached batchRows

	stats     map[string]*tableStats
	up        prometheus.Gauge
	isUp      bool
	lastSeq   int64
	dropped   atomic.Uint64
	dropSeen  uint64
	dropLogAt time.Time
}

type tableStats struct {
	columns                   string
	written, spilled, dropped prometheus.Counter
}

// New opens the spill directory (replaying whatever an earlier run left
// there once ClickHouse is reachable) and registers the metrics.
func New(opt Options) (*Recorder, error) {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	sp, err := openSpill(opt.SpillDir, opt.SpillMaxBytes)
	if err != nil {
		return nil, err
	}
	r := &Recorder{
		opt:    opt,
		log:    opt.Log,
		http:   &http.Client{},
		spill:  sp,
		notify: make(chan struct{}, 1),
		stats:  map[string]*tableStats{},
		isUp:   true,
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "psg_events_clickhouse_up", Help: "1 if the last ClickHouse insert succeeded.",
		}),
	}
	r.up.Set(1)
	counts := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "psg_events_total",
		Help: "Event rows by table and result (written, spilled to disk, dropped).",
	}, []string{"table", "result"})
	for name, cols := range map[string]string{
		submitTable.name: submitTable.columns, dlrTable.name: dlrTable.columns,
		moTable.name: moTable.columns, sessionTable.name: sessionTable.columns,
	} {
		r.stats[name] = &tableStats{
			columns: cols,
			written: counts.WithLabelValues(name, "written"),
			spilled: counts.WithLabelValues(name, "spilled"),
			dropped: counts.WithLabelValues(name, "dropped"),
		}
	}
	if opt.Registerer != nil {
		opt.Registerer.MustRegister(counts, r.up, prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "psg_events_spill_bytes", Help: "Event batches waiting on disk for ClickHouse.",
		}, func() float64 { return float64(sp.bytes.Load()) }))
	}
	if n := len(sp.files); n > 0 {
		r.log.Info("event spill found; replaying when ClickHouse is reachable", "batches", n, "bytes", sp.bytes.Load())
	}
	return r, nil
}

func (r *Recorder) Submit(e Submit) {
	if r != nil {
		record(r, &r.submits, &e, r.stats[submitTable.name])
	}
}

func (r *Recorder) DLR(e DLR) {
	if r != nil {
		record(r, &r.dlrs, &e, r.stats[dlrTable.name])
	}
}

func (r *Recorder) MO(e MO) {
	if r != nil {
		record(r, &r.mos, &e, r.stats[moTable.name])
	}
}

func (r *Recorder) Session(e Session) {
	if r != nil {
		record(r, &r.sessions, &e, r.stats[sessionTable.name])
	}
}

func record[T any](r *Recorder, b *buffer[T], e *T, st *tableStats) {
	switch n := b.add(e, maxBuffered); n {
	case -1:
		st.dropped.Inc()
		r.dropped.Add(1)
	case batchRows:
		select {
		case r.notify <- struct{}{}:
		default:
		}
	}
}

// Run writes buffered events every second, or sooner when a table reaches
// batchRows, until ctx is done; then it flushes once more. Writes are not
// cut short by ctx (each insert has its own timeout), so stopping the worker
// does not push a healthy ClickHouse's batches to the spill.
func (r *Recorder) Run(ctx context.Context) {
	wctx := context.WithoutCancel(ctx)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
		case <-t.C:
		case <-r.notify:
		}
		r.flush(wctx)
		if ctx.Err() != nil {
			return
		}
		r.replay(wctx)
		r.logDrops()
	}
}

func (r *Recorder) flush(ctx context.Context) {
	flushTable(ctx, r, &r.submits, &submitTable)
	flushTable(ctx, r, &r.dlrs, &dlrTable)
	flushTable(ctx, r, &r.mos, &moTable)
	flushTable(ctx, r, &r.sessions, &sessionTable)
}

func flushTable[T any](ctx context.Context, r *Recorder, b *buffer[T], t *table[T]) {
	rows := b.take()
	for chunk := range slices.Chunk(rows, batchRows) {
		// A fresh buffer each time: the HTTP transport may still be reading
		// the last one after an early error reply.
		data := make([]byte, 0, len(chunk)*128)
		for i := range chunk {
			data = t.encode(data, r.opt.WorkerID, &chunk[i])
		}
		r.ship(ctx, t.name, len(chunk), data)
	}
	if rows != nil {
		b.giveBack(rows)
	}
}

// ship inserts a batch, or spills it if ClickHouse fails or older batches
// are still on disk (so rows reach ClickHouse in order).
func (r *Recorder) ship(ctx context.Context, table string, rows int, data []byte) {
	st := r.stats[table]
	seq := r.nextSeq()
	if len(r.spill.files) == 0 {
		err := r.insert(ctx, table, r.token(seq), data)
		if err == nil {
			r.setUp(nil)
			st.written.Add(float64(rows))
			return
		}
		r.setUp(err)
	}
	if err := r.spill.write(spillFile{seq: seq, table: table, rows: rows}, data); err != nil {
		st.dropped.Add(float64(rows))
		r.dropped.Add(uint64(rows))
		return
	}
	st.spilled.Add(float64(rows))
}

// replay inserts spilled batches, oldest first, for up to replayBudget.
func (r *Recorder) replay(ctx context.Context) {
	deadline := time.Now().Add(replayBudget)
	for len(r.spill.files) > 0 && time.Now().Before(deadline) {
		f := r.spill.files[0]
		st := r.stats[f.table]
		data, err := r.spill.read(f)
		if err != nil || st == nil {
			r.log.Error("unreadable event batch; moved aside", "file", f.name(), "err", err)
			r.spill.quarantine(f)
			continue
		}
		err = r.insert(ctx, f.table, r.token(f.seq), data)
		var ie *insertError
		switch {
		case err == nil:
			r.setUp(nil)
			st.written.Add(float64(f.rows))
			r.spill.remove(f)
		case errors.As(err, &ie) && ie.permanent():
			r.log.Error("event batch rejected by ClickHouse; moved aside", "file", f.name(), "err", err)
			r.spill.quarantine(f)
		default:
			r.setUp(err)
			return
		}
	}
}

func (r *Recorder) nextSeq() int64 {
	r.lastSeq = max(time.Now().UnixNano(), r.lastSeq+1)
	return r.lastSeq
}

// token identifies a batch for ClickHouse insert deduplication, so a batch
// resent after a lost reply is not written twice.
func (r *Recorder) token(seq int64) string { return fmt.Sprintf("psg-w%d-%d", r.opt.WorkerID, seq) }

func (r *Recorder) setUp(err error) {
	switch {
	case err == nil && !r.isUp:
		r.log.Info("clickhouse reachable again; replaying spilled events", "batches", len(r.spill.files))
	case err != nil && r.isUp:
		r.log.Warn("clickhouse insert failed; spilling events to disk", "err", err, "dir", r.opt.SpillDir)
	}
	r.isUp = err == nil
	if r.isUp {
		r.up.Set(1)
	} else {
		r.up.Set(0)
	}
}

func (r *Recorder) logDrops() {
	n := r.dropped.Load()
	if n == r.dropSeen || time.Since(r.dropLogAt) < dropLogEvery {
		return
	}
	r.log.Warn("events dropped (buffer or spill full)", "rows", n-r.dropSeen, "total", n)
	r.dropSeen, r.dropLogAt = n, time.Now()
}

type insertError struct {
	status int
	msg    string
}

func (e *insertError) Error() string { return fmt.Sprintf("clickhouse: HTTP %d: %s", e.status, e.msg) }

// permanent reports a batch ClickHouse will never accept (bad data or query).
// Unknown tables (404) are not permanent: the control plane may not have
// created them yet.
func (e *insertError) permanent() bool { return e.status == http.StatusBadRequest }

func (r *Recorder) insert(ctx context.Context, table, token string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, insertTimeout)
	defer cancel()
	q := url.Values{
		"query":                      {"INSERT INTO " + table + " (" + r.stats[table].columns + ") FORMAT RowBinary"},
		"database":                   {r.opt.Database},
		"insert_deduplication_token": {token},
		"deduplicate_blocks_in_dependent_materialized_views": {"1"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.opt.URL+"/?"+q.Encode(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	if r.opt.User != "" {
		req.Header.Set("X-ClickHouse-User", r.opt.User)
	}
	if r.opt.Password != "" {
		req.Header.Set("X-ClickHouse-Key", r.opt.Password)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		if ue, ok := err.(*url.Error); ok {
			return fmt.Errorf("clickhouse: %w", ue.Err) // the URL is long and holds no news
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &insertError{status: resp.StatusCode, msg: strings.TrimSpace(string(msg))}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
