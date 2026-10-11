package clock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
)

// The value of the minutes sensor, in the shape occupancysim writes it.
const record = `{"clock":{"time":"2026-09-21T10:16:25Z","factor":120,"running":true},
"minutes":[{"time":"2026-09-21T10:14:00Z","rooms":{}},
           {"time":"2026-09-21T10:15:00Z","rooms":{"level0/1570":{"occupants":16}}},
           {"time":"2026-09-21T10:16:00Z","rooms":{"level0/1570":{"occupants":17,"guards":1}}}]}`

// sensors is BuildSim reduced to sensor values by id.
type sensors map[string]string

func (s sensors) SensorValue(_ context.Context, id string) (buildsim.Sensor, error) {
	v, ok := s[id]
	if !ok {
		return buildsim.Sensor{}, errors.New("sensor not found")
	}
	return buildsim.Sensor{ID: id, Value: v}, nil
}

func at(minute int) time.Time { return time.Date(2026, 9, 21, 10, minute, 0, 0, time.UTC) }

func TestMinutes(t *testing.T) {
	src := sensors{MinutesSensor: record}

	now, minutes, err := Minutes(context.Background(), src, at(14))
	if err != nil {
		t.Fatal(err)
	}
	if !now.Time.Equal(at(16).Add(25*time.Second)) || now.Factor != 120 || !now.Running {
		t.Errorf("clock: %+v", now)
	}
	if len(minutes) != 2 || !minutes[0].Time.Equal(at(15)) || !minutes[1].Time.Equal(at(16)) {
		t.Fatalf("minutes after 10:14: %+v", minutes)
	}
	if got := minutes[1].Rooms["level0/1570"]; got.Occupants != 17 || got.Guards != 1 {
		t.Errorf("10:16 in 1570: %+v", got)
	}
	if got := minutes[0].Rooms["level0/A109"]; got.Occupants != 0 {
		t.Errorf("a room left out of the record should read as empty: %+v", got)
	}

	// A new reader starts at the newest minute, not at the oldest in the record.
	if _, minutes, _ := Minutes(context.Background(), src, time.Time{}); len(minutes) != 1 || !minutes[0].Time.Equal(at(16)) {
		t.Errorf("first read: %+v", minutes)
	}
	// Up to date: nothing new.
	if _, minutes, _ := Minutes(context.Background(), src, at(16)); len(minutes) != 0 {
		t.Errorf("nothing after 10:16, got %+v", minutes)
	}
}

func TestMinutesWithoutAClock(t *testing.T) {
	for name, src := range map[string]sensors{
		"occupancysim has not registered its clock": {},
		"registered, nothing written":               {MinutesSensor: ""},
		"something else in the sensor":              {MinutesSensor: "12:00"},
		"a record without a time":                   {MinutesSensor: `{"clock":{},"minutes":[]}`},
	} {
		if _, _, err := Minutes(context.Background(), src, time.Time{}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
