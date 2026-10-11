// Package dashboard turns the stream of observations, decisions and actuator
// events into what the dashboard shows (IF-11, FR-10): in the BuildSim
// viewer, one heatmap layer per measure and the alerts that are current right
// now; on viz's own page, the graphs of how each room got there (history.go).
//
// It only ever sees what the control system sees: sensor readings, the
// controller's decisions and the actuators' events. The truth is not on any
// topic viz may read.
package dashboard

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

const (
	// StaleAfter is how old a reading may be, in model time, before the room
	// drops out of its layer and an alert says the sensor is silent. It
	// matches the controller's limit: three missed samples.
	StaleAfter = 180 * time.Second
	// RejectedFor is how long a rejected command stays on the alert list.
	RejectedFor = 5 * time.Minute
	// MaxAlerts is BuildSim's cap on current alerts.
	MaxAlerts = 100

	CO2Limit      = 1000.0 // ppm, NFR-1: the grey midpoint of the CO₂ layer
	ComfortCentre = 22.0   // °C, the middle of the 20–24 °C band (NFR-2)
)

// The palettes follow one rule per job. A measure that is "how much" takes
// one hue from light to dark, so more is always darker. A measure that is
// "which side of a target" takes two opposite hues around a neutral grey, so
// the target itself reads as nothing. No rainbows: a multi-hue ramp makes a
// reader decode a legend where one glance should do.
var (
	sequential = []string{"#cde2fb", "#3987e5", "#0d366b"} // light → dark blue
	diverging  = []string{"#2a78d6", "#f0efec", "#e34948"} // blue, grey at the target, red
)

const opacity = 0.75

type reading struct {
	value float64
	at    time.Time // model time
}

type room struct {
	obs       map[string]reading // latest reading per sensor kind
	predicted *reading           // people the controller is ventilating for
	mode      string             // mode of the latest airflow decision
	fault     string             // what the controller found wrong with a sensor in that decision
	heatMode  string             // the same two for the latest heating decision
	heatFault string
	override  string             // detail of an active CO₂ override; "" if none
	fallback  map[string]bool    // actuator kind → on fallback since its commands expired
	rejected  map[string]reading // actuator kind → when a command was last rejected (value unused)
	rejectMsg map[string]string
	hist      series // the room minute by minute, for the graphs
}

// Board is the building as the dashboard knows it: the current picture, and
// the history behind the graphs.
type Board struct {
	mu     sync.Mutex
	rooms  map[rooms.Key]*room
	newest time.Time // newest model time seen on any message

	sizes  map[rooms.Key]sizing.Room // from the registry
	flows  map[rooms.Key]reading     // airflow each damper has reached, l/s
	policy string                    // of the latest decision

	lastTick int64                     // model minute of the last Tick
	active   map[string]buildsim.Alert // the alerts at the last Tick, by id
	log      []LogEntry                // alerts appearing and going, oldest first
}

func New() *Board { return &Board{rooms: map[rooms.Key]*room{}} }

// SetRooms tells the board each room's sizing, which the registry holds.
func (b *Board) SetRooms(sizes map[rooms.Key]sizing.Room) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sizes = sizes
}

func (b *Board) room(key string) (rooms.Key, *room, bool) {
	k, err := rooms.Parse(key)
	if err != nil {
		return rooms.Key{}, nil, false
	}
	r, ok := b.rooms[k]
	if !ok {
		r = &room{obs: map[string]reading{}, fallback: map[string]bool{}, rejected: map[string]reading{}, rejectMsg: map[string]string{}}
		b.rooms[k] = r
	}
	return k, r, true
}

// seen parses a message's model time and keeps track of the newest.
func (b *Board) seen(modelTime string) time.Time {
	t, err := time.Parse(time.RFC3339, modelTime)
	if err != nil {
		return b.newest // a message without a usable model time: take it as now
	}
	if t.Before(b.newest.Add(-30 * time.Minute)) {
		// The model clock was moved back, e.g. for a new experiment: start
		// over. (Messages a little out of order are far inside this margin.)
		b.rooms = map[rooms.Key]*room{}
		b.newest = t
		b.lastTick, b.active, b.log, b.flows = 0, nil, nil, nil
	}
	if t.After(b.newest) {
		b.newest = t
	}
	return t
}

// Observe records a sensor reading (IF-4).
func (b *Board) Observe(o msg.Observation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.seen(o.ModelTime)
	if _, r, ok := b.room(o.Room); ok {
		if prev, had := r.obs[o.Kind]; !had || !t.Before(prev.at) {
			r.obs[o.Kind] = reading{o.Value, t}
		}
		if p := r.hist.at(minute(t)); p != nil {
			p.observe(o)
		}
	}
}

