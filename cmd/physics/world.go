package main

import (
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/roommodel"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

// world is the simulated rooms. It advances from one whole model minute to the
// next, with the occupancy occupancysim recorded for those minutes. The truth
// is therefore the same function of model time in every run of a day (NFR-7):
// when physics happens to tick only decides when a minute is computed, not
// what it contains. (The first evaluation sampled BuildSim's occupancy, which
// has no time attached and is refreshed on a wall-clock schedule, and two runs
// of one day disagreed in ~0.3 % of samples.)
type world struct {
	keys   []rooms.Key
	models map[rooms.Key]*roommodel.Room
	act    map[rooms.Key]actuation       // what the room's actuators have reached
	occ    map[rooms.Key]clock.RoomCount // who is in each room at last
	last   time.Time                     // the minute the rooms are at; zero before the first
}

// actuation is the state a room's damper and radiator valve have reached.
type actuation struct {
	AirflowLs float64
	Setpoint  float64
}

func newWorld(keys []rooms.Key, sizes map[rooms.Key]sizing.Room) *world {
	w := &world{
		keys:   keys,
		models: map[rooms.Key]*roommodel.Room{},
		act:    map[rooms.Key]actuation{},
		occ:    map[rooms.Key]clock.RoomCount{},
	}
	for _, k := range keys {
		w.models[k] = roommodel.New(sizes[k])
		// Until an actuator reports, assume design flow and the default setpoint.
		w.act[k] = actuation{AirflowLs: sizes[k].Design, Setpoint: sizing.DefaultSetpoint}
	}
	return w
}

// advance moves the rooms to minute m. The people who were in a room at the
// start of the interval breathe and give off heat for all of it; the
// occupancy at m then takes over. The very first minute, or one that is not
// later than the last (the model clock was moved back), only sets the
// starting point. A gap of several minutes, which happens when physics falls
// more than occupancysim's log behind, is stepped in one go.
func (w *world) advance(m clock.Minute) {
	if !w.last.IsZero() && m.Time.After(w.last) {
		d := m.Time.Sub(w.last)
		for _, k := range w.keys {
			a := w.act[k]
			w.models[k].Step(d, roommodel.Inputs{
				Occupancy: w.occ[k].Occupants,
				AirflowLs: a.AirflowLs,
				Setpoint:  a.Setpoint,
			})
		}
	}
	for _, k := range w.keys {
		w.occ[k] = m.Rooms[k.String()] // absent means empty
	}
	w.last = m.Time
}

// truth is one room's true state at w.last (IF-5).
func (w *world) truth(runID string, k rooms.Key) msg.Truth {
	m, a, o := w.models[k], w.act[k], w.occ[k]
	return msg.Truth{
		RunID: runID, Room: k.String(), ModelTime: msg.FormatTime(w.last),
		CO2: m.CO2, Temp: m.Temp, Occupancy: o.Occupants, Guards: o.Guards,
		Airflow: a.AirflowLs, Setpoint: a.Setpoint,
		FanW: m.FanW, AHUHeatW: m.AHUHeatW, RadiatorW: m.RadiatorW,
	}
}
