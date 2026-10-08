// Package stuck detects sensors that keep repeating one value (FR-7). A stuck
// sensor is harder than a silent one: its readings arrive on time and look
// valid. There are two ways to tell, and each fits a different sensor.
//
// CO₂ and temperature sensors are noisy, so a healthy one almost never
// repeats itself: enough identical readings in a row can't be chance (Flat).
//
// The occupancy counter can't be judged like that. An empty room really does
// read 0 for hours. It is judged against the room's CO₂ instead, with the two
// things the mass balance V·dx/dt = G·n − q·x says whatever the airflow q is,
// as long as 0 ≤ q ≤ q_max (x is CO₂ above outdoor, n the people):
//
//	x can't rise faster than G·n/V        (nobody else is breathing)
//	x can't stay below (G·n/q_max)·(1 − e^(−t·q_max/V)) after t with n people
//
// A counter that has not moved and breaks either one is stuck (Counter). The
// damper's state never enters the check, so a wrong idea of the airflow
// can't cause a false alarm.
//
// It has no I/O; the controller feeds it readings and asks.
package stuck

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

// FlatAfter is how many identical readings in a row mean a stuck sensor.
// Readings are rounded (CO₂ to 1 ppm, temperature to 0.1 °C) and noisy (FR-2:
// σ ≈ 21 ppm and 0.15 °C), so a healthy sensor repeats a value now and then.
// These are the shortest runs a healthy sensor produces less than once in 100
// days in a 50-room building (72 000 readings a day per kind): the chance of
// such a run is 5.5e-8 for CO₂ and 1.2e-7 for temperature. Temperature needs
// the longer run because its noise is only 1.5 rounding steps wide.
var FlatAfter = map[string]int{rooms.CO2: 5, rooms.Temp: 12}

const (
	// Mean is the span CO₂ is averaged over: five readings cut the noise
	// from σ ≈ 22 ppm to a standard error of about 10.
	Mean = 5 * time.Minute
	// Window is how far back the rise rule looks.
	Window = 15 * time.Minute
	// Gap is the longest pause between two counter readings that still
	// counts as one unbroken run. It is the controller's staleness limit.
	Gap = 180 * time.Second

	// Sigmas is how far outside a rule's limit the measured CO₂ must be, in
	// standard errors of the measurement, before the counter is blamed.
	// Five is a chance of 3 in 10 million per check: for the 50 rooms that
	// is at most one false alarm in about 200 days.
	Sigmas = 5.0
	// Slack allows people to differ: both rules assume a person may give
	// off up to twice, or as little as half, the CO₂ of sizing.CO2PerPerson.
	Slack = 2.0

	minSamples = 3 // readings a mean needs
)

// co2Noise is the standard deviation of one CO₂ reading at a given level,
// from the sensor's accuracy in FR-2: ±(30 ppm + 3 %) covers 95 %.
func co2Noise(ppm float64) float64 { return (30 + 0.03*ppm) / 2 }

type sample struct {
	at  time.Time // model time
	ppm float64
}

type run struct {
	value float64
	n     int
}

// room is what the counter check keeps for one room.
type room struct {
	have  bool
	count float64   // what the counter says
	since time.Time // first reading of the current unbroken run of that value
	last  time.Time // latest counter reading
	why   string    // why the counter was found stuck; "" = trusted
	co2   []sample  // the last Window + Mean of trusted CO₂ readings
}

// Detector holds the state for every sensor and room of one controller.
type Detector struct {
	mu    sync.Mutex
	runs  map[string]*run // by sensor id
	rooms map[rooms.Key]*room
}

func New() *Detector {
	return &Detector{runs: map[string]*run{}, rooms: map[rooms.Key]*room{}}
}

// Flat records a reading from a CO₂ or temperature sensor. It returns how
// many readings in a row have had this value, and whether that is too many
// for a healthy sensor. One different reading makes the sensor trusted again.
func (d *Detector) Flat(sensorID, kind string, value float64) (n int, stuck bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.runs[sensorID]
	if !ok || r.value != value {
		d.runs[sensorID] = &run{value: value, n: 1}
		return 1, false
	}
	r.n++
	limit, checked := FlatAfter[kind]
	return r.n, checked && r.n >= limit
}

