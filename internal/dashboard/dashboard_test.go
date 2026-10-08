package dashboard

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

var ten = time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)

func at(min int) string { return msg.FormatTime(ten.Add(time.Duration(min) * time.Minute)) }

func obs(room, kind string, v float64, min int) msg.Observation {
	return msg.Observation{Room: room, Kind: kind, Value: v, ModelTime: at(min)}
}

func find(layers []buildsim.RoomLayer, id string) (buildsim.RoomLayer, bool) {
	for _, l := range layers {
		if l.ID == id {
			return l, true
		}
	}
	return buildsim.RoomLayer{}, false
}

func titles(alerts []buildsim.Alert) string {
	var out []string
	for _, a := range alerts {
		out = append(out, a.Severity+": "+a.Title)
	}
	return strings.Join(out, " | ")
}

func ptr(v float64) *float64 { return &v }

func TestLayersShowTheLatestReadings(t *testing.T) {
	b := New()
	b.Observe(obs("level0/1570", rooms.CO2, 640, 0))
	b.Observe(obs("level0/1570", rooms.CO2, 712, 1))
	b.Observe(obs("level0/1570", rooms.CO2, 600, 0)) // late arrival of an older reading
	b.Observe(obs("level0/1570", rooms.Occupancy, 16, 1))
	b.Observe(obs("level0/1570", rooms.Temp, 21.4, 1))
	b.Decide(msg.Decision{Room: "level0/1570", ModelTime: at(1), Mode: "normal", AirflowTarget: ptr(140), PredictedOccupancy: ptr(18), Status: 202})

	layers := b.Layers(map[rooms.Key]float64{{Level: "level0", Name: "1570"}: 44})
	for id, want := range map[string]float64{"co2": 712, "occupancy": 16, "temperature": 21.4, "occupancy_predicted": 18, "airflow": 44} {
		l, ok := find(layers, id)
		if !ok || l.Values["level0/1570"] != want {
			t.Errorf("layer %s: %v (present %v), want %v", id, l.Values["level0/1570"], ok, want)
		}
	}
}

// Under a policy that doesn't forecast there is nothing to show in the
// predicted layer, so it isn't published at all.
func TestEmptyLayersAreLeftOut(t *testing.T) {
	b := New()
	b.Observe(obs("level0/1570", rooms.CO2, 640, 0))
	b.Decide(msg.Decision{Room: "level0/1570", ModelTime: at(0), Mode: "normal", AirflowTarget: ptr(35), Status: 202})
	if _, ok := find(b.Layers(nil), "occupancy_predicted"); ok {
		t.Error("an empty predicted-occupancy layer was published")
	}
}

// T-13 on the dashboard: a sensor that goes quiet drops out of its layer and
// is named in an alert, while the rest of the building carries on.
func TestSilentSensor(t *testing.T) {
	b := New()
	b.Observe(obs("level0/1570", rooms.CO2, 640, 0))
	b.Observe(obs("level0/A109", rooms.CO2, 500, 0))
	for min := 1; min <= 6; min++ {
		b.Observe(obs("level0/A109", rooms.CO2, 500, min)) // 1570's sensor has died
	}
	co2, _ := find(b.Layers(nil), "co2")
	if _, shown := co2.Values["level0/1570"]; shown {
		t.Error("a six-minute-old reading is still coloured in the CO₂ layer")
	}
	if _, shown := co2.Values["level0/A109"]; !shown {
		t.Error("the healthy room dropped out of the CO₂ layer")
	}
	alerts := b.Alerts()
	if len(alerts) != 1 || alerts[0].Severity != "warning" || alerts[0].Room != "1570" || !strings.Contains(alerts[0].Message, "10:00") {
		t.Fatalf("alerts: %+v", alerts)
	}
	b.Observe(obs("level0/1570", rooms.CO2, 610, 7)) // it comes back
	if got := b.Alerts(); len(got) != 0 {
		t.Errorf("the alert stayed after the sensor returned: %s", titles(got))
	}
}

// T-09 on the dashboard: the override is a critical alert for as long as it
// lasts, and sorts first.
func TestOverrideAndFallbackAlerts(t *testing.T) {
	b := New()
	b.Observe(obs("level0/1570", rooms.CO2, 1140, 0))
	b.Event(msg.Event{Room: "level0/A109", ModelTime: at(0), Actuator: rooms.Damper, Kind: "ttl_expired"})
	b.Event(msg.Event{Room: "level0/1570", ModelTime: at(0), Actuator: rooms.Damper, Kind: "override_co2", Detail: "CO₂ 1140 ppm > 1100"})
	alerts := b.Alerts()
	if len(alerts) != 2 || alerts[0].Severity != "critical" || alerts[0].Room != "1570" || alerts[1].Severity != "warning" {
		t.Fatalf("alerts: %s", titles(alerts))
	}
	b.Event(msg.Event{Room: "level0/1570", ModelTime: at(3), Actuator: rooms.Damper, Kind: "override_released"})
	b.Decide(msg.Decision{Room: "level0/A109", ModelTime: at(3), Mode: "normal", AirflowTarget: ptr(10), Status: 202})
	if got := b.Alerts(); len(got) != 0 {
		t.Errorf("alerts after release and a new accepted command: %s", titles(got))
	}
}

