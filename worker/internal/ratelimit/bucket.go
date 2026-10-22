// Package ratelimit implements the token buckets used for gateway TPS limits
// and the per-worker licence TPS cap.
package ratelimit

import (
	"sync"
	"time"
)

// Bucket is a token bucket that refills continuously at Rate tokens per
// second, holding at most Burst tokens. A rate of 0 means unlimited.
type Bucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

// New returns a full bucket. The burst equals one second of traffic, so a
// client that sends its per-second quota in one go is not throttled.
func New(tps float64) *Bucket {
	b := &Bucket{now: time.Now}
	b.SetRate(tps)
	b.tokens = b.burst
	return b
}

// SetRate changes the rate live, keeping the current tokens (capped at the
// new burst).
func (b *Bucket) SetRate(tps float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	b.rate = tps
	b.burst = max(tps, 1)
	b.tokens = min(b.tokens, b.burst)
}

// Rate returns the configured tokens per second.
func (b *Bucket) Rate() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rate
}

// Allow takes one token if available.
func (b *Bucket) Allow() bool { return b.AllowN(1) }

// AllowN takes n tokens if all are available (e.g. every part of a long
// message), or none.
func (b *Bucket) AllowN(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rate <= 0 {
		return true
	}
	b.refill()
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

func (b *Bucket) refill() {
	now := b.now()
	if !b.last.IsZero() {
		b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	}
	b.last = now
}
