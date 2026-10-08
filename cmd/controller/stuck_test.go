package main

import (
	"strings"
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
	"github.com/MisterD0ctor/d7065e-project/internal/stuck"
)

// fixedForecast expects the same number of people at all times.
type fixedForecast float64

func (f fixedForecast) Expected(string, time.Time, time.Duration) (float64, bool) {
	return float64(f), true
}

// FR-7 for the counter: once the room's CO₂ contradicts a counter that has
// not moved, its reading is left out, as if it were missing, and the decision
// says what was found. A counter that CO₂ agrees with is used as before.
func TestStuckCounterIsLeftOut(t *testing.T) {
	size := sizing.ForCapacity(100, 50)
	k := rooms.Key{Level: "level0", Name: "A109"}
	start := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		co2        float64 // what the CO₂ sensor reads for an hour
		wantPeople float64
		wantMode   string
	}{
		{"40 counted, and the air of a full room", 950, 40, "normal"},
		{"40 counted, and the air of an empty room", 430, 0, "degraded_occupancy"},
	} {
		c := &controller{policy: Predictive, forecast: fixedForecast(0), people: map[rooms.Key]count{}, sensors: stuck.New()}
		now := start
		for range 60 {
			now = now.Add(time.Minute)
			c.people[k] = count{n: 40, at: now}
			c.sensors.Count(k, now, 40)
			c.sensors.CO2(k, now, tc.co2)
		}
		people, mode, fault := c.peopleFor(k, size, now)
		if people != tc.wantPeople || mode != tc.wantMode {
			t.Errorf("%s: ventilating for %v people in mode %s, want %v and %s", tc.name, people, mode, tc.wantPeople, tc.wantMode)
		}
		if stuck := mode != "normal"; stuck != strings.Contains(fault, "has read 40") {
			t.Errorf("%s: fault %q", tc.name, fault)
		}
	}
}

// A counter that has gone quiet is degraded too, but that is not a fault the
// controller found: the dashboard already knows the sensor is silent.
func TestSilentCounterHasNoFault(t *testing.T) {
	k := rooms.Key{Level: "level0", Name: "A109"}
	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	c := &controller{policy: Predictive, forecast: fixedForecast(0), people: map[rooms.Key]count{}, sensors: stuck.New()}
	c.people[k] = count{n: 40, at: now.Add(-10 * time.Minute)}
	if people, mode, fault := c.peopleFor(k, sizing.ForCapacity(100, 50), now); people != 0 || mode != "degraded_occupancy" || fault != "" {
		t.Errorf("ten-minute-old count: people %v, mode %s, fault %q", people, mode, fault)
	}
}
