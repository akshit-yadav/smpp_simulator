package server

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/msgid"
)

const (
	maxQueuedPerGateway = 2_000_000
	persistBatch        = 500             // jobs or IDs per HSET/HDEL
	persistPipeline     = 20              // commands per round trip, so at most 10,000 jobs
	persistTimeout      = 5 * time.Second // per round trip
)

func queueKey(gatewayID int64) string { return "psg:dlrh:" + strconv.FormatInt(gatewayID, 10) }

func queueField(j *dlrJob) string { return msgid.Hex(j.MsgID) }

// dlrQueue holds DLRs for gateways with no receiver bound. Jobs live in
// memory; a background persist (once a second, never on the hot path) mirrors
// them to one Redis hash per gateway, keyed by message ID, so they survive a
// worker restart. Every write is an HSET or HDEL by message ID, so a round
// trip that timed out after Redis applied it can be resent without creating
// duplicates.
type dlrQueue struct {
	rdb *redis.Client
	log *slog.Logger

	mu       sync.Mutex
	gws      map[int64]*gwQueue
	total    atomic.Int64
	redisErr bool
}

type gwQueue struct {
	items    []*qItem
	unsaved  []*qItem // pushed since the last persist
	toDelete []string // fields of popped items that may be in Redis
	restored atomic.Bool
}

type qItem struct {
	job    *dlrJob
	stored bool // an HSET was sent, so it may be in Redis
	popped bool
}

func newDLRQueue(rdb *redis.Client, log *slog.Logger) *dlrQueue {
	return &dlrQueue{rdb: rdb, log: log, gws: map[int64]*gwQueue{}}
}

func (q *dlrQueue) gw(id int64) *gwQueue {
	g := q.gws[id]
	if g == nil {
		g = &gwQueue{}
		q.gws[id] = g
	}
	return g
}

func (q *dlrQueue) TotalLen() int { return int(q.total.Load()) }

func (q *dlrQueue) push(j *dlrJob) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pushLocked(q.gw(j.GatewayID), &qItem{job: j}, q.rdb != nil)
}

func (q *dlrQueue) pushLocked(g *gwQueue, it *qItem, persist bool) {
	if len(g.items) >= maxQueuedPerGateway {
		old := g.items[0]
		g.items = g.items[1:]
		q.markPopped(g, old)
		q.total.Add(-1)
	}
	g.items = append(g.items, it)
	if persist {
		g.unsaved = append(g.unsaved, it)
	}
	q.total.Add(1)
}

func (q *dlrQueue) markPopped(g *gwQueue, it *qItem) {
	it.popped = true
	if it.stored {
		g.toDelete = append(g.toDelete, queueField(it.job))
	}
}

// pop removes up to n jobs, oldest first.
func (q *dlrQueue) pop(id int64, n int) []*dlrJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	g := q.gws[id]
	if g == nil || len(g.items) == 0 {
		return nil
	}
	n = min(n, len(g.items))
	out := make([]*dlrJob, n)
	for i, it := range g.items[:n] {
		out[i] = it.job
		q.markPopped(g, it)
	}
	g.items = g.items[n:]
	if len(g.items) == 0 {
		g.items = nil
	}
	q.total.Add(-int64(n))
	return out
}

// forget drops a removed gateway's in-memory queue. Its Redis hash is left
// alone: if the gateway moved to another worker, that worker restores it; if
// it was deleted, the control plane deletes the hash.
func (q *dlrQueue) forget(id int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if g := q.gws[id]; g != nil {
		q.total.Add(-int64(len(g.items)))
		delete(q.gws, id)
	}
}

