package sim_test

import (
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/eislab-cps/D7065E/occupancysim/internal/sim"
)

// collect drives a simulation from Monday 00:00 to the given clock time with
// the supplied step sizes (simulated time per call to Advance, repeated), and
// returns every whole-minute occupancy it produced along the way.
func collect(t *testing.T, until string, steps func() time.Duration) []sim.MinuteOccupancy {
	t.Helper()
	s := newSim(t, sim.DefaultParameters(), at(monday, "12:00"))
	s.SetTime(at(monday, "00:00")) // as an experiment does: replay the day from midnight
	end := at(monday, until)
	var all []sim.MinuteOccupancy
	var last time.Time
	for {
		clock, minutes := s.Minutes(last)
		for _, m := range minutes {
			all = append(all, m)
			last = m.Time
		}
		now, err := time.Parse(time.RFC3339, clock.Time)
		if err != nil {
			t.Fatal(err)
		}
		if !now.Before(end) {
			return all
		}
		s.Advance(steps())
	}
}

// The same day gives the same occupancy at every whole minute, however
// unevenly the wall clock drives the simulation. This is what makes two runs
// of one day comparable minute by minute.
func TestMinutesDoNotDependOnTickTiming(t *testing.T) {
	steady := collect(t, "11:00", func() time.Duration { return 12 * time.Second })
	rng := rand.New(rand.NewSource(1))
	jittery := collect(t, "11:00", func() time.Duration {
		// Anything from a fraction of a step to a stall of over a minute.
		return time.Duration(rng.Intn(90_000)) * time.Millisecond
	})

	// Compare up to the last minute both reached.
	n := min(len(steady), len(jittery))
	if n < 11*60 {
		t.Fatalf("only %d and %d minutes collected", len(steady), len(jittery))
	}
	for i := range n {
		if !steady[i].Time.Equal(jittery[i].Time) {
			t.Fatalf("minute %d: times %v and %v", i, steady[i].Time, jittery[i].Time)
		}
		if !reflect.DeepEqual(steady[i].Rooms, jittery[i].Rooms) {
			t.Fatalf("%s: occupancy differs between a steady and a jittery clock:\n%v\n%v",
				steady[i].Time.Format("15:04"), steady[i].Rooms, jittery[i].Rooms)
		}
	}
	busy := 0
	for _, m := range steady {
		if len(m.Rooms) > 20 {
			busy++
		}
	}
	if busy == 0 {
		t.Fatal("the morning never had more than 20 occupied rooms: the comparison proved nothing")
	}
}

func TestMinutesAreWholeAndConsecutive(t *testing.T) {
	all := collect(t, "02:00", func() time.Duration { return 7 * time.Second })
	if !all[0].Time.Equal(at(monday, "00:00")) {
		t.Fatalf("first minute is %v, want Monday 00:00", all[0].Time)
	}
	for i, m := range all {
		if m.Time.Second() != 0 || m.Time.Nanosecond() != 0 {
			t.Fatalf("%v is not a whole minute", m.Time)
		}
		if i > 0 && m.Time.Sub(all[i-1].Time) != time.Minute {
			t.Fatalf("gap between %v and %v", all[i-1].Time, m.Time)
		}
	}
}

// At night only guards are inside, so every occupant counted is a guard, and
// the patrols do show up in rooms.
func TestMinutesCountGuardsSeparately(t *testing.T) {
	all := collect(t, "04:00", func() time.Duration { return 30 * time.Second })
	visits := 0
	for _, m := range all {
		for room, c := range m.Rooms {
			if c.Guards != c.Occupants {
				t.Fatalf("%s %s: %d occupants but %d guards at night", m.Time.Format("15:04"), room, c.Occupants, c.Guards)
			}
			visits++
		}
	}
	if visits == 0 {
		t.Fatal("no guard was ever counted in a room during the night")
	}
}

func TestMinutesWithoutAfterReturnsTheNewest(t *testing.T) {
	s := newSim(t, sim.DefaultParameters(), at(monday, "12:00"))
	s.SetTime(at(monday, "09:30"))
	_, minutes := s.Minutes(time.Time{})
	if len(minutes) != 1 || !minutes[0].Time.Equal(at(monday, "09:30")) {
		t.Fatalf("got %d minutes, newest %v; want only 09:30", len(minutes), minutes)
	}
	_, later := s.Minutes(at(monday, "09:27"))
	if len(later) != 3 || !later[0].Time.Equal(at(monday, "09:28")) {
		t.Fatalf("after 09:27: %d minutes starting %v; want 09:28, 09:29, 09:30", len(later), later)
	}
}
