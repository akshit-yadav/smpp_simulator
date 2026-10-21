package outcome

import (
	"math"
	"strings"
	"testing"

	"github.com/akshit-yadav/smpp_simulator/worker/pkg/smpp/pdu"
)

func mix() []Outcome {
	return []Outcome{
		{Name: "delivered", DLRStatus: "DELIVRD", DLRError: "000", WeightBP: 9000},
		{Name: "undeliv", DLRStatus: "UNDELIV", DLRError: "001", WeightBP: 650},
		{Name: "template", DLRStatus: "REJECTD", DLRError: "T01", WeightBP: 250},
		{Name: "throttled", CommandStatus: pdu.ESME_RTHROTTLED, WeightBP: 75},
		{Name: "vendor", CommandStatus: 0x401, WeightBP: 25},
	}
}

func counts(t *testing.T, p Profile, n int) []int {
	t.Helper()
	pk, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	idx := map[*Outcome]int{}
	for i := range pk.outcomes {
		idx[&pk.outcomes[i]] = i
	}
	c := make([]int, len(p.Outcomes))
	for range n {
		c[idx[pk.Pick()]]++
	}
	return c
}

func TestFixed(t *testing.T) {
	p := Profile{Mode: ModeFixed, Outcomes: mix()[1:2]}
	if c := counts(t, p, 1000); c[0] != 1000 {
		t.Fatal(c)
	}
}

func TestWeightedProbabilisticWithinHalfPoint(t *testing.T) {
	const n = 1_000_000
	p := Profile{Mode: ModeWeighted, Distribution: DistProbabilistic, Outcomes: mix()}
	c := counts(t, p, n)
	for i, o := range p.Outcomes {
		got := float64(c[i]) / n * 100
		want := float64(o.WeightBP) / 100
		if math.Abs(got-want) > 0.5 {
			t.Errorf("%s: %.3f%%, want %.2f%% ±0.5", o.Name, got, want)
		}
	}
}

func TestExactEveryBlock(t *testing.T) {
	p := Profile{Mode: ModeWeighted, Distribution: DistExact, Outcomes: mix()}
	pk, _ := Compile(p)
	for block := range 5 {
		c := map[string]int{}
		for range DeckSize {
			c[pk.Pick().Name]++
		}
		for _, o := range p.Outcomes {
			if c[o.Name] != o.WeightBP {
				t.Fatalf("block %d: %s = %d, want %d", block, o.Name, c[o.Name], o.WeightBP)
			}
		}
	}
}

func TestRandomUniform(t *testing.T) {
	const n = 300_000
	p := Profile{Mode: ModeRandom, Outcomes: mix()[:3]}
	for i, c := range counts(t, p, n) {
		if math.Abs(float64(c)/n-1.0/3) > 0.01 {
			t.Errorf("outcome %d: %d of %d", i, c, n)
		}
	}
	p.Distribution = DistExact
	c := counts(t, p, DeckSize)
	if c[0] != 3334 || c[1] != 3333 || c[2] != 3333 {
		t.Fatalf("exact random block: %v", c)
	}
}

func TestSeedRepeatable(t *testing.T) {
	seed := uint64(42)
	for _, dist := range []string{DistProbabilistic, DistExact} {
		p := Profile{Mode: ModeWeighted, Distribution: dist, Seed: &seed, Outcomes: mix()}
		a, _ := Compile(p)
		b, _ := Compile(p)
		for i := range 5000 {
			if a.Pick().Name != b.Pick().Name {
				t.Fatalf("%s: diverged at %d", dist, i)
			}
		}
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]Profile{
		"weights total":   {Mode: ModeWeighted, Outcomes: []Outcome{{DLRStatus: "DELIVRD", WeightBP: 5000}}},
		"unknown mode":    {Mode: "nope", Outcomes: mix()},
		"no outcomes":     {Mode: ModeFixed},
		"bad dlr status":  {Mode: ModeFixed, Outcomes: []Outcome{{DLRStatus: "LOST"}}},
		"long dlr error":  {Mode: ModeFixed, Outcomes: []Outcome{{DLRStatus: "UNDELIV", DLRError: "ABCD"}}},
		"non-vendor code": {Mode: ModeFixed, Outcomes: []Outcome{{CommandStatus: 0x500}}},
		"bad delay":       {Mode: ModeFixed, Outcomes: []Outcome{{DLRStatus: "DELIVRD", DLRDelay: &Delay{MinMS: 10, MaxMS: 5}}}},
		"unknown dist":    {Mode: ModeFixed, Distribution: "x", Outcomes: mix()[:1]},
	}
	for name, p := range cases {
		if _, err := Compile(p); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := Compile(Default); err != nil {
		t.Fatal(err)
	}
	// Rejected outcomes need no DLR fields.
	if err := (Profile{Mode: ModeFixed, Outcomes: []Outcome{{CommandStatus: pdu.ESME_RSYSERR}}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Profile{Mode: ModeFixed, Outcomes: []Outcome{{CommandStatus: 0x500}}}).Validate(); err == nil || !strings.Contains(err.Error(), "vendor") {
		t.Fatal(err)
	}
}

func BenchmarkPickWeighted(b *testing.B) {
	pk, _ := Compile(Profile{Mode: ModeWeighted, Outcomes: mix()})
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			pk.Pick()
		}
	})
}
