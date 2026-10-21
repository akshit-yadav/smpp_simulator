package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const maxQueuedPerGateway = 2_000_000

func streamKey(gatewayID int64) string { return "fsg:dlrq:" + strconv.FormatInt(gatewayID, 10) }

// dlrQueue holds DLRs for gateways with no receiver bound. Jobs live in
// memory; a background persist (once a second, never on the hot path) mirrors
// them to one Redis stream per gateway so they survive a worker restart.
type dlrQueue struct {
	rdb *redis.Client
	log *slog.Logger

	mu       sync.Mutex
	gws      map[int64]*gwQueue
	total    atomic.Int64
	redisErr bool
	persists int
}

type gwQueue struct {
	items    []*qItem
	unsaved  []*qItem // pushed since the last persist
	toDelete []string // stream IDs of popped items
	restored atomic.Bool
}

type qItem struct {
	job      *dlrJob
	streamID string
	popped   bool
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
	if it.streamID != "" {
		g.toDelete = append(g.toDelete, it.streamID)
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

// forget drops a removed gateway's in-memory queue. Its Redis stream is left
// alone: if the gateway moved to another worker, that worker restores it; if
// it was deleted, the control plane deletes the stream.
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

// persist mirrors pushes and pops since the last call to Redis.
func (q *dlrQueue) persist(ctx context.Context) {
	if q.rdb == nil {
		return
	}
	type batch struct {
		add []*qItem
		del []string
	}
	q.mu.Lock()
	work := map[int64]*batch{}
	for id, g := range q.gws {
		var add []*qItem
		for _, it := range g.unsaved {
			if !it.popped {
				add = append(add, it)
			}
		}
		if len(add) > 0 || len(g.toDelete) > 0 {
			work[id] = &batch{add: add, del: g.toDelete}
		}
		g.unsaved, g.toDelete = nil, nil
	}
	q.persists++
	trim := q.persists%60 == 0
	q.mu.Unlock()

	if len(work) == 0 && !trim {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pipe := q.rdb.Pipeline()
	adds := map[*qItem]*redis.StringCmd{}
	for id, b := range work {
		key := streamKey(id)
		for _, it := range b.add {
			data, _ := json.Marshal(it.job)
			adds[it] = pipe.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: []any{"j", data}})
		}
		for i := 0; i < len(b.del); i += 1000 {
			pipe.XDel(ctx, key, b.del[i:min(i+1000, len(b.del))]...)
		}
	}
	if trim {
		minID := strconv.FormatInt(time.Now().Add(-72*time.Hour).UnixMilli(), 10)
		q.mu.Lock()
		for id := range q.gws {
			pipe.XTrimMinID(ctx, streamKey(id), minID)
		}
		q.mu.Unlock()
	}
	_, err := pipe.Exec(ctx)

	q.mu.Lock()
	defer q.mu.Unlock()
	if err != nil {
		// Put the work back; it is retried next second.
		for id, b := range work {
			g := q.gw(id)
			for _, it := range b.add {
				if adds[it].Err() != nil {
					g.unsaved = append(g.unsaved, it)
				}
			}
			g.toDelete = append(g.toDelete, b.del...)
		}
		if !q.redisErr {
			q.log.Warn("dlr queue persist failed; keeping queued DLRs in memory", "err", err)
			q.redisErr = true
		}
	} else if q.redisErr {
		q.log.Info("dlr queue persist restored")
		q.redisErr = false
	}
	for it, cmd := range adds {
		if cmd.Err() != nil {
			continue
		}
		it.streamID = cmd.Val()
		if it.popped {
			q.gw(it.job.GatewayID).toDelete = append(q.gw(it.job.GatewayID).toDelete, it.streamID)
		}
	}
}

// restore loads a gateway's persisted queue from Redis, once per gateway.
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
	key, start, n := streamKey(id), "-", 0
	for {
		msgs, err := q.rdb.XRangeN(ctx, key, start, "+", 10_000).Result()
		if err != nil {
			q.log.Warn("dlr queue restore failed", "gateway_id", id, "err", err)
			g.restored.Store(false)
			return
		}
		if len(msgs) == 0 {
			break
		}
		q.mu.Lock()
		for _, msg := range msgs {
			raw, _ := msg.Values["j"].(string)
			var j dlrJob
			if json.Unmarshal([]byte(raw), &j) != nil {
				g.toDelete = append(g.toDelete, msg.ID)
				continue
			}
			q.pushLocked(g, &qItem{job: &j, streamID: msg.ID}, false)
			n++
		}
		q.mu.Unlock()
		start = "(" + msgs[len(msgs)-1].ID
	}
	if n > 0 {
		q.log.Info("dlr queue restored", "gateway_id", id, "dlrs", n)
	}
}