// Decide records a controller decision (IF-6).
func (b *Board) Decide(d msg.Decision) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.seen(d.ModelTime)
	_, r, ok := b.room(d.Room)
	if !ok {
		return
	}
	b.policy = d.Policy
	if d.ModelTime != "" {
		if p := r.hist.at(minute(t)); p != nil {
			p.decide(d)
		}
	}
	kind := rooms.Heating
	if d.AirflowTarget != nil {
		kind = rooms.Damper
		r.mode, r.fault = d.Mode, d.Fault
		r.predicted = nil
		if d.PredictedOccupancy != nil {
			r.predicted = &reading{*d.PredictedOccupancy, t}
		}
	} else {
		r.heatMode, r.heatFault = d.Mode, d.Fault
	}
	if d.Status == 202 {
		// The actuator has a valid command again.
		delete(r.fallback, kind)
	}
}

// Event records something an actuator did on its own (IF-7).
func (b *Board) Event(e msg.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.seen(e.ModelTime)
	_, r, ok := b.room(e.Room)
	if !ok {
		return
	}
	switch e.Kind {
	case "override_co2":
		r.override = e.Detail
	case "override_released":
		r.override = ""
	case "ttl_expired":
		r.fallback[e.Actuator] = true
	case "rejected":
		r.rejected[e.Actuator] = reading{at: t}
		r.rejectMsg[e.Actuator] = e.Detail
	}
}

// State records what an actuator has reached (IF-9), as the actuator itself
// reports it on MQTT. Only the dampers' airflow is shown.
func (b *Board) State(st msg.State) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.seen(st.ModelTime)
	if k, err := rooms.Parse(st.Room); err == nil && st.Actuator == rooms.Damper {
		if b.flows == nil {
			b.flows = map[rooms.Key]reading{}
		}
		b.flows[k] = reading{st.Value, t}
	}
}

func (b *Board) fresh(r reading) bool { return b.newest.Sub(r.at) <= StaleAfter }

// stuck returns what the controller found wrong with a sensor that is still
// reporting, or "" (FR-7). A sensor that has since gone quiet is silent, not
// stuck, and gets that alert instead.
func (b *Board) stuck(r *room, kind string) string {
	if o, ok := r.obs[kind]; !ok || !b.fresh(o) {
		return ""
	}
	switch {
	case kind == rooms.CO2 && r.mode == "degraded_co2",
		kind == rooms.Occupancy && r.mode == "degraded_occupancy":
		return r.fault
	case kind == rooms.Temp && r.heatMode == "degraded_temp":
		return r.heatFault
	}
	return ""
}

// Layers builds the heatmaps. The airflow is each damper's reached state
// (State) as a share of the room's maximum (SetRooms). Rooms without a fresh
// value are left out of a layer, and so are rooms whose sensor the controller
// has found stuck: neither is a measurement. A layer nobody has values for is
// left out altogether (the predicted occupancy, under a policy that doesn't
// forecast).
func (b *Board) Layers() []buildsim.RoomLayer {
	b.mu.Lock()
	defer b.mu.Unlock()

	co2 := layer("co2", "CO₂ (grey = 1000 ppm limit)", "ppm", "sensor reading", CO2Limit-600, CO2Limit+600, diverging)
	people := layer("occupancy", "People counted", "", "sensor reading", 0, 50, sequential)
	expected := layer("occupancy_predicted", "People expected soon", "", "prediction", 0, 50, sequential)
	air := layer("airflow", "Airflow (% of room max)", "%", "actuator state", 0, 100, sequential)
	temp := layer("temperature", "Temperature (grey = 22 °C)", "°C", "sensor reading", ComfortCentre-5, ComfortCentre+5, diverging)

	// The labels are short because the viewer's legend is narrow, and the
	// people layers carry no unit because the viewer prints it straight
	// after the number ("0people"); their labels name it instead. "People
	// expected soon" is the number the controller is ventilating for: the
	// most of the count now and the forecast within the room's lead time.
	for k, r := range b.rooms {
		key := k.String()
		for kind, l := range map[string]*buildsim.RoomLayer{rooms.CO2: &co2, rooms.Occupancy: &people, rooms.Temp: &temp} {
			if o, ok := r.obs[kind]; ok && b.fresh(o) && b.stuck(r, kind) == "" {
				l.Values[key] = o.value
			}
		}
		if r.predicted != nil && b.fresh(*r.predicted) {
			expected.Values[key] = r.predicted.value
		}
	}
	for k, flow := range b.flows {
		if most := b.sizes[k].Max; most > 0 && b.fresh(flow) {
			air.Values[k.String()] = 100 * flow.value / most
		}
	}

	var out []buildsim.RoomLayer
	for _, l := range []buildsim.RoomLayer{co2, people, expected, air, temp} {
		if len(l.Values) > 0 {
			out = append(out, l)
		}
	}
	return out
}

