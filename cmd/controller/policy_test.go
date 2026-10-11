package main

import (
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

var lecture = sizing.ForCapacity(80, 40) // design 308 l/s, τ ≈ 12 min

var noon = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) // a Monday

func TestLeadTimeFollowsTheRoom(t *testing.T) {
	if got := leadTime(lecture); got < 11*time.Minute || got > 13*time.Minute {
		t.Errorf("lecture room lead %v, want ≈ 12 min", got)
	}
	office := sizing.ForCapacity(18.5, 1) // τ ≈ 62 min
	if got := leadTime(office); got != 30*time.Minute {
		t.Errorf("office lead %v, want the 30 min cap", got)
	}
}

func TestPoliciesDiffer(t *testing.T) {
	const cleanAir = 450.0
	night := time.Date(2026, 9, 21, 23, 0, 0, 0, time.UTC)
	sunday := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if airflow(Constant, lecture, 1500, 40, night) != lecture.Empty || airflow(Constant, lecture, cleanAir, 0, sunday) != lecture.Empty {
		t.Error("constant should run at the unoccupied minimum at night and at weekends, whatever the air is like")
	}
	if got := airflow(Constant, lecture, cleanAir, 0, noon); got != lecture.Design {
		t.Errorf("constant: %v, want design %v", got, lecture.Design)
	}
	if got := airflow(Reactive, lecture, cleanAir, 30, noon); got != lecture.Empty {
		t.Errorf("reactive ignores people: %v, want the lowest flow %v", got, lecture.Empty)
	}
	// 30 people expected, air still clean: predictive ventilates ahead.
	want := sizing.PerPersonLs*30 + sizing.PerAreaLs*80
	if got := airflow(Predictive, lecture, cleanAir, 30, noon); got != want {
		t.Errorf("predictive with 30 expected: %v, want %v", got, want)
	}
	// Stale air with nobody expected: predictive still reacts to CO₂.
	if got := airflow(Predictive, lecture, 1000, 0, noon); got != lecture.Max {
		t.Errorf("predictive at 1000 ppm: %v, want max %v", got, lecture.Max)
	}
	// More people than the room is sized for: capped at the maximum.
	if got := airflow(Oracle, lecture, cleanAir, 100, noon); got != lecture.Max {
		t.Errorf("oracle with 100 expected: %v, want max %v", got, lecture.Max)
	}
}

// Less than half a person expected is an empty room: no people-driven flow.
func TestTraceOfOccupancyIsNoDemand(t *testing.T) {
	if got := peopleFlow(lecture, 0.3); got != 0 {
		t.Errorf("0.3 people expected: %v l/s, want 0", got)
	}
	if got, want := peopleFlow(lecture, 1), sizing.PerPersonLs+sizing.PerAreaLs*80; got != want {
		t.Errorf("1 person: %v l/s, want %v", got, want)
	}
}

func TestOracleLooksAhead(t *testing.T) {
	nine := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	o := &oracleForecast{byRoom: map[string]map[int64]int{
		"level0/1570": {nine.Add(20*time.Minute).Unix() / 60: 18},
	}}
	if got, ok := o.Expected("level0/1570", nine, 25*time.Minute); !ok || got != 18 {
		t.Errorf("25 min ahead: %v %v, want 18", got, ok)
	}
	if got, _ := o.Expected("level0/1570", nine, 10*time.Minute); got != 0 {
		t.Errorf("10 min ahead: %v, want 0", got)
	}
	if _, ok := o.Expected("level0/nope", nine, time.Hour); ok {
		t.Error("unknown room should report ok=false")
	}
}
