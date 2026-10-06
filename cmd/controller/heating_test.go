package main

import (
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

var office = rooms.Key{Level: "level0", Name: "A109"}

// at is a model time in the evaluation week: 21 Sep 2026 is a Monday.
func at(day, hour, minute int) time.Time {
	return time.Date(2026, 9, day, hour, minute, 0, 0, time.UTC)
}

func TestWarmUpTime(t *testing.T) {
	// 3 K below the setpoint at 1.5 K/h is 2 h, plus the 15 min margin.
	if got := warmUp(18); got != 2*time.Hour+15*time.Minute {
		t.Errorf("from 18 °C: %v, want 2h15m", got)
	}
	if got := warmUp(21.4); got != 0 {
		t.Errorf("a warm room needs no warm-up, got %v", got)
	}
}

// T-34: the setback ends early enough for a cold room to be warm at 07:00,
// and not before.
func TestOptimalStart(t *testing.T) {
	var o optimalStart
	steps := []struct {
		name     string
		when     time.Time
		measured float64
		want     float64
	}{
		{"Monday evening: setback", at(21, 20, 0), 20.5, SetbackSetpoint},
		{"03:00, 2h15 needed: too early", at(22, 3, 0), 18.0, SetbackSetpoint},
		{"04:44: one minute too early", at(22, 4, 44), 18.0, SetbackSetpoint},
		{"04:45: start", at(22, 4, 45), 18.0, ComfortSetpoint},
		// The room warms faster than assumed: at 05:30 the formula alone
		// would say "not yet" (19.5 °C needs 1h15, due 05:45). The pre-heat
		// must not flip back.
		{"05:30, ahead of schedule: keep heating", at(22, 5, 30), 19.5, ComfortSetpoint},
		{"06:59: still pre-heating", at(22, 6, 59), 20.8, ComfortSetpoint},
		{"07:00: occupied hours", at(22, 7, 0), 20.9, ComfortSetpoint},
		{"17:59: occupied hours", at(22, 17, 59), 21.0, ComfortSetpoint},
		{"18:00: setback again", at(22, 18, 0), 21.0, SetbackSetpoint},
	}
	for _, s := range steps {
		if got, why := o.Setpoint(office, s.when, s.measured); got != s.want {
			t.Errorf("%s: %.0f °C (%s), want %.0f °C", s.name, got, why, s.want)
		}
	}
}

func TestOptimalStartSkipsTheWeekend(t *testing.T) {
	var o optimalStart
	// Saturday 26 Sep, cold room: Monday is two days away.
	if got, _ := o.Setpoint(office, at(26, 4, 45), 18); got != SetbackSetpoint {
		t.Errorf("Saturday morning: %.0f °C, want the setback", got)
	}
	if got, _ := o.Setpoint(office, at(28, 4, 45), 18); got != ComfortSetpoint {
		t.Errorf("Monday 04:45: %.0f °C, want pre-heat", got)
	}
}

// A room that is already warm has nothing to pre-heat.
func TestWarmRoomStaysInSetback(t *testing.T) {
	var o optimalStart
	if got, _ := o.Setpoint(office, at(22, 6, 30), 21.3); got != SetbackSetpoint {
		t.Errorf("warm room at 06:30: %.0f °C, want the setback until 07:00", got)
	}
}

// Rooms are independent, and moving the model clock back (a new experiment)
// forgets a pre-heat that began "later".
func TestOptimalStartPerRoomAndClockReset(t *testing.T) {
	var o optimalStart
	other := rooms.Key{Level: "level0", Name: "1570"}
	o.Setpoint(office, at(22, 5, 0), 18) // starts pre-heating the office
	if got, _ := o.Setpoint(other, at(22, 5, 0), 20.9); got != SetbackSetpoint {
		t.Errorf("another, warm room was pre-heated too: %.0f °C", got)
	}
	// Clock moved back to 02:00 the same night, room cold again.
	if got, _ := o.Setpoint(office, at(22, 2, 0), 18); got != SetbackSetpoint {
		t.Errorf("after the clock moved back: %.0f °C, want the setback", got)
	}
}
