package main

import (
	"math"
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

var (
	fika  = rooms.Key{Level: "level0", Name: "1570"}
	empty = rooms.Key{Level: "level0", Name: "A109"}
	nine  = time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
)

func testWorld() *world {
	return newWorld([]rooms.Key{fika, empty}, map[rooms.Key]sizing.Room{
		fika:  sizing.ForCapacity(101.2, 25),
		empty: sizing.ForCapacity(18.5, 1),
	})
}

func minute(offset int, people, guards int) clock.Minute {
	m := clock.Minute{Time: nine.Add(time.Duration(offset) * time.Minute), Rooms: map[string]clock.RoomCount{}}
	if people > 0 {
		m.Rooms[fika.String()] = clock.RoomCount{Occupants: people, Guards: guards}
	}
	return m
}

// The first minute only sets the starting point; after that each minute is
// stepped with the people who were there at its start.
func TestAdvanceUsesOccupancyAtTheStartOfTheMinute(t *testing.T) {
	w := testWorld()
	w.advance(minute(0, 0, 0))
	if got := w.models[fika].CO2; got != sizing.OutdoorCO2 {
		t.Fatalf("the first minute stepped the room: CO₂ %.1f", got)
	}
	w.advance(minute(1, 20, 0)) // 20 people arrive during this minute
	if got := w.models[fika].CO2; got != sizing.OutdoorCO2 {
		t.Errorf("09:00–09:01 was empty at its start, but CO₂ rose to %.1f", got)
	}
	if tr := w.truth(fika); tr.Occupancy != 20 || tr.ModelTime != "2026-09-21T09:01:00Z" {
		t.Errorf("truth at 09:01: %+v", tr)
	}
	w.advance(minute(2, 20, 0))
	if got := w.models[fika].CO2; got <= sizing.OutdoorCO2 {
		t.Errorf("20 people for a minute, but CO₂ is still %.1f", got)
	}
	if got := w.models[empty].CO2; got != sizing.OutdoorCO2 {
		t.Errorf("the empty room's CO₂ changed to %.1f", got)
	}
}

// The same minutes give the same truth however they are batched: one per
// tick, or several at once after physics fell behind. This is the property
// that makes two runs of a day comparable.
func TestTruthDoesNotDependOnBatching(t *testing.T) {
	people := []int{0, 3, 12, 20, 20, 18, 5, 0, 0, 0}
	run := func(batches [][]int) *world {
		w := testWorld()
		for _, batch := range batches {
			w.act[fika] = actuation{AirflowLs: 100, Setpoint: 21} // as read once per tick
			for _, i := range batch {
				w.advance(minute(i, people[i], 0))
			}
		}
		return w
	}
	one := run([][]int{{0}, {1}, {2}, {3}, {4}, {5}, {6}, {7}, {8}, {9}})
	bursts := run([][]int{{0, 1, 2}, {3}, {4, 5, 6, 7, 8}, {9}})
	if a, b := one.models[fika], bursts.models[fika]; a.CO2 != b.CO2 || a.Temp != b.Temp {
		t.Fatalf("one minute per tick gave %.6f ppm / %.6f °C, bursts gave %.6f / %.6f", a.CO2, a.Temp, b.CO2, b.Temp)
	}
	if one.models[fika].CO2 < 430 {
		t.Fatalf("CO₂ only reached %.1f ppm: the comparison proved nothing", one.models[fika].CO2)
	}
}

// A gap in the minutes is stepped in one go with the occupancy before it.
func TestAdvanceOverAGap(t *testing.T) {
	gap, steps := testWorld(), testWorld()
	gap.advance(minute(0, 10, 0))
	gap.advance(minute(5, 10, 0))
	for i := 0; i <= 5; i++ {
		steps.advance(minute(i, 10, 0))
	}
	if a, b := gap.models[fika].CO2, steps.models[fika].CO2; math.Abs(a-b) > 1e-6 {
		t.Errorf("gap of 5 min: %.6f ppm, minute by minute: %.6f", a, b)
	}
}

// A minute that isn't later than the last one (the clock was moved back for a
// new experiment) restarts there without stepping.
func TestAdvanceAfterTheClockMovedBack(t *testing.T) {
	w := testWorld()
	w.advance(minute(10, 20, 0))
	w.advance(minute(11, 20, 0))
	before := w.models[fika].CO2
	w.advance(minute(0, 2, 0))
	if w.models[fika].CO2 != before {
		t.Error("moving back stepped the room")
	}
	if !w.last.Equal(nine) || w.occ[fika].Occupants != 2 {
		t.Errorf("after moving back: last %v, %d people", w.last, w.occ[fika].Occupants)
	}
}

func TestTruthCountsGuards(t *testing.T) {
	w := testWorld()
	w.advance(minute(0, 1, 1))
	if tr := w.truth(fika); tr.Occupancy != 1 || tr.Guards != 1 {
		t.Errorf("a guard alone in the room: %+v", tr)
	}
}
