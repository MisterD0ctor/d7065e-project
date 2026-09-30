// Package faults turns a true value into what a sensor would report: noise
// for every reading, plus faults injected on purpose (FR-2, FR-7). Injected
// faults are never marked in the output; the controller has to notice them.
package faults

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

// Noise adds measurement noise for a sensor kind. The spread is set so that
// about 95 % of readings fall within the accuracy in FR-2: CO₂ ±(30 ppm + 3 %),
// temperature ±0.3 °C, count ±1.
func Noise(kind string, truth float64, rng *rand.Rand) float64 {
	switch kind {
	case rooms.CO2:
		return truth + rng.NormFloat64()*(30+0.03*truth)/2
	case rooms.Temp:
		return truth + rng.NormFloat64()*0.15
	case rooms.Occupancy:
		n := math.Round(truth)
		switch p := rng.Float64(); {
		case p < 0.05 && n > 0:
			n--
		case p >= 0.95:
			n++
		}
		return n
	}
	return truth
}

// Mode is an injected fault.
type Mode string

const (
	Stuck   Mode = "stuck"   // repeats the last good reading
	Dropout Mode = "dropout" // publishes nothing
	Drift   Mode = "drift"   // adds Rate per model hour since the fault started
)

type Fault struct {
	Mode Mode
	Room rooms.Key
	Rate float64 // drift only
}

// Parse reads a FAULTS list such as
// "stuck:level0/A1111, drift:level0/A1111:+50".
func Parse(s string) ([]Fault, error) {
	var out []Fault
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Split(part, ":")
		if len(fields) < 2 {
			return nil, fmt.Errorf("fault %q: want <mode>:<level>/<room>[:rate]", part)
		}
		k, err := rooms.Parse(fields[1])
		if err != nil {
			return nil, err
		}
		f := Fault{Mode: Mode(fields[0]), Room: k}
		switch f.Mode {
		case Stuck, Dropout:
		case Drift:
			if len(fields) != 3 {
				return nil, fmt.Errorf("fault %q: drift needs a rate per hour", part)
			}
			if f.Rate, err = strconv.ParseFloat(fields[2], 64); err != nil {
				return nil, fmt.Errorf("fault %q: %w", part, err)
			}
		default:
			return nil, fmt.Errorf("fault %q: unknown mode", part)
		}
		out = append(out, f)
	}
	return out, nil
}

// Injector applies the configured faults to one sensor kind's readings.
type Injector struct {
	byRoom map[rooms.Key]Fault
	start  map[rooms.Key]time.Time
	last   map[rooms.Key]float64
}

func NewInjector(fs []Fault) *Injector {
	in := &Injector{byRoom: map[rooms.Key]Fault{}, start: map[rooms.Key]time.Time{}, last: map[rooms.Key]float64{}}
	for _, f := range fs {
		in.byRoom[f.Room] = f
	}
	return in
}

// Apply returns the reading to publish and whether to publish it at all.
func (in *Injector) Apply(k rooms.Key, t time.Time, reading float64) (float64, bool) {
	f, ok := in.byRoom[k]
	if !ok {
		in.last[k] = reading
		return reading, true
	}
	if _, started := in.start[k]; !started {
		in.start[k] = t
	}
	switch f.Mode {
	case Dropout:
		return 0, false
	case Stuck:
		if v, ok := in.last[k]; ok {
			return v, true
		}
		in.last[k] = reading
		return reading, true
	case Drift:
		return reading + f.Rate*t.Sub(in.start[k]).Hours(), true
	}
	return reading, true
}
