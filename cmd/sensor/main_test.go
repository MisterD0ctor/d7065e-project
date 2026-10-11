package main

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/faults"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

// place is the device's spot in BuildSim: one sensor value, or none.
type place struct {
	value string
	err   error
	reads int
}

func (p *place) SensorValue(context.Context, string) (buildsim.Sensor, error) {
	p.reads++
	return buildsim.Sensor{Value: p.value}, p.err
}

var (
	room = rooms.Key{Level: "level0", Name: "1570"}
	ten  = time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
)

func device(kind, fault string, at *place) *sensor {
	var fs []faults.Fault
	if fault != "" {
		fs, _ = faults.Parse(fault + ":" + room.String())
	}
	return &sensor{
		id: "dev-0002", kind: kind, room: room, bs: at, sample: time.Minute, runID: "run-1",
		inject: faults.NewInjector(fs), rng: rand.New(rand.NewPCG(1, 2)),
	}
}

// A reading is the building's value with the device's own error on top,
// rounded to the device's resolution and stamped with the building's time.
func TestReadingIsTheBuildingsValuePlusTheDevicesNoise(t *testing.T) {
	at := &place{value: "812.4"} // what the physical model wrote
	s := device(rooms.CO2, "", at)
	differs := false
	for min := range 200 {
		now := ten.Add(time.Duration(min) * time.Minute)
		if !s.due(now) {
			t.Fatalf("minute %d: not due a minute after the last reading", min)
		}
		o, ok := s.read(context.Background(), now)
		if !ok || o.Kind != rooms.CO2 || o.Room != "level0/1570" || o.SensorID != "dev-0002" || o.Unit != "ppm" || o.RunID != "run-1" {
			t.Fatalf("observation: %+v ok=%v", o, ok)
		}
		if o.ModelTime != now.Format(time.RFC3339) || o.Seq != now.Unix() {
			t.Fatalf("stamped %s seq %d at %s", o.ModelTime, o.Seq, now)
		}
		if o.Value != float64(int(o.Value)) || o.Value < 812-150 || o.Value > 812+150 {
			t.Fatalf("reading %v of a true 812.4 ppm", o.Value)
		}
		differs = differs || o.Value != 812
	}
	if !differs {
		t.Error("200 readings of a noisy sensor were all exactly the true value")
	}
	if s.due(ten.Add(199*time.Minute + 30*time.Second)) {
		t.Error("due again half a minute after a reading")
	}
	if !s.due(ten) {
		t.Error("not due after the clock was moved back")
	}
}

// Nothing to report: the physical model has not written yet, BuildSim is
// away, or the device is faulty and silent. In the first two cases the
// device tries again at the next time signal.
func TestNoReading(t *testing.T) {
	ctx := context.Background()
	empty := &place{}
	s := device(rooms.Temp, "", empty)
	if _, ok := s.read(ctx, ten); ok || !s.due(ten.Add(time.Second)) {
		t.Error("an empty value produced a reading, or stopped the device from trying again")
	}
	away := &place{err: errors.New("connection refused")}
	s = device(rooms.Temp, "", away)
	if _, ok := s.read(ctx, ten); ok || !s.due(ten.Add(time.Second)) {
		t.Error("an unreachable BuildSim produced a reading, or stopped the device from trying again")
	}
	s = device(rooms.Temp, "dropout", &place{value: "21.37"})
	if _, ok := s.read(ctx, ten); ok {
		t.Error("a sensor with a dropout fault reported")
	}
}

// A stuck sensor repeats its first reading whatever the building does: the
// fault is the device's, so it lives here and not in the physical model.
func TestStuckSensorIgnoresTheBuilding(t *testing.T) {
	at := &place{value: "21.37"}
	s := device(rooms.Temp, "stuck", at)
	first, _ := s.read(context.Background(), ten)
	at.value = "25.00"
	for min := 1; min <= 5; min++ {
		o, ok := s.read(context.Background(), ten.Add(time.Duration(min)*time.Minute))
		if !ok || o.Value != first.Value {
			t.Fatalf("minute %d: %v, want the stuck %v", min, o.Value, first.Value)
		}
	}
}

// The clock device passes on what its place in BuildSim shows.
func TestClockDevice(t *testing.T) {
	at := &place{value: `{"model_time":"2026-09-21T10:15:00Z","factor":60}`}
	s := device(rooms.Clock, "", at)
	m, raw, ok := s.readClock(context.Background())
	if !ok || m.ModelTime != "2026-09-21T10:15:00Z" || m.Factor != 60 || raw != at.value {
		t.Errorf("clock: %+v %q ok=%v", m, raw, ok)
	}
	for _, bad := range []string{"", "10:15", `{"model_time":"quarter past ten"}`} {
		at.value = bad
		if _, _, ok := s.readClock(context.Background()); ok {
			t.Errorf("clock value %q was passed on", bad)
		}
	}
	at.value = `{"model_time":"2026-09-21T10:15:00Z","factor":60}`
	if _, _, ok := device(rooms.Clock, "dropout", at).readClock(context.Background()); ok {
		t.Error("a clock with a dropout fault still told the time")
	}
}
