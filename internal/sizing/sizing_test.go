package sizing

import (
	"math"
	"testing"
	"time"
)

// The sanity-check rows in architecture-options.md, "Ventilation sizing".
func TestDesignFlow(t *testing.T) {
	for _, tc := range []struct {
		area   float64
		design float64
	}{
		{18.5, 13.475}, // office: 7·1 + 0.35·18.5
		{80, 308},      // lecture: 7·40 + 0.35·80
	} {
		if got := For(tc.area).Design; math.Abs(got-tc.design) > 0.01 {
			t.Errorf("area %.1f: design %.2f l/s, want %.2f", tc.area, got, tc.design)
		}
	}
}

func TestFloorFollowsSchedule(t *testing.T) {
	r := For(80)
	weekdayNoon := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) // Monday
	night := time.Date(2026, 9, 14, 23, 0, 0, 0, time.UTC)
	saturday := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	if r.Floor(weekdayNoon) != r.Occupied {
		t.Error("weekday noon should use the occupied floor")
	}
	if r.Floor(night) != r.Empty || r.Floor(saturday) != r.Empty {
		t.Error("nights and weekends should use the unoccupied floor")
	}
}
