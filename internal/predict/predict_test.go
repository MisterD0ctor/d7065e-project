package predict

import (
	"testing"
	"time"
)

// A fika room: 20 people from 09:45 to 10:00 on four of five weekdays.
func fikaWeek() []Observation {
	var obs []Observation
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	for d := range 5 {
		day := monday.AddDate(0, 0, d)
		for m := 0; m < 24*60; m++ {
			t := day.Add(time.Duration(m) * time.Minute)
			v := 0.0
			if d != 2 && t.Hour() == 9 && t.Minute() >= 45 {
				v = 20
			}
			obs = append(obs, Observation{Room: "level0/1570", Value: v, ModelTime: t.Format(time.RFC3339)})
		}
	}
	// Weekend readings must not count.
	sat := monday.AddDate(0, 0, 5).Add(9*time.Hour + 50*time.Minute)
	obs = append(obs, Observation{Room: "level0/1570", Value: 99, ModelTime: sat.Format(time.RFC3339)})
	return obs
}

func TestTrainLearnsTheBreak(t *testing.T) {
	p, err := Train("test", []string{"r1"}, fikaWeek())
	if err != nil {
		t.Fatal(err)
	}
	rp := p.Rooms["level0/1570"]
	s := slot(time.Date(2026, 9, 21, 9, 45, 0, 0, time.UTC))
	if rp.High[s] != 20 {
		t.Errorf("09:45 slot high = %v, want 20 (4 of 5 days busy, 80 %% quantile)", rp.High[s])
	}
	if rp.Mean[s] != 16 {
		t.Errorf("09:45 slot mean = %v, want 16", rp.Mean[s])
	}
	if p.TrainedOn.Weekdays != 5 {
		t.Errorf("trained on %d weekdays, want 5 (the Saturday is ignored)", p.TrainedOn.Weekdays)
	}
	if rp.High[slot(time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC))] != 0 {
		t.Error("03:00 should be empty")
	}
}

// T-06 in miniature: ahead of the break the profile foresees it, where a
// naive "same as now" prediction sees an empty room.
func TestExpectedLooksAhead(t *testing.T) {
	p, _ := Train("test", nil, fikaWeek())
	nextMonday := time.Date(2026, 9, 21, 9, 30, 0, 0, time.UTC)
	if got := p.Expected("level0/1570", nextMonday, 20*time.Minute); got != 20 {
		t.Errorf("at 09:30 with a 20 min lead: %v, want 20", got)
	}
	if got := p.Expected("level0/1570", nextMonday, 5*time.Minute); got != 0 {
		t.Errorf("at 09:30 with a 5 min lead: %v, want 0 (break starts 09:45)", got)
	}
	saturday := time.Date(2026, 9, 19, 9, 30, 0, 0, time.UTC)
	if got := p.Expected("level0/1570", saturday, 30*time.Minute); got != 0 {
		t.Errorf("Saturday: %v, want 0", got)
	}
	if got := p.Expected("level0/unknown", nextMonday, time.Hour); got != 0 {
		t.Errorf("unknown room: %v, want 0", got)
	}
}

func TestTrainNeedsData(t *testing.T) {
	if _, err := Train("x", nil, nil); err == nil {
		t.Error("training on nothing succeeded")
	}
}
