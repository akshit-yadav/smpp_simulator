// Package timewheel is a hierarchical timing wheel for pending DLRs.
//
// Level 0 has 256 slots of one tick each; levels 1-4 have 64 slots, each slot
// spanning a whole turn of the level below. With a 5 ms tick that covers
// 1.28 s, 82 s, 87 min, 3.9 days and 248 days. Entries in higher levels are
// cascaded down as time reaches their slot. Add and Advance are O(1) amortised.
//
// Entries never fire early: a deadline is rounded up to the next tick. With
// Advance called every tick they fire at most one tick (plus scheduler
// jitter) late.
package timewheel

import (
	"sync"
	"time"
)

const (
	l0Bits  = 8
	lnBits  = 6
	levels  = 5
	l0Size  = 1 << l0Bits
	lnSize  = 1 << lnBits
	maxSpan = uint64(1) << (l0Bits + (levels-1)*lnBits)
)

type entry[T any] struct {
	at uint64 // absolute tick
	v  T
}

type Wheel[T any] struct {
	mu    sync.Mutex
	tick  time.Duration
	start time.Time
	cur   uint64 // last processed tick
	slots [levels][][]entry[T]
	count int
}

// New creates a wheel whose tick 0 is start.
func New[T any](tick time.Duration, start time.Time) *Wheel[T] {
	w := &Wheel[T]{tick: tick, start: start}
	w.slots[0] = make([][]entry[T], l0Size)
	for i := 1; i < levels; i++ {
		w.slots[i] = make([][]entry[T], lnSize)
	}
	return w
}

// Len returns the number of pending entries.
func (w *Wheel[T]) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.count
}

// Add schedules v to fire at the first tick at or after deadline. Deadlines
// already passed fire on the next tick.
func (w *Wheel[T]) Add(deadline time.Time, v T) {
	d := deadline.Sub(w.start)
	var at uint64
	if d > 0 {
		at = uint64((d + w.tick - 1) / w.tick)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.place(entry[T]{at: max(at, w.cur+1), v: v})
	w.count++
}

// place files e by its distance from the current tick. e.at >= w.cur; an entry
// due at w.cur (only possible while cascading) lands in the slot about to be
// processed.
func (w *Wheel[T]) place(e entry[T]) {
	delta := e.at - w.cur
	if delta >= maxSpan {
		delta = maxSpan - 1
		e.at = w.cur + delta
	}
	if delta < l0Size {
		i := e.at & (l0Size - 1)
		w.slots[0][i] = append(w.slots[0][i], e)
		return
	}
	for lvl := 1; lvl < levels; lvl++ {
		shift := uint(l0Bits + (lvl-1)*lnBits)
		if delta < uint64(1)<<(shift+lnBits) || lvl == levels-1 {
			i := (e.at >> shift) & (lnSize - 1)
			w.slots[lvl][i] = append(w.slots[lvl][i], e)
			return
		}
	}
}

// Advance processes every tick up to now and calls fire for each due entry,
// outside the lock. It returns the number fired.
func (w *Wheel[T]) Advance(now time.Time, fire func(T)) int {
	d := now.Sub(w.start)
	if d < 0 {
		return 0
	}
	target := uint64(d / w.tick)

	var due []T
	w.mu.Lock()
	for w.cur < target {
		w.cur++
		w.cascade()
		i := w.cur & (l0Size - 1)
		for _, e := range w.slots[0][i] {
			due = append(due, e.v)
		}
		clear(w.slots[0][i])
		w.slots[0][i] = w.slots[0][i][:0]
	}
	w.count -= len(due)
	w.mu.Unlock()

	for _, v := range due {
		fire(v)
	}
	return len(due)
}

// cascade moves the entries of every higher-level slot that starts at the
// current tick down into the levels below. Highest level first, so entries
// can fall more than one level.
func (w *Wheel[T]) cascade() {
	for lvl := levels - 1; lvl >= 1; lvl-- {
		shift := uint(l0Bits + (lvl-1)*lnBits)
		if w.cur&(uint64(1)<<shift-1) != 0 {
			continue
		}
		i := (w.cur >> shift) & (lnSize - 1)
		moved := w.slots[lvl][i]
		w.slots[lvl][i] = nil
		for _, e := range moved {
			w.place(e)
		}
	}
}

// Drain removes every pending entry, regardless of deadline, and calls fn for
// each (outside the lock). Used on shutdown.
func (w *Wheel[T]) Drain(fn func(T)) int {
	var all []T
	w.mu.Lock()
	for lvl := range w.slots {
		for i, slot := range w.slots[lvl] {
			for _, e := range slot {
				all = append(all, e.v)
			}
			w.slots[lvl][i] = nil
		}
	}
	w.count = 0
	w.mu.Unlock()
	for _, v := range all {
		fn(v)
	}
	return len(all)
}
