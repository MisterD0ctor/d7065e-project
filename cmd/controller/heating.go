package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

// Heating follows the occupied schedule: the comfort setpoint in occupied
// hours, a setback otherwise. A room that cooled overnight needs over an hour
// to warm up, so switching at 07:00 leaves the first arrivals cold (found in
// the evaluation: rooms at 17.9 °C at 07:00, 20 °C at 08:13). "Optimal start"
// ends the setback early enough for the room to be warm when the occupied
// hours begin. It is the same idea as the predictive ventilation, applied to
// the slower of the two processes: the schedule is known, so act ahead of it.
const (
	ComfortSetpoint = 21.0
	SetbackSetpoint = 18.0

	// WarmUpRate is how fast a room is assumed to warm with the radiator
	// valve fully open. It is a commissioning estimate, on the slow side so
	// that rooms are warm early rather than late; a real optimal-start
	// controller learns it per room from earlier mornings.
	WarmUpRate = 1.5 // K per hour

	// WarmUpMargin covers what the rate leaves out: the valve travelling to
	// the new setpoint and one sample period of the temperature sensor.
	WarmUpMargin = 15 * time.Minute
)

// warmUp is how long before the occupied hours heating has to start in a room
// measured at temp.
func warmUp(temp float64) time.Duration {
	deficit := ComfortSetpoint - temp
	if deficit <= 0 {
		return 0
	}
	return time.Duration(deficit/WarmUpRate*float64(time.Hour)) + WarmUpMargin
}

// optimalStart remembers, per room, which occupied period it has started
// pre-heating for. Without that memory the decision would flip back: when a
// room warms faster than WarmUpRate assumes, the time still needed shrinks
// faster than the clock advances, and the setpoint would drop to the setback
// again halfway through the warm-up.
type optimalStart struct {
	mu      sync.Mutex
	started map[rooms.Key]preheat
}

type preheat struct {
	until time.Time // start of the occupied period being pre-heated for
	since time.Time // model time the pre-heat began
}

// Setpoint returns the heating setpoint for room k at model time t, given its
// measured temperature, and the reason for the decision record.
func (o *optimalStart) Setpoint(k rooms.Key, t time.Time, measured float64) (float64, string) {
	if sizing.OccupiedHours(t) {
		return ComfortSetpoint, fmt.Sprintf("schedule: occupied hours, %.1f °C measured", measured)
	}
	next := sizing.NextOccupiedStart(t)

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.started == nil {
		o.started = map[rooms.Key]preheat{}
	}
	p, ok := o.started[k]
	if ok && t.Before(p.since) {
		// The model clock was moved back to before this pre-heat began.
		delete(o.started, k)
		ok = false
	}
	if ok && p.until.Equal(next) {
		return ComfortSetpoint, fmt.Sprintf("pre-heat for %s, %.1f °C measured", next.Format("Mon 15:04"), measured)
	}
	if need := warmUp(measured); need > 0 && !t.Add(need).Before(next) {
		o.started[k] = preheat{until: next, since: t}
		return ComfortSetpoint, fmt.Sprintf("pre-heat for %s: %.1f °C measured, %.0f min needed",
			next.Format("Mon 15:04"), measured, need.Minutes())
	}
	return SetbackSetpoint, fmt.Sprintf("schedule: setback, %.1f °C measured", measured)
}