func TestModeAlerts(t *testing.T) {
	b := New()
	for i := range 3 {
		b.Decide(msg.Decision{Room: fmt.Sprintf("level0/R%d", i), ModelTime: at(0), Mode: "no_forecast", AirflowTarget: ptr(10), Status: 202})
	}
	b.Decide(msg.Decision{Room: "level0/1570", ModelTime: at(0), Mode: "degraded_occupancy", AirflowTarget: ptr(10), Status: 202})
	alerts := b.Alerts()
	// One building-wide notice for the missing model, not one per room.
	if len(alerts) != 2 || alerts[0].Severity != "warning" || alerts[1].ID != "no-forecast" || !strings.Contains(alerts[1].Message, "3 rooms") {
		t.Fatalf("alerts: %+v", alerts)
	}
}

func TestAlertsAreCapped(t *testing.T) {
	b := New()
	for i := range 150 {
		b.Event(msg.Event{Room: fmt.Sprintf("level0/R%03d", i), ModelTime: at(0), Actuator: rooms.Damper, Kind: "ttl_expired"})
	}
	alerts := b.Alerts()
	if len(alerts) != MaxAlerts || !strings.Contains(alerts[MaxAlerts-1].Title, "51 more") {
		t.Fatalf("%d alerts, last %q", len(alerts), alerts[len(alerts)-1].Title)
	}
	ids := map[string]bool{}
	for _, a := range alerts {
		if ids[a.ID] {
			t.Fatalf("duplicate alert id %q: BuildSim would reject the whole list", a.ID)
		}
		ids[a.ID] = true
	}
}

// A new experiment moves the model clock back; yesterday's picture must not
// linger as stale alerts.
func TestClockMovedBackStartsOver(t *testing.T) {
	b := New()
	b.Observe(obs("level0/1570", rooms.CO2, 640, 0))
	b.Event(msg.Event{Room: "level0/1570", ModelTime: at(0), Actuator: rooms.Damper, Kind: "ttl_expired"})
	b.Observe(obs("level0/A109", rooms.CO2, 450, -600)) // ten hours earlier
	if got := b.Alerts(); len(got) != 0 {
		t.Errorf("alerts from before the clock moved: %s", titles(got))
	}
	co2, _ := find(b.Layers(nil), "co2")
	if len(co2.Values) != 1 || co2.Values["level0/A109"] != 450 {
		t.Errorf("CO₂ layer after the clock moved back: %v", co2.Values)
	}
}

// The colour rules, checked rather than eyeballed: a "how much" ramp gets
// steadily darker, and a "which side of the target" scale is lightest exactly
// at the target, with the target in the middle of its range.
func TestPalettes(t *testing.T) {
	seq := lightnessAlong(sequential)
	for i := 1; i < len(seq); i++ {
		if seq[i] >= seq[i-1] {
			t.Fatalf("the sequential ramp doesn't darken at step %d: %v", i, seq)
		}
	}
	div := lightnessAlong(diverging)
	mid := len(div) / 2
	for i := range div {
		if i != mid && div[i] >= div[mid] {
			t.Fatalf("the diverging scale isn't lightest at its midpoint: %v", div)
		}
	}
	if math.Abs(div[0]-div[len(div)-1]) > 0.08 {
		t.Errorf("the two ends of the diverging scale differ in lightness: %.2f vs %.2f", div[0], div[len(div)-1])
	}
	b := New()
	b.Observe(obs("level0/1570", rooms.CO2, 640, 0))
	b.Observe(obs("level0/1570", rooms.Temp, 21, 0))
	layers := b.Layers(nil)
	for id, target := range map[string]float64{"co2": CO2Limit, "temperature": ComfortCentre} {
		l, _ := find(layers, id)
		if (l.Minimum+l.Maximum)/2 != target {
			t.Errorf("layer %s: the grey midpoint is at %v, want the target %v", id, (l.Minimum+l.Maximum)/2, target)
		}
	}
}

// lightnessAlong samples a palette the way the viewer draws it (straight-line
// interpolation between the stops) and returns the OKLab lightness of each
// sample, with a sample exactly on every stop.
func lightnessAlong(palette []string) []float64 {
	const perSegment = 4
	var out []float64
	for i := 0; i < len(palette)-1; i++ {
		a, b := rgb(palette[i]), rgb(palette[i+1])
		for s := 0; s < perSegment; s++ {
			f := float64(s) / perSegment
			out = append(out, oklabL(a[0]+(b[0]-a[0])*f, a[1]+(b[1]-a[1])*f, a[2]+(b[2]-a[2])*f))
		}
	}
	last := rgb(palette[len(palette)-1])
	return append(out, oklabL(last[0], last[1], last[2]))
}

func rgb(hex string) [3]float64 {
	var r, g, b int
	fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	return [3]float64{float64(r), float64(g), float64(b)}
}

func oklabL(r, g, b float64) float64 {
	lin := func(c float64) float64 {
		c /= 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	r, g, b = lin(r), lin(g), lin(b)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return 0.2104542553*l + 0.7936177850*m - 0.0040720468*s
}