func (d *Detector) room(k rooms.Key) *room {
	r, ok := d.rooms[k]
	if !ok {
		r = &room{}
		d.rooms[k] = r
	}
	return r
}

// Count records a reading from a room's occupancy counter.
func (d *Detector) Count(k rooms.Key, t time.Time, n float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.room(k)
	switch {
	case t.Before(r.last):
		// The model clock was moved back (a new experiment): start over.
		*r = room{}
		fallthrough
	case !r.have, n != r.count, t.Sub(r.last) > Gap:
		// A changed value is a counter that works; so the verdict goes too.
		// After a pause the run starts again: nothing is known about the gap.
		r.have, r.count, r.since, r.why = true, n, t, ""
	}
	r.last = t
}

// CO2 records a CO₂ reading for a room. Only readings from a sensor that is
// itself trusted belong here.
func (d *Detector) CO2(k rooms.Key, t time.Time, ppm float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.room(k)
	if len(r.co2) > 0 && t.Before(r.co2[len(r.co2)-1].at) {
		r.co2 = nil // the clock was moved back
	}
	r.co2 = append(r.co2, sample{t, ppm})
	for len(r.co2) > 0 && t.Sub(r.co2[0].at) >= Window+Mean {
		r.co2 = r.co2[1:]
	}
}

// mean averages the CO₂ readings in (from, to] and returns how many there
// were. ok is false with too few.
func (r *room) mean(from, to time.Time) (ppm, n float64, ok bool) {
	var sum float64
	for _, s := range r.co2 {
		if s.at.After(from) && !s.at.After(to) {
			sum += s.ppm
			n++
		}
	}
	return sum / n, n, n >= minSamples
}

// Counter reports whether room k's occupancy counter is stuck at model time
// t, and why. A verdict stays until the counter's value changes.
//
// What it can't see: a counter stuck on a number whose CO₂ would be lost in
// the sensor noise even at full airflow (roughly, fewer people than a quarter
// of the room's capacity), and a counter stuck on a number that happens to
// stay right. Neither costs much: the first is a small flow, the second none.
func (d *Detector) Counter(k rooms.Key, size sizing.Room, t time.Time) (why string, stuck bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.rooms[k]
	if !ok || !r.have || size.VolumeM3 <= 0 || size.Max <= 0 {
		return "", false
	}
	if r.why != "" {
		return r.why, true
	}
	now, nNow, ok := r.mean(t.Add(-Mean), t)
	if !ok {
		return "", false
	}
	flat := t.Sub(r.since)
	perPerson := sizing.CO2PerPerson * 1e6 // ppm·m³/s of CO₂ from one person

	// Too few counted: CO₂ rose more than the counted people can breathe out.
	if flat >= Window+Mean {
		if before, nBefore, ok := r.mean(t.Add(-Window-Mean), t.Add(-Window)); ok {
			most := Slack * perPerson * r.count * Window.Seconds() / size.VolumeM3
			margin := Sigmas * co2Noise(max(now, before)) * math.Sqrt(1/nNow+1/nBefore)
			if rise := now - before; rise > most+margin {
				r.why = fmt.Sprintf("Occupancy counter has read %.0f for %.0f min, but CO₂ rose %.0f ppm in %.0f min: more people than that are breathing",
					r.count, flat.Minutes(), rise, Window.Minutes())
			}
		}
	}

	// Too many counted: CO₂ is lower than that many people could leave it,
	// even with the damper fully open the whole time. The people have been
	// there since the run began, so by the start of the averaging span they
	// had been breathing for flat − Mean.
	if r.why == "" && r.count >= 1 && flat > Mean {
		qMax := size.Max / 1000 // m³/s
		least := perPerson * r.count / qMax * (1 - math.Exp(-(flat-Mean).Seconds()*qMax/size.VolumeM3)) / Slack
		margin := Sigmas * co2Noise(now) / math.Sqrt(nNow)
		if above := now - sizing.OutdoorCO2; above < least-margin {
			r.why = fmt.Sprintf("Occupancy counter has read %.0f for %.0f min, but CO₂ is %.0f ppm above outdoor: that many people would keep it at least %.0f above",
				r.count, flat.Minutes(), max(above, 0), least)
		}
	}
	return r.why, r.why != ""
}
