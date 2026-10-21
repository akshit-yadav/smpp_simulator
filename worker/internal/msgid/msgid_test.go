package msgid

import (
	"sync"
	"testing"
	"time"
)

func TestLayout(t *testing.T) {
	g := New(0xAB)
	at := Epoch.Add(1234567 * time.Millisecond)
	g.now = func() time.Time { return at }
	id := g.Next()
	if WorkerID(id) != 0xAB || !Time(id).Equal(at) || id&seqMask != 0 {
		t.Fatalf("id %x: worker %x time %v seq %d", id, WorkerID(id), Time(id), id&seqMask)
	}
	if id2 := g.Next(); id2 != id+1 {
		t.Fatalf("second id in same ms: %x after %x", id2, id)
	}
}

func TestMonotonicWhenClockStepsBack(t *testing.T) {
	g := New(1)
	at := Epoch.Add(time.Hour)
	g.now = func() time.Time { return at }
	a := g.Next()
	at = at.Add(-time.Minute)
	if b := g.Next(); b <= a {
		t.Fatalf("not monotonic: %x then %x", a, b)
	}
}

func TestSequenceOverflowBorrowsNextMillisecond(t *testing.T) {
	g := New(1)
	at := Epoch.Add(time.Second)
	g.now = func() time.Time { return at }
	var last uint64
	for range seqMask + 2 {
		last = g.Next()
	}
	if !Time(last).Equal(at.Add(time.Millisecond)) || last&seqMask != 0 {
		t.Fatalf("after overflow: time %v seq %d", Time(last), last&seqMask)
	}
}

func TestUniqueConcurrent(t *testing.T) {
	g := New(7)
	const workers, per = 8, 50000
	ids := make(chan uint64, workers*per)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range per {
				ids <- g.Next()
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := make(map[uint64]bool, workers*per)
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate %x", id)
		}
		seen[id] = true
	}
}

func TestFormats(t *testing.T) {
	if Hex(255) != "ff" || Decimal(255) != "255" {
		t.Fatal(Hex(255), Decimal(255))
	}
}
