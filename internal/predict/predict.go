// Package predict is the data-driven part of the controller (FR-4): a
// per-room occupancy profile by time of day on weekdays, learned from the
// occupancy *sensor* readings in storage, never from the truth.
//
// The profile keeps, per 15-minute slot, the mean count and a high quantile.
// The controller ventilates for the high quantile: running a fan a little
// early costs less energy than catching up on stale air (proposal §1).
package predict

import (
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	SlotMinutes = 15
	SlotsPerDay = 24 * 60 / SlotMinutes
	// Quantile is the share of observed days the prediction must cover.
	Quantile = 0.8
)

// Observation is one occupancy reading, as storage returns it.
type Observation struct {
	Room      string  `json:"room"`
	Value     float64 `json:"value"`
	ModelTime string  `json:"model_time"`
}

// RoomProfile holds, per slot of a weekday, the mean and the Quantile of the
// highest count seen in that slot on each training day.
type RoomProfile struct {
	Mean [SlotsPerDay]float64 `json:"mean"`
	High [SlotsPerDay]float64 `json:"high"`
	Days [SlotsPerDay]int     `json:"days"` // training days with data in the slot
}

type Profile struct {
	Name      string                 `json:"name"`
	SlotMin   int                    `json:"slot_min"`
	Quantile  float64                `json:"quantile"`
	TrainedOn TrainedOn              `json:"trained_on"`
	Rooms     map[string]RoomProfile `json:"rooms"`
}

type TrainedOn struct {
	RunIDs   []string `json:"run_ids"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Weekdays int      `json:"weekdays"`
	Readings int      `json:"readings"`
}

func slot(t time.Time) int { return (t.Hour()*60 + t.Minute()) / SlotMinutes }

func weekday(t time.Time) bool {
	wd := t.Weekday()
	return wd != time.Saturday && wd != time.Sunday
}

// Train builds a profile from occupancy readings. Weekend readings are
// ignored: the building is empty then, and the profile is for weekdays.
func Train(name string, runIDs []string, obs []Observation) (Profile, error) {
	// room → slot → day → peak count in that slot on that day
	peaks := map[string]map[int]map[string]float64{}
	days := map[string]bool{}
	var from, to time.Time
	n := 0
	for _, o := range obs {
		t, err := time.Parse(time.RFC3339, o.ModelTime)
		if err != nil || !weekday(t) {
			continue
		}
		day := t.Format("2006-01-02")
		days[day] = true
		if from.IsZero() || t.Before(from) {
			from = t
		}
		if t.After(to) {
			to = t
		}
		if peaks[o.Room] == nil {
			peaks[o.Room] = map[int]map[string]float64{}
		}
		s := slot(t)
		if peaks[o.Room][s] == nil {
			peaks[o.Room][s] = map[string]float64{}
		}
		if v, seen := peaks[o.Room][s][day]; !seen || o.Value > v {
			peaks[o.Room][s][day] = o.Value
		}
		n++
	}
	if n == 0 {
		return Profile{}, fmt.Errorf("no weekday occupancy readings to train on")
	}

	p := Profile{
		Name: name, SlotMin: SlotMinutes, Quantile: Quantile,
		TrainedOn: TrainedOn{
			RunIDs: runIDs, From: from.Format(time.RFC3339), To: to.Format(time.RFC3339),
			Weekdays: len(days), Readings: n,
		},
		Rooms: map[string]RoomProfile{},
	}
	for room, slots := range peaks {
		var rp RoomProfile
		for s, byDay := range slots {
			var vals []float64
			var sum float64
			for _, v := range byDay {
				vals = append(vals, v)
				sum += v
			}
			// Days without a reading in this slot count as empty: a slot seen
			// on one day of five is mostly empty, not always busy.
			for len(vals) < len(days) {
				vals = append(vals, 0)
			}
			rp.Mean[s] = sum / float64(len(vals))
			rp.High[s] = quantile(vals, Quantile)
			rp.Days[s] = len(byDay)
		}
		p.Rooms[room] = rp
	}
	return p, nil
}

func quantile(vals []float64, q float64) float64 {
	sort.Float64s(vals)
	i := int(math.Ceil(q*float64(len(vals)))) - 1
	return vals[max(0, min(i, len(vals)-1))]
}

// Expected returns the highest count the profile expects in room between t
// and t+lead (the High quantile of every slot the window touches). It is 0
// on weekends and for rooms the profile doesn't know.
func (p Profile) Expected(room string, t time.Time, lead time.Duration) float64 {
	rp, ok := p.Rooms[room]
	if !ok {
		return 0
	}
	var peak float64
	for u := t; !u.After(t.Add(lead)); u = u.Add(SlotMinutes * time.Minute) {
		if weekday(u) {
			peak = max(peak, rp.High[slot(u)])
		}
	}
	if end := t.Add(lead); weekday(end) {
		peak = max(peak, rp.High[slot(end)])
	}
	return peak
}
