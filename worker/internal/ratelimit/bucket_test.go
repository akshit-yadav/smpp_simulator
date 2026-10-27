package ratelimit

import (
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestBucket(tps float64) (*Bucket, *fakeClock) {
	c := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	b := &Bucket{now: c.now}
	b.SetRate(tps)
	b.tokens = b.burst
	return b, c
}

func count(b *Bucket, n int) int {
	ok := 0
	for range n {
		if b.Allow() {
			ok++
		}
	}
	return ok
}

func TestBurstThenRefill(t *testing.T) {
	b, c := newTestBucket(100)
	if got := count(b, 150); got != 100 {
		t.Fatalf("initial burst: %d allowed, want 100", got)
	}
	c.advance(100 * time.Millisecond)
	if got := count(b, 50); got != 10 {
		t.Fatalf("after 100ms: %d allowed, want 10", got)
	}
	c.advance(10 * time.Second)
	if got := count(b, 500); got != 100 {
		t.Fatalf("refill is capped at burst: %d allowed", got)
	}
}

func TestSustainedRate(t *testing.T) {
	b, c := newTestBucket(1000)
	count(b, 1000) // drain the burst
	allowed := 0
	for range 10_000 { // 10 s in 1 ms steps, 5 attempts each
		c.advance(time.Millisecond)
		allowed += count(b, 5)
	}
	if allowed < 9990 || allowed > 10010 {
		t.Fatalf("allowed %d over 10s at 1000 TPS", allowed)
	}
}

func TestUnlimited(t *testing.T) {
	b, _ := newTestBucket(0)
	if count(b, 100_000) != 100_000 {
		t.Fatal("rate 0 must be unlimited")
	}
}

func TestSetRateLive(t *testing.T) {
	b, c := newTestBucket(10)
	count(b, 10)
	b.SetRate(1000)
	c.advance(100 * time.Millisecond)
	if got := count(b, 1000); got != 100 {
		t.Fatalf("after raising rate: %d allowed, want 100", got)
	}
	b.SetRate(5)
	c.advance(10 * time.Second)
	if got := count(b, 100); got != 5 {
		t.Fatalf("after lowering rate: %d allowed, want 5", got)
	}
}

func TestMessageLargerThanBurst(t *testing.T) {
	b, c := newTestBucket(2) // burst 2: a 3-part message never fits
	if !b.AllowN(3) {
		t.Fatal("a full bucket must pass a message larger than its burst")
	}
	if b.AllowN(1) {
		t.Fatal("the oversized message is paid back before the next one")
	}
	c.advance(time.Second) // tokens -1 -> 1
	if !b.AllowN(1) || b.AllowN(1) {
		t.Fatal("after one second only one token is back")
	}
	c.advance(10 * time.Second)
	if b.AllowN(3) != true || b.AllowN(3) {
		t.Fatal("refill is capped at the burst")
	}
}
