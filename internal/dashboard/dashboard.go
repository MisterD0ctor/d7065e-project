// Package dashboard turns the stream of observations, decisions and actuator
// events into what the BuildSim viewer shows (IF-11, FR-10): one heatmap
// layer per measure, and the alerts that are current right now.
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
	override  string             // detail of an active CO₂ override; "" if none
	fallback  map[string]bool    // actuator kind → on fallback since its commands expired
	rejected  map[string]reading // actuator kind → when a command was last rejected (value unused)
	rejectMsg map[string]string
}

// Board is the current picture of the building, as the dashboard knows it.
type Board struct {
	mu     sync.Mutex
	rooms  map[rooms.Key]*room
	newest time.Time // newest model time seen on any message
}

func New() *Board { return &Board{rooms: map[rooms.Key]*room{}} }

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
		return b.newest // decisions made in the REST fallback carry no model time
	}
	if t.Before(b.newest.Add(-30 * time.Minute)) {
		// The model clock was moved back, e.g. for a new experiment: start
		// over. (Messages a little out of order are far inside this margin.)
		b.rooms = map[rooms.Key]*room{}
		b.newest = t
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
	kind := rooms.Heating
	if d.AirflowTarget != nil {
		kind = rooms.Damper
		r.mode = d.Mode
		r.predicted = nil
		if d.PredictedOccupancy != nil {
			r.predicted = &reading{*d.PredictedOccupancy, t}
		}
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

func (b *Board) fresh(r reading) bool { return b.newest.Sub(r.at) <= StaleAfter }

// Layers builds the heatmaps. airflowPct is each room's airflow as a share of
// its maximum, which the caller reads from the actuators' reported states.
// Rooms without a fresh value are left out of a layer, and a layer nobody has
// values for is left out altogether (the predicted occupancy, under a policy
// that doesn't forecast).
func (b *Board) Layers(airflowPct map[rooms.Key]float64) []buildsim.RoomLayer {
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
			if o, ok := r.obs[kind]; ok && b.fresh(o) {
				l.Values[key] = o.value
			}
		}
		if r.predicted != nil && b.fresh(*r.predicted) {
			expected.Values[key] = r.predicted.value
		}
	}
	for k, pct := range airflowPct {
		air.Values[k.String()] = pct
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

// Alerts lists what is wrong right now, most severe first. It is the current
// picture only; storage is the audit trail.
func (b *Board) Alerts() []buildsim.Alert {
	b.mu.Lock()
	defer b.mu.Unlock()

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
			// A silent counter already has its own alert saying the same.
			if o, ok := r.obs[rooms.Occupancy]; !ok || b.fresh(o) {
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
	if len(alerts) > MaxAlerts {
		more := len(alerts) - (MaxAlerts - 1)
		alerts = append(alerts[:MaxAlerts-1], buildsim.Alert{
			ID: "more", Severity: "info", Title: fmt.Sprintf("%d more alerts", more),
			Message: "The viewer shows at most 100; storage has every event.",
		})
	}
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
