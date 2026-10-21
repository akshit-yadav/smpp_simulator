// Package msgid generates 64-bit message IDs:
//
//	worker ID (8 bits) | milliseconds since Epoch (40 bits) | sequence (16 bits)
//
// IDs are unique per worker and monotonic even if the wall clock steps back.
package msgid

import (
	"strconv"
	"sync/atomic"
	"time"
)

// Epoch is the zero point of the 40-bit millisecond field (~34.8 years of range).
var Epoch = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	seqBits  = 16
	timeBits = 40
	seqMask  = 1<<seqBits - 1
	timeMask = 1<<timeBits - 1
)

type Generator struct {
	worker uint64
	state  atomic.Uint64 // ms<<16 | seq of the last issued ID
	now    func() time.Time
}

func New(workerID uint8) *Generator {
	return &Generator{worker: uint64(workerID) << (timeBits + seqBits), now: time.Now}
}

// Next returns a new ID. Lock-free; if 65,536 IDs are used within one
// millisecond it borrows from the next millisecond.
func (g *Generator) Next() uint64 {
	ms := uint64(g.now().Sub(Epoch)/time.Millisecond) & timeMask
	for {
		old := g.state.Load()
		next := ms << seqBits
		if next <= old {
			next = old + 1
		}
		if g.state.CompareAndSwap(old, next) {
			return g.worker | next&(timeMask<<seqBits|seqMask)
		}
	}
}

// Hex formats an ID the way it appears in submit_sm_resp.
func Hex(id uint64) string { return strconv.FormatUint(id, 16) }

// Decimal formats an ID in base 10 (optional DLR "id:" format).
func Decimal(id uint64) string { return strconv.FormatUint(id, 10) }

// WorkerID extracts the worker ID from an ID.
func WorkerID(id uint64) uint8 { return uint8(id >> (timeBits + seqBits)) }

// Time extracts the millisecond timestamp from an ID.
func Time(id uint64) time.Time {
	return Epoch.Add(time.Duration(id>>seqBits&timeMask) * time.Millisecond)
}
