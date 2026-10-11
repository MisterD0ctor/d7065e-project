package dashboard

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

var (
	r1570 = rooms.Key{Level: "level0", Name: "1570"}
	rA109 = rooms.Key{Level: "level0", Name: "A109"}
	rA123 = rooms.Key{Level: "level0", Name: "A123"}
)

// same compares a column with the wanted values; NaN matches NaN.
func same(got Column, want ...float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i, w := range want {
		if g := float64(got[i]); g != w && !(math.IsNaN(g) && math.IsNaN(w)) {
			return false
		}
	}
	return true
}

var none = math.NaN()

func airflow(room string, min int, target float64, status int, expected *float64) msg.Decision {
	return msg.Decision{
		Room: room, ModelTime: at(min), Policy: "predictive", Mode: "normal",
		AirflowTarget: &target, AppliedTarget: target, PredictedOccupancy: expected, Status: status,
	}
}

// reached is a damper reporting its airflow.
func reached(room string, min int, flow float64) msg.State {
	return msg.State{Room: room, ModelTime: at(min), Actuator: rooms.Damper, Value: flow, Unit: "l/s"}
}

func heating(room string, min int, setpoint float64) msg.Decision {
	return msg.Decision{Room: room, ModelTime: at(min), Mode: "normal", SetpointTarget: &setpoint, AppliedTarget: setpoint, Status: 202}
}

// The graphs' data: one value per model minute, a hole where nothing was
// seen. A command the actuator refused leaves no target, and a damper that
// has stopped reporting leaves no airflow once its last report is stale.
func TestRoomHistoryIsAPointPerMinute(t *testing.T) {
	b := New()
	b.SetRooms(map[rooms.Key]sizing.Room{r1570: sizing.ForCapacity(100, 25)})
	room := r1570.String()

	b.Observe(obs(room, rooms.CO2, 600, 0))
	b.Observe(obs(room, rooms.Occupancy, 12, 0))
	b.Observe(obs(room, rooms.Temp, 21.3, 0))
	b.Decide(airflow(room, 0, 140, 202, ptr(15)))
	b.Decide(heating(room, 0, 21))
	b.State(reached(room, 0, 120))
	b.Tick()

	b.Observe(obs(room, rooms.Occupancy, 13, 1)) // the CO₂ reading of this minute was lost
	b.Decide(airflow(room, 1, 150, 0, nil))      // and the command didn't get through
	b.State(reached(room, 1, 130))
	b.Tick()

	// The damper goes quiet. Its last report stands for as long as a reading
	// is valid, three minutes, and then the line stops.
	for min := 2; min <= 5; min++ {
		b.Observe(obs(room, rooms.CO2, 640, min))
		b.Tick()
	}

	h, ok := b.RoomHistory(r1570, 60)
	if !ok || h.Start != ten.Unix() || h.Step != 60 || h.Size.Capacity != 25 {
		t.Fatalf("history: ok=%v start=%d step=%d size=%+v", ok, h.Start, h.Step, h.Size)
	}
	for name, c := range map[string]struct {
		got  Column
		want []float64
	}{
		"co2":      {h.CO2, []float64{600, none, 640, 640, 640, 640}},
		"people":   {h.People, []float64{12, 13, none, none, none, none}},
		"expected": {h.Expected, []float64{15, none, none, none, none, none}},
		"target":   {h.Target, []float64{140, none, none, none, none, none}},
		"flow":     {h.Flow, []float64{120, 130, 130, 130, 130, none}},
		"setpoint": {h.Setpoint, []float64{21, none, none, none, none, none}},
	} {
		if !same(c.got, c.want...) {
			t.Errorf("%s: %v, want %v", name, c.got, c.want)
		}
	}
	if float64(h.Temp[0]) < 21.29 || float64(h.Temp[0]) > 21.31 {
		t.Errorf("temperature: %v", h.Temp)
	}
	js, _ := json.Marshal(h)
	if !strings.Contains(string(js), `"co2":[600,null,640,640,640,640]`) {
		t.Errorf("a missing minute should be null in JSON: %s", js)
	}
	if _, ok := b.RoomHistory(rA109, 60); ok {
		t.Error("history for a room nobody has heard from")
	}
}

