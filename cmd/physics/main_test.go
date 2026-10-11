package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
)

// fakeBuilding is BuildSim as physics sees it: equipment with sensor values
// and actuator states, nothing else.
type fakeBuilding struct {
	mu        sync.Mutex
	equipment []buildsim.Equipment
	values    map[string]string // sensor id → value; a sensor exists once it is here
	order     []string          // sensor ids in the order they were written
	created   int
}

func (b *fakeBuilding) SensorValue(_ context.Context, id string) (buildsim.Sensor, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.values[id]
	if !ok {
		return buildsim.Sensor{}, &buildsim.StatusError{Code: 404}
	}
	return buildsim.Sensor{ID: id, Value: v}, nil
}

func (b *fakeBuilding) SetSensorValue(_ context.Context, id, value string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.values[id]; !ok {
		return &buildsim.StatusError{Code: 404}
	}
	b.values[id] = value
	b.order = append(b.order, id)
	return nil
}

func (b *fakeBuilding) Equipment(context.Context, string) ([]buildsim.Equipment, error) {
	return b.equipment, nil
}

func (b *fakeBuilding) BulkCreate(_ context.Context, eq []buildsim.Equipment) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.created++
	for _, e := range eq {
		for _, s := range e.Sensors {
			if _, ok := b.values[s.ID]; !ok {
				b.values[s.ID] = ""
			}
		}
	}
	return nil
}

// install adds the equipment the registry would have created, and
// occupancysim's clock.
func install(devices ...buildsim.Equipment) *fakeBuilding {
	b := &fakeBuilding{values: map[string]string{clock.MinutesSensor: ""}, equipment: devices}
	for _, e := range devices {
		for _, s := range e.Sensors {
			b.values[s.ID] = ""
		}
	}
	return b
}

func sensorIn(room, id, kind string) buildsim.Equipment {
	return buildsim.Equipment{ID: id, Type: kind, Level: "level0", Room: room, Sensors: []buildsim.Sensor{{ID: id + "-reading"}}}
}

func actuatorIn(room, id, kind, state string) buildsim.Equipment {
	return buildsim.Equipment{ID: id, Type: kind, Level: "level0", Room: room, Actuators: []buildsim.Actuator{{ID: id + "-state", State: state}}}
}

func timeOf(minute int) time.Duration { return time.Duration(minute) * time.Minute }

// clockSays sets what occupancysim has published: the last minutes after 09:00
// with that many people in the fika room, at factor 60.
func (b *fakeBuilding) clockSays(people ...int) {
	doc := `{"clock":{"time":"` + msg.FormatTime(nine.Add(timeOf(len(people)-1))) + `","factor":60,"running":true},"minutes":[`
	for i, n := range people {
		if i > 0 {
			doc += ","
		}
		doc += fmt.Sprintf(`{"time":%q,"rooms":{%q:{"occupants":%d}}}`, msg.FormatTime(nine.Add(timeOf(i))), fika.String(), n)
	}
	b.mu.Lock()
	b.values[clock.MinutesSensor] = doc + "]}"
	b.mu.Unlock()
}

