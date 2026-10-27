// Package outcome decides what happens to each accepted submit_sm: the
// command_status to answer with and, if accepted, the DLR status and error.
//
// A Profile is compiled once (when saved or loaded) into a Picker, whose Pick
// is O(1): an alias table for weighted probabilistic mode, or a shuffled deck
// of 10,000 for exact mode.
package outcome

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"

	"github.com/akshit-yadav/smpp_simulator/worker/pkg/smpp/pdu"
)

// MessageStates maps DLR stat: values to the message_state TLV value.
var MessageStates = map[string]byte{
	"ENROUTE": 1, "DELIVRD": 2, "EXPIRED": 3, "DELETED": 4,
	"UNDELIV": 5, "ACCEPTD": 6, "UNKNOWN": 7, "REJECTD": 8,
}

// IsIntermediate reports whether a DLR status is non-final.
func IsIntermediate(stat string) bool { return stat == "ENROUTE" || stat == "ACCEPTD" }

const (
	ModeFixed    = "fixed"
	ModeRandom   = "random"
	ModeWeighted = "weighted"

	DistProbabilistic = "probabilistic"
	DistExact         = "exact"

	// DeckSize is the block size in exact mode; weights are basis points of it.
	DeckSize = 10_000
)

type NetworkError struct {
	Type byte   `json:"type"` // 3 = GSM
	Code uint16 `json:"code"`
}

// Delay is a DLR delay range in milliseconds. Min == Max is a fixed delay.
type Delay struct {
	MinMS int `json:"min_ms"`
	MaxMS int `json:"max_ms"`
}

type Outcome struct {
	Name          string        `json:"name,omitempty"`
	CommandStatus pdu.Status    `json:"command_status"`
	DLRStatus     string        `json:"dlr_status"`
	DLRError      string        `json:"dlr_error"`
	NetworkError  *NetworkError `json:"network_error_code,omitempty"`
	DLRText       string        `json:"dlr_text,omitempty"`
	WeightBP      int           `json:"weight_bp,omitempty"`
	DLRDelay      *Delay        `json:"dlr_delay,omitempty"` // overrides the gateway's timing
}

// Accepted reports whether the message is accepted (gets a message ID).
func (o *Outcome) Accepted() bool { return o.CommandStatus == pdu.ESME_ROK }

// Delivered reports whether the final DLR is a success.
func (o *Outcome) Delivered() bool { return o.DLRStatus == "DELIVRD" }

type Profile struct {
	Name         string    `json:"name,omitempty"`
	Mode         string    `json:"mode"`
	Distribution string    `json:"distribution,omitempty"`
	Seed         *uint64   `json:"seed,omitempty"`
	Outcomes     []Outcome `json:"outcomes"`
}

// Default is used when a gateway has no profile: accept and deliver everything.
var Default = Profile{
	Name: "default", Mode: ModeFixed,
	Outcomes: []Outcome{{Name: "delivered", DLRStatus: "DELIVRD", DLRError: "000"}},
}

func (p Profile) Validate() error {
	if len(p.Outcomes) == 0 {
		return errors.New("profile needs at least one outcome")
	}
	switch p.Mode {
	case ModeFixed, ModeRandom:
	case ModeWeighted:
		total := 0
		for _, o := range p.Outcomes {
			if o.WeightBP < 0 {
				return fmt.Errorf("outcome %q: negative weight", o.Name)
			}
			total += o.WeightBP
		}
		if total != DeckSize {
			return fmt.Errorf("weights total %d basis points, want %d (100%%)", total, DeckSize)
		}
	default:
		return fmt.Errorf("unknown mode %q", p.Mode)
	}
	switch p.Distribution {
	case "", DistProbabilistic, DistExact:
	default:
		return fmt.Errorf("unknown distribution %q", p.Distribution)
	}
	for _, o := range p.Outcomes {
		if err := o.validate(); err != nil {
			return fmt.Errorf("outcome %q: %w", o.Name, err)
		}
	}
	return nil
}

func (o Outcome) validate() error {
	if _, std := pdu.StatusNames[o.CommandStatus]; !std && !o.CommandStatus.IsVendor() {
		return fmt.Errorf("command_status 0x%X is neither SMPP 3.4 nor vendor (0x400-0x4FF)", uint32(o.CommandStatus))
	}
	if !o.Accepted() {
		return nil
	}
	if _, ok := MessageStates[o.DLRStatus]; !ok {
		return fmt.Errorf("unknown dlr_status %q", o.DLRStatus)
	}
	if len(o.DLRError) > 3 {
		return fmt.Errorf("dlr_error %q longer than 3 characters", o.DLRError)
	}
	if d := o.DLRDelay; d != nil && (d.MinMS < 0 || d.MaxMS < d.MinMS) {
		return fmt.Errorf("invalid dlr_delay %d-%d ms", d.MinMS, d.MaxMS)
	}
	return nil
}