// A week is held, and the oldest minutes go as new ones come.
func TestHistoryHoldsAWeek(t *testing.T) {
	b := New()
	room := r1570.String()
	for min := 0; min <= 8*24*60; min += 30 {
		b.Observe(obs(room, rooms.CO2, float64(min), min))
	}
	h, _ := b.RoomHistory(r1570, 1<<30)
	if len(h.CO2) != Keep {
		t.Fatalf("%d minutes held, want %d", len(h.CO2), Keep)
	}
	newest := 8 * 24 * 60
	if got := h.CO2[len(h.CO2)-1]; got != float32(newest) {
		t.Errorf("newest point %v, want %v", got, newest)
	}
	if want := ten.Unix() + int64(newest-Keep+1)*60; h.Start != want {
		t.Errorf("start %d, want %d", h.Start, want)
	}
	if recent, _ := b.RoomHistory(r1570, 60); len(recent.CO2) != 60 {
		t.Errorf("the last hour is %d points", len(recent.CO2))
	}
}

// After a restart viz fills the graphs from storage. Old records must not
// reach the current picture: not the layers, not the alerts, and not the
// rule that an older model time means the clock was moved back.
func TestBackfillOnlyTouchesTheHistory(t *testing.T) {
	b := New()
	room := r1570.String()
	b.Observe(obs(room, rooms.CO2, 1150, 0)) // live
	b.Event(msg.Event{Room: room, ModelTime: at(0), Actuator: rooms.Damper, Kind: "override_co2", Detail: "CO₂ 1150 ppm > 1100"})

	b.BackfillObservation(obs(room, rooms.CO2, 500, -120))
	b.BackfillDecision(airflow(room, -120, 35, 202, ptr(0)))
	b.BackfillObservation(obs(room, rooms.CO2, 1, -Keep-5)) // older than is kept

	if got := titles(b.Alerts()); got != "critical: CO₂ override in 1570" {
		t.Errorf("alerts after the backfill: %s", got)
	}
	co2, _ := find(b.Layers(), "co2")
	if co2.Values[room] != 1150 {
		t.Errorf("CO₂ layer shows %v, want the live 1150", co2.Values[room])
	}
	h, _ := b.RoomHistory(r1570, Keep)
	if len(h.CO2) != 121 || h.CO2[0] != 500 || h.CO2[120] != 1150 || h.Target[0] != 35 || h.Expected[0] != 0 {
		t.Errorf("history: %d points, co2 %v … %v, target %v", len(h.CO2), h.CO2[0], h.CO2[len(h.CO2)-1], h.Target[0])
	}
}

// The building's totals: sums over the rooms, with a room's last value
// standing in for a lost reading (a missed count is not people leaving),
// but not for longer than a reading stays valid.
func TestBuildingHistory(t *testing.T) {
	b := New()
	sizes := map[rooms.Key]sizing.Room{
		r1570: sizing.ForCapacity(100, 25), rA109: sizing.ForCapacity(12, 1), rA123: sizing.ForCapacity(64, 32),
	}
	b.SetRooms(sizes)
	for k, v := range map[rooms.Key][2]float64{r1570: {10, 800}, rA109: {1, 500}, rA123: {30, 600}} {
		b.Observe(obs(k.String(), rooms.Occupancy, v[0], 0))
		b.Observe(obs(k.String(), rooms.CO2, v[1], 0))
	}
	for k, flow := range map[rooms.Key]float64{r1570: 100, rA109: 10, rA123: 240} {
		b.State(reached(k.String(), 0, flow))
	}
	b.Tick()
	// From here on only 1570 reports.
	for min := 1; min <= 5; min++ {
		b.Observe(obs(r1570.String(), rooms.Occupancy, 12, min))
		b.Observe(obs(r1570.String(), rooms.CO2, 820, min))
	}
	o := b.Overview(60)
	h := o.Building
	if !same(h.People, 41, 43, 43, 43, 12, 12) {
		t.Errorf("people: %v, want 41, then 43 while the others' last counts stand in, then 12", h.People)
	}
	if !same(h.CO2Max, 800, 820, 820, 820, 820, 820) || !same(h.CO2Median, 600, 600, 600, 600, 820, 820) {
		t.Errorf("CO₂ highest %v, middle room %v", h.CO2Max, h.CO2Median)
	}
	if want := 350 * KWPerLs; math.Abs(float64(h.PowerKW[0])-want) > 1e-3 {
		t.Errorf("ventilation power %v kW at 350 l/s, want %.3f", h.PowerKW[0], want)
	}
	if got, want := KWPerLs*1000, 13.5; math.Abs(got-want) > 1e-9 {
		t.Errorf("%v kW per m³/s, want 13.5 (1.5 fan + 12 air heating)", got)
	}
	design := sizes[r1570].Design + sizes[rA109].Design + sizes[rA123].Design
	if math.Abs(o.ConstantKW-design*KWPerLs) > 1e-9 {
		t.Errorf("constant-flow power %v, want %v", o.ConstantKW, design*KWPerLs)
	}
}

