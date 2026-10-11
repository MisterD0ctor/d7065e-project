// Package clock reads the simulation clock from BuildSim (IF-1): the model
// time, and who was in each room at every whole model minute. occupancysim
// owns both and publishes them into BuildSim as the values of two text
// sensors on a piece of equipment of its own; nothing of ours calls
// occupancysim.
package clock

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
)

// The BuildSim sensors occupancysim keeps (external/occupancysim,
// internal/buildsim/clock.go).
const (
	TimeSensor    = "sim-clock-time"    // the model time, RFC 3339
	MinutesSensor = "sim-clock-minutes" // the clock and the last minutes' occupancy, JSON
)

type Reading struct {
	Time    time.Time
	Factor  float64 // model seconds per real second
	Running bool
	Weekend bool
}

// clockJSON is occupancysim's clock as it appears in its documents.
type clockJSON struct {
	Time    string  `json:"time"`
	Factor  float64 `json:"factor"`
	Running bool    `json:"running"`
	Weekend bool    `json:"weekend"`
}

func (c clockJSON) reading() (Reading, error) {
	t, err := time.Parse(time.RFC3339, c.Time)
	if err != nil {
		return Reading{}, fmt.Errorf("clock: %w", err)
	}
	return Reading{Time: t, Factor: c.Factor, Running: c.Running, Weekend: c.Weekend}, nil
}

// RoomCount is how many people are in a room. Guards are counted in
// Occupants and again in Guards: a guard on patrol breathes like anyone else,
// but a room with only a guard in it is not in use.
type RoomCount struct {
	Occupants int `json:"occupants"`
	Guards    int `json:"guards"`
}

// Minute is who was in each room at one whole model minute, keyed
// "<level>/<room>". Rooms with nobody in them are left out.
type Minute struct {
	Time  time.Time            `json:"time"`
	Rooms map[string]RoomCount `json:"rooms"`
}

// Source is where a sensor's value is read: BuildSim.
type Source interface {
	SensorValue(ctx context.Context, sensorID string) (buildsim.Sensor, error)
}

// Minutes returns the clock and the whole-minute occupancy recorded after
// the given model time, oldest first. A zero time returns only the newest
// minute, which is where a reader starts.
//
// Each entry is the state exactly on its minute and depends only on the
// day's plan, so every run of the same date sees the same sequence (NFR-7).
// BuildSim's own occupancy collection could not give that: it carries no
// time and is refreshed on a wall-clock schedule. The record holds the last
// ten minutes, so a reader that polls a few times per real second misses
// none; one that falls further behind sees a gap between its last minute and
// the first one returned.
func Minutes(ctx context.Context, src Source, after time.Time) (Reading, []Minute, error) {
	s, err := src.SensorValue(ctx, MinutesSensor)
	if err != nil {
		return Reading{}, nil, fmt.Errorf("simulation clock in BuildSim: %w", err)
	}
	if s.Value == "" {
		return Reading{}, nil, fmt.Errorf("simulation clock in BuildSim: nothing written yet")
	}
	var doc struct {
		Clock   clockJSON `json:"clock"`
		Minutes []Minute  `json:"minutes"`
	}
	if err := json.Unmarshal([]byte(s.Value), &doc); err != nil {
		return Reading{}, nil, fmt.Errorf("simulation clock in BuildSim: %w", err)
	}
	r, err := doc.Clock.reading()
	if err != nil {
		return Reading{}, nil, err
	}
	if after.IsZero() && len(doc.Minutes) > 0 {
		return r, doc.Minutes[len(doc.Minutes)-1:], nil
	}
	first := len(doc.Minutes)
	for i, m := range doc.Minutes {
		if m.Time.After(after) {
			first = i
			break
		}
	}
	return r, doc.Minutes[first:], nil
}
