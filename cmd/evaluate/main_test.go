package main

import (
	"testing"
	"time"
)

func record(people int, guards *int, co2, temp float64) truthRecord {
	return truthRecord{Room: "level0/A109", Occupancy: people, Guards: guards, CO2: co2, Temp: temp}
}

func ptr(n int) *int { return &n }

// A guard alone in a cold room at night is a patrol, not an occupied room.
func TestPatrolsAreNotOccupied(t *testing.T) {
	var tl tally
	tl.add(record(1, ptr(1), 450, 18.0), time.Minute) // guard alone during the setback
	tl.add(record(1, ptr(0), 450, 21.0), time.Minute) // one member of staff
	tl.add(record(3, ptr(1), 450, 19.0), time.Minute) // staff working late, guard looks in
	tl.add(record(0, ptr(0), 450, 18.0), time.Minute) // empty
	if tl.occupied != 2 || tl.patrol != 1 {
		t.Fatalf("occupied %d, patrols %d; want 2 and 1", tl.occupied, tl.patrol)
	}
	if tl.comfortOK != 1 {
		t.Errorf("%d of the occupied records in the comfort band, want 1: the late workers at 19 °C still count as cold", tl.comfortOK)
	}
}

// Runs recorded before guards were counted have no guards field: everyone
// counts as an occupant, as they did then.
func TestOldRecordsWithoutGuards(t *testing.T) {
	var tl tally
	tl.add(record(1, nil, 450, 18.0), time.Minute)
	if tl.occupied != 1 || tl.patrol != 0 {
		t.Fatalf("occupied %d, patrols %d; want 1 and 0", tl.occupied, tl.patrol)
	}
}