// An alert's life in the log and in the room's history. The ticks here are
// two model minutes apart, as at factor 120: the minute in between is filled.
func TestAlertLogAndSeverity(t *testing.T) {
	b := New()
	room := r1570.String()
	b.Observe(obs(room, rooms.CO2, 1090, 0))
	b.Tick()
	b.Event(msg.Event{Room: room, ModelTime: at(1), Actuator: rooms.Damper, Kind: "override_co2", Detail: "CO₂ 1140 ppm > 1100"})
	b.Observe(obs(room, rooms.CO2, 1140, 1))
	b.Tick()
	b.Observe(obs(room, rooms.CO2, 1120, 3))
	b.Tick()
	b.Event(msg.Event{Room: room, ModelTime: at(4), Actuator: rooms.Damper, Kind: "override_released"})
	b.Observe(obs(room, rooms.CO2, 1080, 4))
	b.Tick()

	h, _ := b.RoomHistory(r1570, 60)
	if got := h.Severity; len(got) != 5 || got[0] != 0 || got[1] != 2 || got[2] != 2 || got[3] != 2 || got[4] != 0 {
		t.Errorf("severity per minute: %v, want [0 2 2 2 0]", got)
	}
	log := b.Overview(60).Log
	if len(log) != 2 || !log[0].Cleared || log[0].ModelTime != at(4) || log[1].Cleared || log[1].ModelTime != at(1) ||
		log[1].Severity != "critical" || !strings.Contains(log[1].Message, "1140 ppm") {
		t.Errorf("log, newest first: %+v", log)
	}
	if len(h.Log) != 2 {
		t.Errorf("the room's own log has %d entries", len(h.Log))
	}
}

// The overview lists every registered room, heard from or not, and shows no
// value where there is no trustworthy reading.
func TestOverviewListsEveryRegisteredRoom(t *testing.T) {
	b := New()
	b.SetRooms(map[rooms.Key]sizing.Room{r1570: sizing.ForCapacity(100, 25), rA109: sizing.ForCapacity(12, 1)})
	room := r1570.String()
	b.Observe(obs(room, rooms.CO2, 512, 0))
	b.Observe(obs(room, rooms.Occupancy, 7, 0))
	d := airflow(room, 0, 210, 202, ptr(9))
	d.Mode, d.Fault = "degraded_co2", "CO₂ sensor has read 512 ppm 5 times in a row"
	b.Decide(d)
	b.State(reached(room, 0, 210))
	b.Tick()

	o := b.Overview(60)
	if len(o.Rooms) != 2 || o.Rooms[0].Name != "1570" || o.Rooms[1].Name != "A109" {
		t.Fatalf("rooms: %+v", o.Rooms)
	}
	full, quiet := o.Rooms[0], o.Rooms[1]
	if full.CO2 != nil {
		t.Errorf("a stuck CO₂ sensor's value is shown: %v", *full.CO2)
	}
	if full.People == nil || *full.People != 7 || full.Expected == nil || *full.Expected != 9 || full.FlowLs == nil || *full.FlowLs != 210 {
		t.Errorf("room 1570: %+v", full)
	}
	if full.Severity != 1 || full.Capacity != 25 || len(full.Spark) != sparkMinutes {
		t.Errorf("room 1570: severity %d, capacity %d, %d sparkline points", full.Severity, full.Capacity, len(full.Spark))
	}
	if quiet.CO2 != nil || quiet.People != nil || quiet.FlowLs != nil || quiet.Severity != 0 || quiet.Capacity != 1 {
		t.Errorf("room A109, never heard from: %+v", quiet)
	}
	if o.Policy != "predictive" || o.ModelTime != at(0) || o.Limits.CO2Limit != 1000 {
		t.Errorf("overview: policy %q, model time %q, limits %+v", o.Policy, o.ModelTime, o.Limits)
	}
	js, _ := json.Marshal(o)
	if !strings.Contains(string(js), `"alerts":[{`) || strings.Contains(string(js), `:null,"log"`) {
		t.Errorf("overview JSON: %.300s", js)
	}
}