func layer(id, label, unit, source string, lo, hi float64, palette []string) buildsim.RoomLayer {
	return buildsim.RoomLayer{
		ID: id, Label: label, Unit: unit, Source: source,
		Minimum: lo, Maximum: hi, Opacity: opacity, Palette: palette,
		Values: map[string]float64{},
	}
}

var sensorNames = map[string]string{rooms.CO2: "CO₂", rooms.Temp: "temperature", rooms.Occupancy: "occupancy"}

var actuatorNames = map[string]string{rooms.Damper: "Damper", rooms.Heating: "Radiator valve"}

// What a stuck sensor is called in an alert, and what the system does about it.
var stuckAlerts = map[string][2]string{
	rooms.CO2:       {"CO₂ sensor", "The damper is held at design flow."},
	rooms.Temp:      {"Temperature sensor", "The radiator valve is held at 21 °C."},
	rooms.Occupancy: {"Occupancy counter", "The controller goes by the forecast and CO₂."},
}

// Alerts lists what is wrong right now for the viewer, most severe first and
// at most MaxAlerts. It is the current picture only; storage is the audit
// trail.
func (b *Board) Alerts() []buildsim.Alert {
	b.mu.Lock()
	defer b.mu.Unlock()
	alerts := b.current()
	if len(alerts) > MaxAlerts {
		more := len(alerts) - (MaxAlerts - 1)
		alerts = append(alerts[:MaxAlerts-1], buildsim.Alert{
			ID: "more", Severity: "info", Title: fmt.Sprintf("%d more alerts", more),
			Message: "The viewer shows at most 100; storage has every event.",
		})
	}
	return alerts
}

// current is every alert, most severe first. The caller holds the lock.
func (b *Board) current() []buildsim.Alert {
	var alerts []buildsim.Alert
	add := func(severity, kind string, k rooms.Key, title, message string) {
		alerts = append(alerts, buildsim.Alert{
			ID: kind + "-" + k.Level + "-" + k.Name, Severity: severity,
			Title: title, Message: message, Level: k.Level, Room: k.Name,
		})
	}
	noForecast := 0
	for k, r := range b.rooms {
		if r.override != "" {
			add("critical", "override", k, "CO₂ override in "+k.Name,
				r.override+". The damper is at maximum, whatever the controller asks.")
		}
		for kind, name := range sensorNames {
			if o, ok := r.obs[kind]; ok && !b.fresh(o) {
				add("warning", "silent-"+kind, k, fmt.Sprintf("No %s reading from %s", name, k.Name),
					fmt.Sprintf("Last reading at %s, %.0f min ago.%s", o.at.Format("15:04"), b.newest.Sub(o.at).Minutes(), consequence(kind)))
			}
		}
		for kind, text := range stuckAlerts {
			if fault := b.stuck(r, kind); fault != "" {
				add("warning", "stuck-"+kind, k, fmt.Sprintf("%s in %s looks stuck", text[0], k.Name), fault+". "+text[1])
			}
		}
		for kind := range r.fallback {
			add("warning", "fallback-"+kind, k, fmt.Sprintf("%s in %s on fallback", actuatorNames[kind], k.Name),
				"Its commands expired, so it went to its safe default.")
		}
		for kind, at := range r.rejected {
			if b.newest.Sub(at.at) <= RejectedFor {
				add("warning", "rejected-"+kind, k, fmt.Sprintf("Command rejected in %s", k.Name), r.rejectMsg[kind])
			}
		}
		switch r.mode {
		case "degraded_occupancy":
			// A silent or a stuck counter already has its own alert saying
			// the same.
			if o, ok := r.obs[rooms.Occupancy]; (!ok || b.fresh(o)) && b.stuck(r, rooms.Occupancy) == "" {
				add("warning", "mode", k, "No occupancy count in "+k.Name, "The controller is going by the forecast and CO₂ only.")
			}
		case "no_forecast":
			noForecast++
		}
	}
	if noForecast > 0 {
		alerts = append(alerts, buildsim.Alert{
			ID: "no-forecast", Severity: "info", Title: "No trained model",
			Message: fmt.Sprintf("The predictive policy has no forecast for %d rooms and acts on CO₂ alone there.", noForecast),
		})
	}

	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.Slice(alerts, func(i, j int) bool {
		if rank[alerts[i].Severity] != rank[alerts[j].Severity] {
			return rank[alerts[i].Severity] < rank[alerts[j].Severity]
		}
		return alerts[i].ID < alerts[j].ID
	})
	return alerts
}

// consequence says what the system does about a silent sensor (FR-7).
func consequence(kind string) string {
	switch kind {
	case rooms.CO2:
		return " Without it the damper gets no commands and falls back to design flow."
	case rooms.Temp:
		return " Without it the radiator valve gets no commands and falls back to 21 °C."
	case rooms.Occupancy:
		return " The controller goes by the forecast and CO₂."
	}
	return ""
}