// Picker picks outcomes for one compiled profile. Safe for concurrent use.
type Picker struct {
	Profile  Profile
	outcomes []Outcome
	pick     func() int
}

// Pick returns the outcome for the next message. The result must not be modified.
func (p *Picker) Pick() *Outcome { return &p.outcomes[p.pick()] }

// Compile validates a profile and builds its picker.
func Compile(p Profile) (*Picker, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	pk := &Picker{Profile: p, outcomes: p.Outcomes}
	r := newRNG(p.Seed)
	n := len(p.Outcomes)

	switch {
	case p.Mode == ModeFixed || n == 1:
		pk.pick = func() int { return 0 }
	case p.Distribution == DistExact:
		pk.pick = newDeck(weights(p), r).next
	case p.Mode == ModeRandom:
		pk.pick = func() int { return r.IntN(n) }
	default:
		pk.pick = newAlias(weights(p), r).next
	}
	return pk, nil
}

// weights returns basis-point weights; random mode splits 10,000 evenly.
func weights(p Profile) []int {
	w := make([]int, len(p.Outcomes))
	for i, o := range p.Outcomes {
		if p.Mode == ModeWeighted {
			w[i] = o.WeightBP
		} else {
			w[i] = DeckSize / len(w)
			if i < DeckSize%len(w) {
				w[i]++
			}
		}
	}
	return w
}

// rng abstracts over a seeded (mutex-guarded, repeatable) source and the
// lock-free global source.
type rng interface {
	IntN(n int) int
	Float64() float64
}

type globalRNG struct{}

func (globalRNG) IntN(n int) int   { return rand.IntN(n) }
func (globalRNG) Float64() float64 { return rand.Float64() }

type lockedRNG struct {
	mu sync.Mutex
	r  *rand.Rand
}

func (l *lockedRNG) IntN(n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.IntN(n)
}

func (l *lockedRNG) Float64() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.Float64()
}

func newRNG(seed *uint64) rng {
	if seed == nil {
		return globalRNG{}
	}
	return &lockedRNG{r: rand.New(rand.NewPCG(*seed, *seed^0x9E3779B97F4A7C15))}
}

// alias is Vose's alias table: O(1) weighted sampling.
type alias struct {
	prob  []float64
	alias []int
	r     rng
}

func newAlias(w []int, r rng) *alias {
	n := len(w)
	a := &alias{prob: make([]float64, n), alias: make([]int, n), r: r}
	total := 0
	for _, x := range w {
		total += x
	}
	scaled := make([]float64, n)
	var small, large []int
	for i, x := range w {
		scaled[i] = float64(x) * float64(n) / float64(total)
		if scaled[i] < 1 {
			small = append(small, i)
		} else {
			large = append(large, i)
		}
	}
	for len(small) > 0 && len(large) > 0 {
		s, l := small[len(small)-1], large[len(large)-1]
		small, large = small[:len(small)-1], large[:len(large)-1]
		a.prob[s], a.alias[s] = scaled[s], l
		scaled[l] += scaled[s] - 1
		if scaled[l] < 1 {
			small = append(small, l)
		} else {
			large = append(large, l)
		}
	}
	for _, i := range append(small, large...) {
		a.prob[i] = 1
	}
	return a
}

func (a *alias) next() int {
	i := a.r.IntN(len(a.prob))
	if a.r.Float64() < a.prob[i] {
		return i
	}
	return a.alias[i]
}

// deck hands out a shuffled block of 10,000 picks, so every block matches the
// weights exactly.
type deck struct {
	mu    sync.Mutex
	cards []int
	pos   int
	r     rng
}

func newDeck(w []int, r rng) *deck {
	d := &deck{cards: make([]int, 0, DeckSize), r: r}
	for i, x := range w {
		for range x {
			d.cards = append(d.cards, i)
		}
	}
	d.pos = len(d.cards) // shuffle on first use
	return d
}

func (d *deck) next() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pos == len(d.cards) {
		for i := len(d.cards) - 1; i > 0; i-- {
			j := d.r.IntN(i + 1)
			d.cards[i], d.cards[j] = d.cards[j], d.cards[i]
		}
		d.pos = 0
	}
	c := d.cards[d.pos]
	d.pos++
	return c
}
