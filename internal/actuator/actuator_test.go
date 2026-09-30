package actuator

import (
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

var (
	testRoom = rooms.Key{Level: "level0", Name: "A1111"}
	size     = sizing.For(80)
	monday   = time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
)

func damper(t *testing.T) *Device {
	t.Helper()
	d := New(rooms.Damper, map[rooms.Key]sizing.Room{testRoom: size})
	d.Tick(monday, nil)
	return d
}

// T-07: out-of-range, wrong unit, expired and duplicate commands leave the
// state unchanged.
func TestValidation(t *testing.T) {
	d := damper(t)
	ok := Command{CmdID: "a", Value: 200, Unit: "l/s", TTLs: 180}
	if r := d.Handle(testRoom, ok); r.Status != 202 {
		t.Fatalf("valid command: %+v", r)
	}
	for name, tc := range map[string]struct {
		c    Command
		want int
	}{
		"out of range": {Command{CmdID: "b", Value: size.Max + 1, Unit: "l/s"}, 422},
		"wrong unit":   {Command{CmdID: "c", Value: 100, Unit: "°C"}, 422},
		"expired":      {Command{CmdID: "d", Value: 100, Unit: "l/s", TTLs: 60, IssuedAt: monday.Add(-2 * time.Minute).Format(time.RFC3339)}, 409},
		"duplicate":    {Command{CmdID: "a", Value: 50, Unit: "l/s"}, 200},
	} {
		if r := d.Handle(testRoom, tc.c); r.Status != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", name, r.Status, tc.want, r.Reason)
		}
	}
	if got := d.target(d.rooms[testRoom]); got != 200 {
		t.Errorf("target changed to %.1f by rejected commands", got)
	}
}

// T-08: the reached state moves at most 10 % of max per model-minute.
func TestTravelRate(t *testing.T) {
	d := damper(t)
	d.Handle(testRoom, Command{CmdID: "a", Value: size.Max, Unit: "l/s"})
	start := d.rooms[testRoom].reached
	reached, _ := d.Tick(monday.Add(time.Minute), nil)
	if step := reached[testRoom] - start; step > 0.1*size.Max+1e-9 {
		t.Errorf("moved %.1f l/s in one minute, limit %.1f", step, 0.1*size.Max)
	}
}

// T-09: CO₂ above 1100 ppm forces maximum flow whatever the command says, and
// releases below it.
func TestCO2Override(t *testing.T) {
	d := damper(t)
	d.Handle(testRoom, Command{CmdID: "a", Value: 0, Unit: "l/s"})
	_, ev := d.Tick(monday.Add(time.Minute), map[rooms.Key]float64{testRoom: 1200})
	if d.target(d.rooms[testRoom]) != size.Max || len(ev) != 1 || ev[0].Kind != "override_co2" {
		t.Fatalf("override not applied: target %.1f, events %+v", d.target(d.rooms[testRoom]), ev)
	}
	_, ev = d.Tick(monday.Add(2*time.Minute), map[rooms.Key]float64{testRoom: 900})
	if d.target(d.rooms[testRoom]) == size.Max || len(ev) != 1 || ev[0].Kind != "override_released" {
		t.Fatalf("override not released: events %+v", ev)
	}
}

// T-11: a zero-flow command is clamped to the occupied floor on a weekday and
// the unoccupied floor at night.
func TestFlowFloors(t *testing.T) {
	d := damper(t)
	r := d.Handle(testRoom, Command{CmdID: "a", Value: 0, Unit: "l/s"})
	if !r.Clamped || r.Target != size.Occupied {
		t.Errorf("weekday: %+v, want clamped to %.1f", r, size.Occupied)
	}
	d.Tick(monday.Add(12*time.Hour), nil) // 22:00
	r = d.Handle(testRoom, Command{CmdID: "b", Value: 0, Unit: "l/s"})
	if !r.Clamped || r.Target != size.Empty {
		t.Errorf("night: %+v, want clamped to %.1f", r, size.Empty)
	}
}

// A command expires after its TTL and the damper returns to design flow.
func TestTTLFallback(t *testing.T) {
	d := damper(t)
	d.Handle(testRoom, Command{CmdID: "a", Value: 50, Unit: "l/s", TTLs: 180})
	_, ev := d.Tick(monday.Add(4*time.Minute), nil)
	if len(ev) != 1 || ev[0].Kind != "ttl_expired" {
		t.Fatalf("events %+v, want ttl_expired", ev)
	}
	if got := d.target(d.rooms[testRoom]); got != size.Design {
		t.Errorf("target %.1f after expiry, want design %.1f", got, size.Design)
	}
}
