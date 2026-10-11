// Command physics is the physical model (D-1, FR-1): CO₂ and temperature in
// the simulated rooms. BuildSim is the only thing it talks to, as in the
// course's lab quickstart.
//
// It reads, from BuildSim: the simulation clock and the people in each room
// at each whole model minute, which occupancysim publishes there (IF-1), and
// what every damper and radiator valve has reached (IF-9).
//
// It writes, to BuildSim: the value at every sensor (IF-3), which is CO₂,
// temperature and head-count as they truly are, and the time at every clock;
// and the full state of the rooms, for the evaluation (IF-5).
//
// What it writes is the truth. A device's measurement error is the sensor
// service's business: it reads its value here and reports its own.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/site"
)

const (
	// Tick is how often physics looks in BuildSim for new minutes, in real
	// time. A model minute lasts 0.5 s at factor 120; the clock's record
	// holds ten of them, so nothing is missed at this rate.
	Tick = 250 * time.Millisecond
	// Writers is how many sensor values are written to BuildSim at a time.
	// A minute's values have to be there well before the next minute.
	Writers = 16
)

// building is the part of BuildSim physics uses.
type building interface {
	clock.Source
	Equipment(ctx context.Context, level string) ([]buildsim.Equipment, error)
	BulkCreate(ctx context.Context, eq []buildsim.Equipment) error
	SetSensorValue(ctx context.Context, sensorID, value string) error
}

type sim struct {
	bs building
	w  *world

	record     []msg.RecordMinute // the rooms' full state, last minutes
	registered bool               // the record's equipment is in BuildSim
	quiet      int                // ticks in a row without a clock, to log it once
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	keys, err := rooms.ParseList(env.Must("ROOMS"))
	if err != nil {
		log.Fatal(err)
	}
	bs := buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090"))
	sizes, err := site.Sizes(ctx, bs, keys)
	if err != nil {
		log.Fatal(err)
	}
	s := &sim{bs: bs, w: newWorld(keys, sizes)}
	log.Printf("simulating %d rooms", len(keys))

	tick := time.NewTicker(Tick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.tick(ctx)
		}
	}
}

// tick reads the minutes recorded since the last one, advances the rooms
// through each of them, and writes the result back.
func (s *sim) tick(ctx context.Context) {
	now, minutes, err := clock.Minutes(ctx, s.bs, s.w.last)
	if err != nil {
		if s.quiet++; s.quiet == 1 || s.quiet%240 == 0 {
			log.Printf("%v (holding state)", err)
		}
		return
	}
	s.quiet = 0
	if !s.w.last.IsZero() && now.Time.Before(s.w.last) {
		// The clock was moved back, e.g. for a new experiment. Forget where
		// we were; the next tick starts from the record's newest minute.
		log.Printf("model clock moved back from %s to %s; restarting there", msg.FormatTime(s.w.last), msg.FormatTime(now.Time))
		s.w.last, s.record = time.Time{}, nil
		return
	}
	if len(minutes) == 0 {
		return
	}
	// The equipment is read once per tick: the actuators' states are held
	// for the minutes processed in it, at most a few unless physics is
	// catching up.
	equipment := s.equipment(ctx)
	s.readActuators(equipment)
	for _, m := range minutes {
		if gap := m.Time.Sub(s.w.last); !s.w.last.IsZero() && gap > time.Minute {
			log.Printf("fell behind the clock's record: stepping %v in one go", gap)
		}
		s.w.advance(m)
		s.remember()
	}
	s.write(ctx, equipment, now.Factor)
}

// equipment lists what is installed on the simulated rooms' floors. On an
// error a floor is left out this round: its actuators keep their last known
// state and its sensors get no new value.
func (s *sim) equipment(ctx context.Context) []buildsim.Equipment {
	levels := map[string]bool{}
	var all []buildsim.Equipment
	for _, k := range s.w.keys {
		if levels[k.Level] {
			continue
		}
		levels[k.Level] = true
		eq, err := s.bs.Equipment(ctx, k.Level)
		if err != nil {
			log.Printf("equipment %s: %v", k.Level, err)
			continue
		}
		all = append(all, eq...)
	}
	return all
}

// readActuators takes over what each room's damper and radiator valve have
// reached. The devices are found by equipment type and room: physics doesn't
// need the registry to know which device is which.
func (s *sim) readActuators(equipment []buildsim.Equipment) {
	airflow := rooms.Actuators[rooms.Damper].EquipmentType
	radiator := rooms.Actuators[rooms.Heating].EquipmentType
	for _, e := range equipment {
		k := rooms.Key{Level: e.Level, Name: e.Room}
		a, ok := s.w.act[k]
		if !ok || len(e.Actuators) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(e.Actuators[0].State, 64)
		if err != nil {
			continue // not reported yet
		}
		switch e.Type {
		case airflow:
			a.AirflowLs = v
		case radiator:
			a.Setpoint = v
		}
		s.w.act[k] = a
	}
}