func (q *dlrQueue) gatewaysWithItems() []int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	var ids []int64
	for id, g := range q.gws {
		if len(g.items) > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

// persistOp is one HDEL (del) or HSET (items, with jobs copied under the lock
// so encoding them races with nothing).
type persistOp struct {
	gw    int64
	del   []string
	items []*qItem
	jobs  []dlrJob
}

// persist mirrors pushes and pops since the last call to Redis, in round
// trips of persistPipeline commands. Within a gateway, deletes go before adds,
// so a job popped and queued again in the same second ends up stored. On the
// first failed round trip, it and everything after it is put back for the
// next call.
func (q *dlrQueue) persist(ctx context.Context) {
	if q.rdb == nil {
		return
	}
	q.mu.Lock()
	var ops []persistOp
	for id, g := range q.gws {
		for b := range slices.Chunk(g.toDelete, persistBatch) {
			ops = append(ops, persistOp{gw: id, del: b})
		}
		op := persistOp{gw: id}
		for _, it := range g.unsaved {
			if it.popped {
				continue
			}
			it.stored = true
			op.items = append(op.items, it)
			op.jobs = append(op.jobs, *it.job)
			if len(op.items) == persistBatch {
				ops = append(ops, op)
				op = persistOp{gw: id}
			}
		}
		if len(op.items) > 0 {
			ops = append(ops, op)
		}
		g.unsaved, g.toDelete = nil, nil
	}
	q.mu.Unlock()

	var err error
	for i := 0; i < len(ops); i += persistPipeline {
		if err = q.write(ctx, ops[i:min(i+persistPipeline, len(ops))]); err != nil {
			q.putBack(ops[i:])
			break
		}
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if err != nil && !q.redisErr {
		q.log.Warn("dlr queue persist failed; keeping queued DLRs in memory", "err", err)
		q.redisErr = true
	} else if err == nil && q.redisErr {
		q.log.Info("dlr queue persist restored")
		q.redisErr = false
	}
}

func (q *dlrQueue) write(ctx context.Context, ops []persistOp) error {
	ctx, cancel := context.WithTimeout(ctx, persistTimeout)
	defer cancel()
	pipe := q.rdb.Pipeline()
	for _, op := range ops {
		key := queueKey(op.gw)
		if op.del != nil {
			pipe.HDel(ctx, key, op.del...)
			continue
		}
		vals := make([]any, 0, 2*len(op.jobs))
		for i := range op.jobs {
			data, _ := json.Marshal(&op.jobs[i])
			vals = append(vals, queueField(&op.jobs[i]), data)
		}
		pipe.HSet(ctx, key, vals...)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// putBack re-files ops that may not have reached Redis. Items popped since
// were added to toDelete by markPopped (they are marked stored), so they are
// skipped by the next persist and deleted instead.
func (q *dlrQueue) putBack(ops []persistOp) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, op := range ops {
		g := q.gw(op.gw)
		g.toDelete = append(g.toDelete, op.del...)
		g.unsaved = append(g.unsaved, op.items...)
	}
}

// restore loads a gateway's persisted queue from Redis, once per gateway,
// oldest first.
func (q *dlrQueue) restore(id int64) {
	if q.rdb == nil {
		return
	}
	q.mu.Lock()
	g := q.gw(id)
	q.mu.Unlock()
	if g.restored.Swap(true) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	seen := map[string]bool{} // HSCAN may return a field twice
	var items []*qItem
	var bad []string
	it := q.rdb.HScan(ctx, queueKey(id), 0, "", 10_000).Iterator()
	for it.Next(ctx) {
		field := it.Val()
		if !it.Next(ctx) {
			break
		}
		if seen[field] {
			continue
		}
		seen[field] = true
		var j dlrJob
		if json.Unmarshal([]byte(it.Val()), &j) != nil {
			bad = append(bad, field)
			continue
		}
		items = append(items, &qItem{job: &j, stored: true})
	}
	if err := it.Err(); err != nil {
		q.log.Warn("dlr queue restore failed", "gateway_id", id, "err", err)
		g.restored.Store(false)
		return
	}
	slices.SortFunc(items, func(a, b *qItem) int {
		return cmp.Or(a.job.QueuedAt.Compare(b.job.QueuedAt), cmp.Compare(a.job.MsgID, b.job.MsgID))
	})

	q.mu.Lock()
	for _, it := range items {
		q.pushLocked(g, it, false)
	}
	g.toDelete = append(g.toDelete, bad...)
	q.mu.Unlock()
	if len(items) > 0 {
		q.log.Info("dlr queue restored", "gateway_id", id, "dlrs", len(items))
	}
}
