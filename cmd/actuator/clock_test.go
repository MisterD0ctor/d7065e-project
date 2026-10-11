package main

import (
	"testing"
	"time"
)

func TestTimekeeper(t *testing.T) {
	model := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	real := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	var k timekeeper

	if _, known, _ := k.at(real); known {
		t.Error("a device that never heard the time claims to know it")
	}

	// The signal sets the time, and between beats the time stands: the
	// building's clock moves in whole minutes.
	k.set(model, 60, real)
	if got, known, lost := k.at(real.Add(900 * time.Millisecond)); !known || lost || !got.Equal(model) {
		t.Errorf("0.9 s after a signal: %v known=%v lost=%v", got, known, lost)
	}
	// A paused model clock still beats: the same time again, and no drift.
	k.set(model, 60, real.Add(4*time.Second))
	if got, _, lost := k.at(real.Add(8 * time.Second)); lost || !got.Equal(model) {
		t.Errorf("paused clock: %v lost=%v", got, lost)
	}

	// The signal stops. After TimeLost the device keeps time itself, at the
	// last known factor: ten real seconds are ten model minutes at factor 60,
	// long enough for a command's five-minute TTL to run out.
	silent := real.Add(4*time.Second + 10*time.Second)
	got, known, lost := k.at(silent)
	if !known || !lost || !got.Equal(model.Add(10*time.Minute)) {
		t.Errorf("10 s without a signal: %v known=%v lost=%v, want 10:10 and lost", got, known, lost)
	}

	// The signal returns: its time is taken as it is, even if that is earlier.
	k.set(model.Add(time.Minute), 60, silent)
	if got, _, lost := k.at(silent.Add(time.Second)); lost || !got.Equal(model.Add(time.Minute)) {
		t.Errorf("after the signal came back: %v lost=%v", got, lost)
	}
}
