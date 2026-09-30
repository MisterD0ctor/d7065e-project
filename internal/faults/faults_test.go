package faults

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

// T-03: noise is unbiased and ~95 % of readings fall inside the FR-2 accuracy.
func TestNoiseStatistics(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, tc := range []struct {
		kind  string
		truth float64
		acc   float64
	}{
		{rooms.CO2, 800, 30 + 0.03*800},
		{rooms.Temp, 21, 0.3},
		{rooms.Occupancy, 12, 1},
	} {
		const n = 10000
		var sum float64
		inside := 0
		for range n {
			v := Noise(tc.kind, tc.truth, rng)
			sum += v - tc.truth
			if math.Abs(v-tc.truth) <= tc.acc {
				inside++
			}
		}
		if mean := sum / n; math.Abs(mean) > tc.acc/20 {
			t.Errorf("%s: mean error %.3f, want ≈0", tc.kind, mean)
		}
		if share := float64(inside) / n; share < 0.93 {
			t.Errorf("%s: %.1f %% within ±%.2f, want ≥ 93 %%", tc.kind, share*100, tc.acc)
		}
	}
}

// T-04: each fault mode produces its signal and leaves other rooms alone.
func TestFaultModes(t *testing.T) {
	a := rooms.Key{Level: "level0", Name: "A1"}
	b := rooms.Key{Level: "level0", Name: "B2"}
	t0 := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	fs, err := Parse("stuck:level0/A1")
	if err != nil {
		t.Fatal(err)
	}
	in := NewInjector(fs)
	in.Apply(a, t0, 600)
	if v, _ := in.Apply(a, t0.Add(time.Minute), 900); v != 600 {
		t.Errorf("stuck: got %v, want the first reading 600", v)
	}
	if v, _ := in.Apply(b, t0.Add(time.Minute), 900); v != 900 {
		t.Errorf("stuck leaked into another room: %v", v)
	}

	fs, _ = Parse("dropout:level0/A1")
	if _, ok := NewInjector(fs).Apply(a, t0, 600); ok {
		t.Error("dropout: reading was published")
	}

	fs, _ = Parse("drift:level0/A1:+50")
	in = NewInjector(fs)
	in.Apply(a, t0, 600)
	if v, _ := in.Apply(a, t0.Add(2*time.Hour), 600); v != 700 {
		t.Errorf("drift: got %v after 2 h at +50/h, want 700", v)
	}
}

func TestParseRejectsBadFaults(t *testing.T) {
	for _, s := range []string{"stuck", "melt:level0/A1", "drift:level0/A1", "stuck:A1"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) accepted a bad fault", s)
		}
	}
}
