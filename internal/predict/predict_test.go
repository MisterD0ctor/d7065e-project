package predict

import (
	"math/rand/v2"
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

// minuteReadings is one room's counter over five weekdays, a reading per
// minute, with the counter's noise: one person too many in 5 % of readings
// and one too few in 5 % (never below zero). people gives the true count.
func minuteReadings(room string, people func(day int, t time.Time) float64) []Observation {
	rng := rand.New(rand.NewPCG(7, 7))
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	var obs []Observation
	for d := range 5 {
		for m := 0; m < 24*60; m++ {
			t := monday.AddDate(0, 0, d).Add(time.Duration(m) * time.Minute)
			v := people(d, t)
			switch p := rng.Float64(); {
			case p < 0.05 && v > 0:
				v--
			case p >= 0.95:
				v++
			}
			obs = append(obs, Observation{Room: room, Value: v, ModelTime: t.Format(time.RFC3339)})
		}
	}
	return obs
}

// The flaw the second evaluation exposed: an empty room's counter says "1"
// now and then, and a model that takes the highest reading per slot ends up
// expecting someone almost all the time. With a reading per minute the first
// version of Train failed this test in 86 % of the slots.
func TestTrainIgnoresCounterNoise(t *testing.T) {
	p, err := Train("noise", nil, minuteReadings("level0/A105", func(int, time.Time) float64 { return 0 }))
	if err != nil {
		t.Fatal(err)
	}
	rp := p.Rooms["level0/A105"]
	for s, high := range rp.High {
		if high != 0 {
			t.Fatalf("slot %02d:%02d of a room nobody ever entered: the model expects %v people", s/4, s%4*15, high)
		}
	}
	if p.SlotQuantile != SlotQuantile {
		t.Errorf("the model doesn't record how it was trained: slot_quantile %v", p.SlotQuantile)
	}
}

// A gathering that lasts a third of a slot still registers at its full size;
// a count that shows for two minutes does not.
func TestTrainKeepsGatheringsDropsBlips(t *testing.T) {
	p, _ := Train("gathering", nil, minuteReadings("level0/1570", func(day int, t time.Time) float64 {
		switch {
		case t.Hour() == 9 && t.Minute() >= 45 && t.Minute() < 50: // 5 of 15 minutes, every day
			return 20
		case t.Hour() == 14 && t.Minute() < 2: // 2 of 15 minutes, every day
			return 20
		}
		return 0
	}))
	rp := p.Rooms["level0/1570"]
	if got := rp.High[slot(time.Date(2026, 9, 21, 9, 45, 0, 0, time.UTC))]; got < 19 {
		t.Errorf("20 people for five minutes at 09:45: the model expects %v", got)
	}
	if got := rp.High[slot(time.Date(2026, 9, 21, 14, 0, 0, 0, time.UTC))]; got != 0 {
		t.Errorf("20 people for two minutes at 14:00: the model expects %v, want 0", got)
	}
}