// The quickstart loop: physics reads the clock, the people and the actuators
// from BuildSim, and writes the value at every sensor back, the clock last.
func TestPhysicsReadsAndWritesOnlyBuildSim(t *testing.T) {
	b := install(
		sensorIn("1570", "co2-0002", "co2_sensor"),
		sensorIn("1570", "temp-0002", "temperature_sensor"),
		sensorIn("1570", "occ-0002", "occupancy_counter"),
		sensorIn("1543", "co2-0001", "co2_sensor"), // a room physics does not simulate
		sensorIn("1570", "clock-0001", "bms_controller"),
		actuatorIn("1570", "damper-0002", "ventilation_fan", "150.0"),
		actuatorIn("1570", "heat-0002", "radiator", "21.0"),
	)
	s := &sim{bs: b, w: testWorld()}
	ctx := context.Background()

	s.tick(ctx) // occupancysim has written nothing yet
	if len(b.order) != 0 {
		t.Fatalf("wrote %v without a clock", b.order)
	}

	b.clockSays(20) // 09:00, the starting point
	s.tick(ctx)
	b.clockSays(20, 20, 12) // physics missed 09:01: both minutes are in the record
	s.tick(ctx)

	if got := b.values["occ-0002-reading"]; got != "12" {
		t.Errorf("head-count at the counter: %q, want the true 12", got)
	}
	var co2 float64
	fmt.Sscan(b.values["co2-0002-reading"], &co2)
	if want := s.w.models[fika].CO2; co2 < want-0.06 || co2 > want+0.06 || co2 <= 420 {
		t.Errorf("CO₂ at the sensor: %q, the room is at %.2f", b.values["co2-0002-reading"], want)
	}
	if b.values["temp-0002-reading"] == "" {
		t.Error("no temperature written")
	}
	if got := b.values["co2-0001-reading"]; got != "" {
		t.Errorf("a sensor in a room that isn't simulated got %q", got)
	}

	// The clock shows the minute whose values are in BuildSim, and is written last.
	var shown msg.ModelTime
	if err := json.Unmarshal([]byte(b.values["clock-0001-reading"]), &shown); err != nil || shown.ModelTime != "2026-09-21T09:02:00Z" || shown.Factor != 60 {
		t.Errorf("clock value %q: %v", b.values["clock-0001-reading"], err)
	}
	if last := b.order[len(b.order)-1]; last != "clock-0001-reading" {
		t.Errorf("the last write of the minute was %s, want the clock: %v", last, b.order)
	}

	// The record for the evaluation: every minute physics computed, with the
	// airflow it used.
	var rec msg.Record
	if err := json.Unmarshal([]byte(b.values[msg.RecordSensor]), &rec); err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(rec.Minutes) != 3 || rec.Minutes[1].ModelTime != "2026-09-21T09:01:00Z" || rec.Minutes[2].ModelTime != "2026-09-21T09:02:00Z" {
		t.Fatalf("record minutes: %+v", rec.Minutes)
	}
	room := rec.Minutes[2].Rooms[0]
	if room.Room != fika.String() || room.Occupancy != 12 || room.Airflow != 150 || room.Setpoint != 21 || room.RunID != "" {
		t.Errorf("record of 1570 at 09:02: %+v", room)
	}

	// Nothing new on the clock: nothing is written.
	writes := len(b.order)
	s.tick(ctx)
	if len(b.order) != writes {
		t.Errorf("a tick without a new minute wrote %v", b.order[writes:])
	}
}

// BuildSim restarts blank: the device sensors are the registry's to bring
// back, the record is physics' own.
func TestPhysicsSurvivesABlankBuildSim(t *testing.T) {
	b := install(sensorIn("1570", "co2-0002", "co2_sensor"))
	s := &sim{bs: b, w: testWorld()}
	ctx := context.Background()
	b.clockSays(5)
	s.tick(ctx)
	if b.created != 1 {
		t.Fatalf("record equipment registered %d times", b.created)
	}

	b.mu.Lock()
	b.values = map[string]string{clock.MinutesSensor: ""} // blank, and occupancysim's clock is back
	b.mu.Unlock()
	b.clockSays(5, 5)
	s.tick(ctx) // every write finds nothing
	b.clockSays(5, 5, 5)
	s.tick(ctx)
	if b.created != 2 || !strings.Contains(b.values[msg.RecordSensor], "09:02:00Z") {
		t.Errorf("after the restart: registered %d times, record %.80q", b.created, b.values[msg.RecordSensor])
	}
}

// The record never grows past its few minutes, and a clock moved back starts
// it over.
func TestRecordIsShortAndRestartsWithTheClock(t *testing.T) {
	b := install()
	s := &sim{bs: b, w: testWorld()}
	ctx := context.Background()
	for i := 1; i <= 9; i++ {
		b.clockSays(make([]int, i)...)
		s.tick(ctx)
	}
	if len(s.record) != msg.RecordMinutes || s.record[len(s.record)-1].ModelTime != "2026-09-21T09:08:00Z" {
		t.Fatalf("record: %d minutes, newest %s", len(s.record), s.record[len(s.record)-1].ModelTime)
	}
	b.clockSays(0) // a new experiment: back to 09:00
	s.tick(ctx)    // notices
	s.tick(ctx)    // starts there
	if len(s.record) != 1 || s.record[0].ModelTime != "2026-09-21T09:00:00Z" {
		t.Errorf("record after the clock moved back: %+v", s.record)
	}
}
