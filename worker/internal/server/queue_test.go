package server

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Needs a real Redis: PSG_TEST_REDIS_ADDR=127.0.0.1:6379 (CI provides one).
func testRedis(t *testing.T) *redis.Client {
	addr := os.Getenv("PSG_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("PSG_TEST_REDIS_ADDR not set")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis: %v", err)
	}
	rdb.FlushDB(context.Background())
	t.Cleanup(func() { rdb.FlushDB(context.Background()); rdb.Close() })
	return rdb
}

func TestQueuePersistsAndRestores(t *testing.T) {
	rdb := testRedis(t)
	ctx := context.Background()
	q := newDLRQueue(rdb, slog.Default())
	for i := range 2500 {
		q.push(&dlrJob{GatewayID: 9, MsgID: uint64(i), Dst: "123", QueuedAt: time.Now()})
	}
	q.pop(9, 500) // popped before ever being persisted: must not reach Redis
	q.persist(ctx)
	if n := rdb.HLen(ctx, queueKey(9)).Val(); n != 2000 {
		t.Fatalf("hash length %d, want 2000", n)
	}
	q.pop(9, 1000)
	q.persist(ctx) // deletes the popped entries
	if n := rdb.HLen(ctx, queueKey(9)).Val(); n != 1000 {
		t.Fatalf("hash length after pop %d, want 1000", n)
	}

	// A fresh worker restores the remaining 1000, in order.
	q2 := newDLRQueue(rdb, slog.Default())
	q2.restore(9)
	if q2.TotalLen() != 1000 {
		t.Fatalf("restored %d, want 1000", q2.TotalLen())
	}
	jobs := q2.pop(9, 1000)
	if jobs[0].MsgID != 1500 || jobs[999].MsgID != 2499 || jobs[0].Dst != "123" {
		t.Fatalf("restored jobs %d..%d", jobs[0].MsgID, jobs[999].MsgID)
	}
	q2.persist(ctx)
	if n := rdb.HLen(ctx, queueKey(9)).Val(); n != 0 {
		t.Fatalf("hash not emptied: %d", n)
	}
}

// A round trip can time out after Redis applied it. Resending it (here, the
// whole queue twice over) must not duplicate jobs, and a job popped and queued
// again before the next persist must stay stored.
func TestQueueResendIsIdempotent(t *testing.T) {
	rdb := testRedis(t)
	ctx := context.Background()
	q := newDLRQueue(rdb, slog.Default())
	base := time.Now()
	for i := range 25_000 { // more than one round trip
		q.push(&dlrJob{GatewayID: 4, MsgID: uint64(i), QueuedAt: base.Add(time.Duration(i))})
	}
	q.persist(ctx)
	g := q.gws[4]
	g.unsaved = append(g.unsaved, g.items...) // as if every reply had been lost
	g.unsaved = append(g.unsaved, g.items...)
	q.persist(ctx)
	if n := rdb.HLen(ctx, queueKey(4)).Val(); n != 25_000 {
		t.Fatalf("hash length %d, want 25000", n)
	}

	j := q.pop(4, 1)[0]
	q.push(j) // e.g. its receiver unbound before sending it
	q.persist(ctx)
	if !rdb.HExists(ctx, queueKey(4), queueField(j)).Val() {
		t.Fatal("re-queued job was deleted")
	}

	q2 := newDLRQueue(rdb, slog.Default())
	q2.restore(4)
	jobs := q2.pop(4, 25_000)
	if len(jobs) != 25_000 || jobs[0].MsgID != 0 || jobs[24_999].MsgID != 24_999 {
		t.Fatalf("restored %d jobs, first %d last %d", len(jobs), jobs[0].MsgID, jobs[len(jobs)-1].MsgID)
	}
}

func TestQueuePersistFailurePutsWorkBack(t *testing.T) {
	rdb := testRedis(t)
	q := newDLRQueue(rdb, slog.Default())
	for i := range 10 {
		q.push(&dlrJob{GatewayID: 2, MsgID: uint64(i)})
	}
	q.persist(context.Background())
	q.pop(2, 4) // 4 deletes pending
	q.push(&dlrJob{GatewayID: 2, MsgID: 100})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	q.persist(cancelled)
	if g := q.gws[2]; len(g.toDelete) != 4 || len(g.unsaved) != 1 {
		t.Fatalf("put back %d deletes, %d adds; want 4, 1", len(g.toDelete), len(g.unsaved))
	}
	q.persist(context.Background())
	if n := rdb.HLen(context.Background(), queueKey(2)).Val(); n != 7 {
		t.Fatalf("hash length %d, want 7", n)
	}
}

func TestQueueKeepsIntermediateAndFinalApart(t *testing.T) {
	rdb := testRedis(t)
	ctx := context.Background()
	q := newDLRQueue(rdb, slog.Default())
	q.push(&dlrJob{GatewayID: 3, MsgID: 7, Interim: true})
	q.push(&dlrJob{GatewayID: 3, MsgID: 7})
	q.persist(ctx)
	if n := rdb.HLen(ctx, queueKey(3)).Val(); n != 2 {
		t.Fatalf("hash length %d, want 2", n)
	}
	q2 := newDLRQueue(rdb, slog.Default())
	q2.restore(3)
	if jobs := q2.pop(3, 2); len(jobs) != 2 || jobs[0].Interim == jobs[1].Interim {
		t.Fatalf("restored %+v", jobs)
	}
}

func TestQueueKeepsJobsWhileRedisDown(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	defer rdb.Close()
	q := newDLRQueue(rdb, slog.Default())
	for i := range 10 {
		q.push(&dlrJob{GatewayID: 1, MsgID: uint64(i)})
	}
	q.persist(context.Background())
	if q.TotalLen() != 10 || len(q.gws[1].unsaved) != 10 {
		t.Fatalf("len %d unsaved %d", q.TotalLen(), len(q.gws[1].unsaved))
	}
	if got := q.pop(1, 10); len(got) != 10 {
		t.Fatalf("popped %d", len(got))
	}
}
