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
	if n := rdb.XLen(ctx, streamKey(9)).Val(); n != 2000 {
		t.Fatalf("stream length %d, want 2000", n)
	}
	q.pop(9, 1000)
	q.persist(ctx) // deletes the popped entries
	if n := rdb.XLen(ctx, streamKey(9)).Val(); n != 1000 {
		t.Fatalf("stream length after pop %d, want 1000", n)
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
	if n := rdb.XLen(ctx, streamKey(9)).Val(); n != 0 {
		t.Fatalf("stream not emptied: %d", n)
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