// remember adds the rooms' state at the current minute to the record.
func (s *sim) remember() {
	m := msg.RecordMinute{ModelTime: msg.FormatTime(s.w.last)}
	for _, k := range s.w.keys {
		m.Rooms = append(m.Rooms, s.w.truth(k))
	}
	s.record = append(s.record, m)
	if extra := len(s.record) - msg.RecordMinutes; extra > 0 {
		s.record = s.record[extra:]
	}
}

// reading is one value to write to a BuildSim sensor.
type reading struct{ sensor, value string }

// readings works out what every installed sensor in a simulated room reads
// at the current minute, and what the clocks show. A sensor in a room that
// is not simulated gets nothing.
func (s *sim) readings(equipment []buildsim.Equipment, factor float64) (sensors, clocks []reading) {
	now, _ := json.Marshal(msg.ModelTime{ModelTime: msg.FormatTime(s.w.last), Factor: factor})
	for _, e := range equipment {
		kind, ok := rooms.SensorKind(e.Type)
		if !ok || len(e.Sensors) == 0 {
			continue
		}
		id := e.Sensors[0].ID
		if kind == rooms.Clock {
			clocks = append(clocks, reading{id, string(now)})
			continue
		}
		k := rooms.Key{Level: e.Level, Name: e.Room}
		m, ok := s.w.models[k]
		if !ok {
			continue
		}
		switch kind {
		case rooms.CO2:
			sensors = append(sensors, reading{id, strconv.FormatFloat(m.CO2, 'f', 1, 64)})
		case rooms.Temp:
			sensors = append(sensors, reading{id, strconv.FormatFloat(m.Temp, 'f', 2, 64)})
		case rooms.Occupancy:
			sensors = append(sensors, reading{id, strconv.Itoa(s.w.occ[k].Occupants)})
		}
	}
	return sensors, clocks
}

// write puts the current minute into BuildSim: the sensors' values, then the
// record, and the clocks last. A device that sees the new time on a clock
// therefore finds the values of that minute already there.
func (s *sim) write(ctx context.Context, equipment []buildsim.Equipment, factor float64) {
	sensors, clocks := s.readings(equipment, factor)
	failed, first := s.put(ctx, sensors)
	if err := s.writeRecord(ctx); err != nil {
		log.Printf("room-state record: %v", err)
	}
	f, err := s.put(ctx, clocks)
	if failed += f; first == nil {
		first = err
	}
	if failed > 0 {
		// A BuildSim that restarted blank gets its equipment back from the
		// registry within seconds; until then these writes find nothing.
		log.Printf("%s: %d of %d sensor values not written: %v", msg.FormatTime(s.w.last), failed, len(sensors)+len(clocks), first)
	}
}

// put writes values a few at a time and reports how many failed.
func (s *sim) put(ctx context.Context, values []reading) (failed int, first error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, Writers)
	for _, v := range values {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			if err := s.bs.SetSensorValue(ctx, v.sensor, v.value); err != nil {
				mu.Lock()
				if failed++; first == nil {
					first = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return failed, first
}

// writeRecord keeps the rooms' full state in BuildSim, on a piece of
// equipment of physics' own. Only the truth recorder reads it (IF-5).
func (s *sim) writeRecord(ctx context.Context) error {
	if !s.registered {
		home := s.w.keys[0] // BuildSim wants every piece of equipment in a room
		err := s.bs.BulkCreate(ctx, []buildsim.Equipment{{
			ID: msg.RecordEquipment, Name: "Physical model: room state", Type: "generic", Category: "simulation",
			Level: home.Level, Room: home.Name, Status: "running",
			Sensors:   []buildsim.Sensor{{ID: msg.RecordSensor, Name: "Room state, last minutes", Type: "record", DataType: "text"}},
			Actuators: []buildsim.Actuator{},
		}})
		if err != nil {
			return err
		}
		s.registered = true
	}
	value, err := json.Marshal(msg.Record{Minutes: s.record})
	if err != nil {
		return err
	}
	err = s.bs.SetSensorValue(ctx, msg.RecordSensor, string(value))
	if buildsim.IsNotFound(err) {
		s.registered = false // BuildSim restarted blank: register again next minute
	}
	return err
}
