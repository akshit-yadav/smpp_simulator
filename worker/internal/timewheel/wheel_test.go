package timewheel

import (
	"math/rand/v2"
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0)

const tick = 5 * time.Millisecond

// run advances one tick at a time until end, recording the tick each value fired at.
func run(w *Wheel[int], end time.Duration) map[int]time.Duration {
	fired := map[int]time.Duration{}
	for at := time.Duration(0); at <= end; at += tick {
		w.Advance(t0.Add(at), func(v int) {
			if _, dup := fired[v]; dup {
				panic("fired twice")
			}
			fired[v] = at
		})
	}
	return fired
}

func TestFiresOnTimeAcrossAllLevels(t *testing.T) {
	w := New[int](tick, t0)
	delays := []time.Duration{
		0, 1 * time.Millisecond, 5 * time.Millisecond, 7 * time.Millisecond,
		500 * time.Millisecond, 1280 * time.Millisecond, 1281 * time.Millisecond, 2 * time.Second,
		81 * time.Second, 82 * time.Second, 83 * time.Second,
		90 * time.Minute, 3 * time.Hour, 73 * time.Hour,
	}
	for i, d := range delays {
		w.Add(t0.Add(d), i)
	}
	if w.Len() != len(delays) {
		t.Fatalf("Len = %d", w.Len())
	}
	fired := run(w, 74*time.Hour)
	for i, d := range delays {
		got, ok := fired[i]
		if !ok {
			t.Fatalf("delay %v never fired", d)
		}
		want := max(d.Round(tick), tick)
		if d%tick != 0 {
			want = d - d%tick + tick
		}
		if got != want {
			t.Errorf("delay %v fired at %v, want %v", d, got, want)
		}
	}
	if w.Len() != 0 {
		t.Fatalf("Len after firing = %d", w.Len())
	}
}

func TestRandomisedAddsWhileRunning(t *testing.T) {
	w := New[int](tick, t0)
	r := rand.New(rand.NewPCG(1, 2))
	type job struct{ deadline time.Duration }
	jobs := map[int]job{}
	fired := map[int]time.Duration{}
	id := 0
	end := 30 * time.Minute
	for now := time.Duration(0); now <= end+2*time.Hour; now += tick {
		if now <= end && r.IntN(4) == 0 {
			d := now + time.Duration(r.Int64N(int64(90*time.Minute)))
			jobs[id] = job{d}
			w.Add(t0.Add(d), id)
			id++
		}
		w.Advance(t0.Add(now), func(v int) { fired[v] = now })
	}
	if len(fired) != len(jobs) {
		t.Fatalf("fired %d of %d", len(fired), len(jobs))
	}
	for k, j := range jobs {
		late := fired[k] - j.deadline
		if late < 0 || late >= tick {
			t.Fatalf("job %d: deadline %v fired %v (late %v)", k, j.deadline, fired[k], late)
		}
	}
}

func TestPastDeadlineFiresNextTick(t *testing.T) {
	w := New[int](tick, t0)
	w.Advance(t0.Add(time.Second), func(int) {})
	w.Add(t0, 1)
	n := w.Advance(t0.Add(time.Second+tick), func(int) {})
	if n != 1 {
		t.Fatalf("fired %d", n)
	}
}

func TestBigJumpFiresEverything(t *testing.T) {
	w := New[int](tick, t0)
	for i := range 1000 {
		w.Add(t0.Add(time.Duration(i)*time.Second), i)
	}
	n := w.Advance(t0.Add(2000*time.Second), func(int) {})
	if n != 1000 {
		t.Fatalf("fired %d", n)
	}
}

func BenchmarkAddAdvance(b *testing.B) {
	w := New[int](tick, t0)
	now := t0
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		w.Add(now.Add(time.Duration(500+i%1500)*time.Millisecond), i)
		if i%50 == 0 { // 50 adds per 5 ms tick ≈ 10k/s; bench measures per-op cost
			now = now.Add(tick)
			w.Advance(now, func(int) {})
		}
		i++
	}
}

func TestDrain(t *testing.T) {
	w := New[int](tick, t0)
	for i := range 100 {
		w.Add(t0.Add(time.Duration(i)*time.Minute), i)
	}
	got := 0
	if n := w.Drain(func(int) { got++ }); n != 100 || got != 100 || w.Len() != 0 {
		t.Fatalf("drained %d/%d, Len %d", n, got, w.Len())
	}
	if w.Advance(t0.Add(200*time.Minute), func(int) { t.Fatal("fired after drain") }) != 0 {
		t.Fatal("entries left")
	}
}
