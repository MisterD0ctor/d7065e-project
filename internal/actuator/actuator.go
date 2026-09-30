// Package actuator is the device logic behind IF-8: validate a command, keep
// it only while its TTL lasts, enforce the safety limits independently of the
// controller (D-6), and travel towards the target at a fixed rate (FR-5).
// It has no I/O; cmd/actuator wires it to HTTP, the clock and BuildSim.
package actuator

import (
	"fmt"
	"sync"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

const DefaultTTL = 180 * time.Second

type Command struct {
	CmdID    string  `json:"cmd_id"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit"`
	IssuedAt string  `json:"issued_at,omitempty"` // model time; optional until obs carry model time
	TTLs     float64 `json:"ttl_s"`
	Reason   string  `json:"reason"`
}

// Result is the outcome of a command, mapped to an HTTP status by the caller.
type Result struct {
	Status    int     `json:"-"`
	Target    float64 `json:"target,omitempty"`
	Clamped   bool    `json:"clamped,omitempty"`
	Duplicate bool    `json:"duplicate,omitempty"`
	Reason    string  `json:"reason,omitempty"`
}

// Event is something the dashboard and the audit trail should know (IF-7).
type Event struct {
	Room   rooms.Key
	Kind   string // override_co2, override_released, ttl_expired, rejected
	Detail string
}

type room struct {
	size      sizing.Room
	commanded float64   // last accepted command value
	accepted  time.Time // model time it was accepted; zero = none
	ttl       time.Duration
	reached   float64
	override  bool
}

// Device is one actuator process: one kind, many rooms.
type Device struct {
	Kind string

	mu    sync.Mutex
	rooms map[rooms.Key]*room
	seen  map[string]bool
	order []string // cmd_ids in arrival order, to bound seen
	now   time.Time
}

const seenLimit = 4096

func New(kind string, sizes map[rooms.Key]sizing.Room) *Device {
	d := &Device{Kind: kind, rooms: map[rooms.Key]*room{}, seen: map[string]bool{}}
	for k, s := range sizes {
		d.rooms[k] = &room{size: s, reached: d.fallback(s)}
	}
	return d
}

func (d *Device) Unit() string {
	if d.Kind == rooms.Heating {
		return "°C"
	}
	return "l/s"
}

// fallback is where the actuator goes without a valid command: design flow,
// or the default heating setpoint.
func (d *Device) fallback(s sizing.Room) float64 {
	if d.Kind == rooms.Heating {
		return sizing.DefaultSetpoint
	}
	return s.Design
}

func (d *Device) limits(s sizing.Room) (lo, hi float64) {
	if d.Kind == rooms.Heating {
		return sizing.MinSetpoint, sizing.MaxSetpoint
	}
	return 0, s.Max
}

// ratePerMinute is how far the reached state may move per model-minute.
func (d *Device) ratePerMinute(s sizing.Room) float64 {
	if d.Kind == rooms.Heating {
		return 0.5
	}
	return 0.1 * s.Max
}

// Handle validates and applies a command (IF-8).
func (d *Device) Handle(k rooms.Key, c Command) Result {
	d.mu.Lock()
	defer d.mu.Unlock()

	r, ok := d.rooms[k]
	if !ok {
		return Result{Status: 422, Reason: fmt.Sprintf("unknown room %s", k)}
	}
	if c.CmdID == "" {
		return Result{Status: 422, Reason: "cmd_id missing"}
	}
	if d.seen[c.CmdID] {
		return Result{Status: 200, Duplicate: true}
	}
	if c.Unit != d.Unit() {
		return Result{Status: 422, Reason: fmt.Sprintf("unit %q, want %q", c.Unit, d.Unit())}
	}
	if lo, hi := d.limits(r.size); c.Value < lo || c.Value > hi {
		return Result{Status: 422, Reason: fmt.Sprintf("value %.1f outside %.1f–%.1f", c.Value, lo, hi)}
	}
	ttl := DefaultTTL
	if c.TTLs > 0 {
		ttl = time.Duration(c.TTLs * float64(time.Second))
	}
	if c.IssuedAt != "" {
		issued, err := time.Parse(time.RFC3339, c.IssuedAt)
		if err != nil {
			return Result{Status: 422, Reason: "issued_at is not RFC 3339"}
		}
		if !d.now.IsZero() && issued.Add(ttl).Before(d.now) {
			return Result{Status: 409, Reason: "expired"}
		}
	}

	d.remember(c.CmdID)
	r.commanded, r.accepted, r.ttl = c.Value, d.now, ttl
	target := d.target(r)
	return Result{Status: 202, Target: target, Clamped: target != c.Value}
}

func (d *Device) remember(id string) {
	d.seen[id] = true
	d.order = append(d.order, id)
	if len(d.order) > seenLimit {
		delete(d.seen, d.order[0])
		d.order = d.order[1:]
	}
}

// target applies the safety policy to the commanded value. Only the damper
// has one: the flow floor, and maximum flow during a CO₂ override.
func (d *Device) target(r *room) float64 {
	v := r.commanded
	if r.accepted.IsZero() {
		v = d.fallback(r.size)
	}
	if d.Kind != rooms.Damper {
		return v
	}
	if r.override {
		return r.size.Max
	}
	return max(v, r.size.Floor(d.now))
}

// Tick advances to model time now. co2 holds the latest CO₂ reading per room
// (damper only; missing rooms keep their override state). It returns the
// reached state per room and the events that happened.
func (d *Device) Tick(now time.Time, co2 map[rooms.Key]float64) (map[rooms.Key]float64, []Event) {
	d.mu.Lock()
	defer d.mu.Unlock()

	elapsed := time.Duration(0)
	if !d.now.IsZero() && now.After(d.now) {
		elapsed = now.Sub(d.now)
	}
	d.now = now

	var events []Event
	reached := map[rooms.Key]float64{}
	for k, r := range d.rooms {
		if !r.accepted.IsZero() && now.Sub(r.accepted) > r.ttl {
			r.accepted = time.Time{}
			events = append(events, Event{k, "ttl_expired", "no valid command; back to fallback"})
		}
		if v, ok := co2[k]; ok && d.Kind == rooms.Damper {
			switch over := v > sizing.OverrideCO2; {
			case over && !r.override:
				events = append(events, Event{k, "override_co2", fmt.Sprintf("CO₂ %.0f ppm > %.0f", v, sizing.OverrideCO2)})
			case !over && r.override:
				events = append(events, Event{k, "override_released", fmt.Sprintf("CO₂ %.0f ppm", v)})
			}
			r.override = v > sizing.OverrideCO2
		}
		r.reached = travel(r.reached, d.target(r), d.ratePerMinute(r.size)*elapsed.Minutes())
		reached[k] = r.reached
	}
	return reached, events
}

func travel(from, to, step float64) float64 {
	switch {
	case to > from:
		return min(from+step, to)
	case to < from:
		return max(from-step, to)
	}
	return from
}
