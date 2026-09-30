package main

import (
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

var lecture = sizing.ForCapacity(80, 40) // design 308 l/s, τ ≈ 12 min

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
	if got := airflow(Constant, lecture, cleanAir, 0); got != lecture.Design {
		t.Errorf("constant: %v, want design %v", got, lecture.Design)
	}
	if got := airflow(Reactive, lecture, cleanAir, 30); got != lecture.Empty {
		t.Errorf("reactive ignores people: %v, want the lowest flow %v", got, lecture.Empty)
	}
	// 30 people expected, air still clean: predictive ventilates ahead.
	want := sizing.PerPersonLs*30 + sizing.PerAreaLs*80
	if got := airflow(Predictive, lecture, cleanAir, 30); got != want {
		t.Errorf("predictive with 30 expected: %v, want %v", got, want)
	}
	// Stale air with nobody expected: predictive still reacts to CO₂.
	if got := airflow(Predictive, lecture, 1000, 0); got != lecture.Max {
		t.Errorf("predictive at 1000 ppm: %v, want max %v", got, lecture.Max)
	}
	// More people than the room is sized for: capped at the maximum.
	if got := airflow(Oracle, lecture, cleanAir, 100); got != lecture.Max {
		t.Errorf("oracle with 100 expected: %v, want max %v", got, lecture.Max)
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
