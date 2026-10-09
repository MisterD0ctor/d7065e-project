package dashboard

import (
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/roommodel"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

// The graphs (D-8, IF-11). The viewer's layers show the building now; the
// history here is what the web page plots: one point per room and model
// minute, of what the system saw and did. It is kept in memory, because
// storage answers a history query by reading the whole run (IF-10) and a
// page that redraws every two seconds can't wait for that. After a restart
// viz fills it again from storage (Backfill).

const (
	// Keep is how much history is held per room: a week of model minutes,
	// the span of an evaluation run. 50 rooms take about 16 MB.
	Keep = 7 * 24 * 60
	// MaxLog is how many alert changes the log holds.
	MaxLog = 300
	// CarryFor is how long a room's last value stands in for a missing one
	// when the building's totals are summed: a lost reading shouldn't show
	// as people leaving. It is the staleness limit, in minutes.
	CarryFor = int(StaleAfter / time.Minute)
	// fillFor is how many model minutes one Tick may cover. At factor 120 a
	// real second is two model minutes, and the minute in between gets the
	// same airflow and alerts rather than a hole.
	fillFor = 5

	CO2Override = sizing.OverrideCO2 // ppm, FR-6
	ComfortLow  = 20.0               // °C, NFR-2
	ComfortHigh = 24.0

	// KWPerLs is the power ventilation takes per l/s of airflow: the fans,
	// and heating outdoor air to the supply temperature. Those are the parts
	// of the evaluation's energy proxy (NFR-3) that follow from the airflow
	// alone, with the same constants. The radiators depend on the room's
	// true temperature, which the dashboard doesn't see, and are left out.
	KWPerLs = (roommodel.SpecificFanPower + roommodel.AirHeatCap*(roommodel.SupplyTemp-roommodel.OutdoorTemp)) / 1e6
)

var nan = float32(math.NaN())

// point is one model minute of one room. NaN means nothing that minute.
type point struct {
	co2, temp, people float32 // sensor readings
	expected          float32 // people the controller ventilated for
	target, flow      float32 // airflow the damper accepted, and reached; l/s
	setpoint          float32 // heating setpoint the valve accepted; °C
	severity          uint8   // worst alert in the room: 0 none, 1 warning, 2 critical
}

var blank = point{co2: nan, temp: nan, people: nan, expected: nan, target: nan, flow: nan, setpoint: nan}

// series is a room's history: one point per model minute, without holes.
type series struct {
	first int64 // model minute (Unix time / 60) of pts[0]
	pts   []point
}

func minute(t time.Time) int64 { return t.Unix() / 60 }

func (s *series) last() int64 { return s.first + int64(len(s.pts)) - 1 }

// at returns the point for a model minute, growing the series to reach it.
// It returns nil for a minute more than Keep before the newest.
func (s *series) at(m int64) *point {
	switch {
	case len(s.pts) == 0 || m-s.last() >= Keep:
		// Empty, or a jump that leaves nothing of the old week.
		s.first, s.pts = m, []point{blank}
	case m < s.first:
		// Older than anything held: a backfill after the live data began.
		if s.last()-m >= Keep {
			return nil
		}
		grown := make([]point, int(s.first-m), int(s.first-m)+len(s.pts))
		for i := range grown {
			grown[i] = blank
		}
		s.pts, s.first = append(grown, s.pts...), m
	case m > s.last():
		for n := m - s.last(); n > 0; n-- {
			s.pts = append(s.pts, blank)
		}
		if extra := len(s.pts) - Keep; extra > 0 {
			s.pts, s.first = s.pts[extra:], s.first+int64(extra)
		}
	}
	return &s.pts[m-s.first]
}

// get returns the point for a model minute, blank outside what is held.
func (s *series) get(m int64) point {
	if m < s.first || m > s.last() || len(s.pts) == 0 {
		return blank
	}
	return s.pts[m-s.first]
}

func (p *point) observe(o msg.Observation) {
	switch o.Kind {
	case rooms.CO2:
		p.co2 = float32(o.Value)
	case rooms.Temp:
		p.temp = float32(o.Value)
	case rooms.Occupancy:
		p.people = float32(o.Value)
	}
}

// decide notes what a decision led to. Only a command the actuator accepted
// (202) has a target; a refused or lost one changed nothing.
func (p *point) decide(d msg.Decision) {
	accepted := d.Status == 202
	switch {
	case d.AirflowTarget != nil:
		if d.PredictedOccupancy != nil {
			p.expected = float32(*d.PredictedOccupancy)
		}
		if accepted {
			p.target = float32(d.AppliedTarget)
		}
	case d.SetpointTarget != nil && accepted:
		p.setpoint = float32(d.AppliedTarget)
	}
}

// BackfillObservation adds an old reading to the history and nothing else:
// the current picture and the alerts are for live messages only.
func (b *Board) BackfillObservation(o msg.Observation) {
	t, err := time.Parse(time.RFC3339, o.ModelTime)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, r, ok := b.room(o.Room); ok {
		if p := r.hist.at(minute(t)); p != nil {
			p.observe(o)
		}
	}
}

// BackfillDecision is the same for an old decision.
func (b *Board) BackfillDecision(d msg.Decision) {
	t, err := time.Parse(time.RFC3339, d.ModelTime)
	if err != nil {
		return // decided in the REST fallback, without a model time
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, r, ok := b.room(d.Room); ok {
		if p := r.hist.at(minute(t)); p != nil {
			p.decide(d)
		}
	}
}

// LogEntry is an alert appearing or going away.
type LogEntry struct {
	ModelTime string `json:"model_time"`
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	Message   string `json:"message,omitempty"`
	Level     string `json:"level,omitempty"`
	Room      string `json:"room,omitempty"`
	Cleared   bool   `json:"cleared"`
}

var severityRank = map[string]uint8{"warning": 1, "critical": 2}

// Tick brings the history up to the present. flows is the airflow each
// damper has reached, in l/s, or nil if BuildSim could not be read. viz calls
// it once per real second.
func (b *Board) Tick(flows map[rooms.Key]float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if flows != nil {
		b.flows = flows
	}
	if b.newest.IsZero() {
		return
	}
	now := minute(b.newest)
	from := now
	if b.lastTick != 0 && b.lastTick < now && now-b.lastTick <= fillFor {
		from = b.lastTick + 1
	}
	b.lastTick = now

	alerts := b.current()
	worst := map[rooms.Key]uint8{}
	for _, a := range alerts {
		k := rooms.Key{Level: a.Level, Name: a.Room}
		worst[k] = max(worst[k], severityRank[a.Severity])
	}
	for k, r := range b.rooms {
		for m := from; m <= now; m++ {
			p := r.hist.at(m)
			if p == nil {
				continue
			}
			if f, ok := flows[k]; ok {
				p.flow = float32(f)
			}
			p.severity = max(p.severity, worst[k])
		}
	}

	// The log: what is new since the last tick, and what has gone.
	active := map[string]buildsim.Alert{}
	for _, a := range alerts {
		active[a.ID] = a
		if _, was := b.active[a.ID]; !was {
			b.logged(a, false)
		}
	}
	var gone []string
	for id := range b.active {
		if _, still := active[id]; !still {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		b.logged(b.active[id], true)
	}
	b.active = active
}

func (b *Board) logged(a buildsim.Alert, cleared bool) {
	e := LogEntry{
		ModelTime: msg.FormatTime(b.newest), Severity: a.Severity, Title: a.Title,
		Level: a.Level, Room: a.Room, Cleared: cleared,
	}
	if !cleared {
		e.Message = a.Message
	}
	b.log = append(b.log, e)
	if extra := len(b.log) - MaxLog; extra > 0 {
		b.log = b.log[extra:]
	}
}

// Column is one measure over time. A minute without a value is written as
// null in JSON.
type Column []float32

func (c Column) MarshalJSON() ([]byte, error) {
	out := make([]byte, 0, 6*len(c)+2)
	out = append(out, '[')
	for i, v := range c {
		if i > 0 {
			out = append(out, ',')
		}
		if v != v || math.IsInf(float64(v), 0) {
			out = append(out, "null"...)
		} else {
			out = strconv.AppendFloat(out, float64(v), 'f', -1, 32)
		}
	}
	return append(out, ']'), nil
}

// RoomHistory is one room over a span of model time: a value per minute from
// Start. The airflow and the setpoint are what the actuators accepted and
// reached; everything else is a sensor reading or the controller's forecast.
type RoomHistory struct {
	Room     string      `json:"room"`
	Size     sizing.Room `json:"size"`
	Start    int64       `json:"start"` // model time of the first point, Unix seconds
	Step     int         `json:"step"`  // seconds between points
	CO2      Column      `json:"co2"`
	People   Column      `json:"people"`
	Expected Column      `json:"expected"`
	Flow     Column      `json:"flow"`
	Target   Column      `json:"target"`
	Temp     Column      `json:"temp"`
	Setpoint Column      `json:"setpoint"`
	Severity []int       `json:"severity"`
	Log      []LogEntry  `json:"log"` // this room's alert changes, newest first
}

// end is the newest model minute anything is known about.
func (b *Board) end() int64 {
	end := int64(0)
	if !b.newest.IsZero() {
		end = minute(b.newest)
	}
	for _, r := range b.rooms {
		if len(r.hist.pts) > 0 {
			end = max(end, r.hist.last())
		}
	}
	return end
}

// RoomHistory returns the last minutes of a room, as far back as is held.
func (b *Board) RoomHistory(k rooms.Key, minutes int) (RoomHistory, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.rooms[k]
	if !ok {
		return RoomHistory{}, false
	}
	end := b.end()
	from := max(end-int64(min(minutes, Keep))+1, r.hist.first)
	n := 0
	if len(r.hist.pts) > 0 && from <= end {
		n = int(end - from + 1)
	}
	h := RoomHistory{
		Room: k.String(), Size: b.sizes[k], Start: from * 60, Step: 60,
		CO2: make(Column, n), People: make(Column, n), Expected: make(Column, n),
		Flow: make(Column, n), Target: make(Column, n), Temp: make(Column, n),
		Setpoint: make(Column, n), Severity: make([]int, n), Log: []LogEntry{},
	}
	for i := range n {
		p := r.hist.get(from + int64(i))
		h.CO2[i], h.People[i], h.Expected[i] = p.co2, p.people, p.expected
		h.Flow[i], h.Target[i], h.Temp[i], h.Setpoint[i] = p.flow, p.target, p.temp, p.setpoint
		h.Severity[i] = int(p.severity)
	}
	for i := len(b.log) - 1; i >= 0; i-- {
		if e := b.log[i]; e.Level == k.Level && e.Room == k.Name {
			h.Log = append(h.Log, e)
		}
	}
	return h, true
}

// BuildingHistory is the whole building over a span of model time.
type BuildingHistory struct {
	Start     int64  `json:"start"`
	Step      int    `json:"step"`
	People    Column `json:"people"`     // counted, summed over the rooms
	Expected  Column `json:"expected"`   // ventilated for, summed
	PowerKW   Column `json:"power_kw"`   // ventilation power from the reached airflow
	CO2Max    Column `json:"co2_max"`    // the highest reading in any room
	CO2Median Column `json:"co2_median"` // the reading of the middle room
}

func (b *Board) building(minutes int) BuildingHistory {
	end := b.end()
	from := end + 1
	for _, r := range b.rooms {
		if len(r.hist.pts) > 0 {
			from = min(from, r.hist.first)
		}
	}
	from = max(from, end-int64(min(minutes, Keep))+1)
	n := max(int(end-from+1), 0)
	h := BuildingHistory{
		Start: from * 60, Step: 60,
		People: make(Column, n), Expected: make(Column, n), PowerKW: make(Column, n),
		CO2Max: make(Column, n), CO2Median: make(Column, n),
	}
	if n == 0 {
		return h
	}
	// Sums start as "nothing known" and become numbers with the first room
	// that has a value, so a minute nobody reported is a hole, not a zero.
	sums := []Column{h.People, h.Expected, h.PowerKW}
	for _, c := range sums {
		for i := range c {
			c[i] = nan
		}
	}
	co2 := make([][]float32, n)
	for _, r := range b.rooms {
		// held is each measure's last value and how many minutes old it is.
		var held [4]float32
		age := [4]int{CarryFor + 1, CarryFor + 1, CarryFor + 1, CarryFor + 1}
		for m := from - int64(CarryFor); m <= end; m++ {
			p := r.hist.get(m)
			for j, v := range [4]float32{p.people, p.expected, p.flow, p.co2} {
				if v == v {
					held[j], age[j] = v, 0
				} else {
					age[j]++
				}
			}
			if m < from {
				continue
			}
			i := int(m - from)
			for j, c := range sums {
				if age[j] <= CarryFor {
					if c[i] != c[i] {
						c[i] = 0
					}
					c[i] += held[j]
				}
			}
			if age[3] <= CarryFor {
				co2[i] = append(co2[i], held[3])
			}
		}
	}
	for i := range n {
		h.PowerKW[i] *= KWPerLs // it was the summed airflow until here
		h.CO2Max[i], h.CO2Median[i] = nan, nan
		if v := co2[i]; len(v) > 0 {
			sort.Slice(v, func(a, c int) bool { return v[a] < v[c] })
			h.CO2Max[i], h.CO2Median[i] = v[len(v)-1], v[len(v)/2]
		}
	}
	return h
}

// RoomNow is a room's line in the overview. A value is absent when the
// reading is stale or the controller has found the sensor stuck.
type RoomNow struct {
	Room     string   `json:"room"`
	Level    string   `json:"level"`
	Name     string   `json:"name"`
	Capacity int      `json:"capacity"`
	DesignLs float64  `json:"design_ls"`
	MaxLs    float64  `json:"max_ls"`
	CO2      *float64 `json:"co2"`
	People   *float64 `json:"people"`
	Expected *float64 `json:"expected"`
	FlowLs   *float64 `json:"flow_ls"`
	Temp     *float64 `json:"temp"`
	Severity int      `json:"severity"` // 0 none, 1 warning, 2 critical
	Spark    Column   `json:"spark"`    // CO₂ over the last hour
}

// Limits are the lines the graphs draw, so the page needn't know them.
type Limits struct {
	CO2Limit    float64 `json:"co2_limit"`
	CO2Override float64 `json:"co2_override"`
	ComfortLow  float64 `json:"comfort_low"`
	ComfortHigh float64 `json:"comfort_high"`
}

// Overview is everything the building page shows.
type Overview struct {
	ModelTime  string           `json:"model_time"` // "" before the first message
	Policy     string           `json:"policy"`
	Limits     Limits           `json:"limits"`
	ConstantKW float64          `json:"constant_kw"` // ventilation power with every room at design flow
	Rooms      []RoomNow        `json:"rooms"`
	Building   BuildingHistory  `json:"building"`
	Alerts     []buildsim.Alert `json:"alerts"`
	Log        []LogEntry       `json:"log"` // newest first
}

const sparkMinutes = 60

// Overview returns the building page's data for the last minutes.
func (b *Board) Overview(minutes int) Overview {
	b.mu.Lock()
	defer b.mu.Unlock()
	o := Overview{
		Policy:   b.policy,
		Limits:   Limits{CO2Limit, CO2Override, ComfortLow, ComfortHigh},
		Building: b.building(minutes),
		Alerts:   b.current(),
		Rooms:    []RoomNow{}, Log: []LogEntry{},
	}
	if o.Alerts == nil {
		o.Alerts = []buildsim.Alert{}
	}
	end := b.end()
	if end > 0 {
		o.ModelTime = msg.FormatTime(time.Unix(end*60, 0).UTC())
	}
	worst := map[rooms.Key]uint8{}
	for _, a := range o.Alerts {
		k := rooms.Key{Level: a.Level, Name: a.Room}
		worst[k] = max(worst[k], severityRank[a.Severity])
	}

	// Every registered room is listed, heard from or not.
	keys := map[rooms.Key]bool{}
	for k := range b.sizes {
		keys[k] = true
	}
	for k := range b.rooms {
		keys[k] = true
	}
	for k := range keys {
		size := b.sizes[k]
		o.ConstantKW += size.Design * KWPerLs
		now := RoomNow{
			Room: k.String(), Level: k.Level, Name: k.Name,
			Capacity: size.Capacity, DesignLs: size.Design, MaxLs: size.Max,
			Severity: int(worst[k]), Spark: Column{},
		}
		if f, ok := b.flows[k]; ok {
			now.FlowLs = &f
		}
		if r, ok := b.rooms[k]; ok {
			for kind, dst := range map[string]**float64{rooms.CO2: &now.CO2, rooms.Occupancy: &now.People, rooms.Temp: &now.Temp} {
				if v, ok := r.obs[kind]; ok && b.fresh(v) && b.stuck(r, kind) == "" {
					*dst = &v.value
				}
			}
			if r.predicted != nil && b.fresh(*r.predicted) {
				now.Expected = &r.predicted.value
			}
			for m := end - sparkMinutes + 1; m <= end; m++ {
				now.Spark = append(now.Spark, r.hist.get(m).co2)
			}
		}
		o.Rooms = append(o.Rooms, now)
	}
	sort.Slice(o.Rooms, func(i, j int) bool { return o.Rooms[i].Room < o.Rooms[j].Room })
	for i := len(b.log) - 1; i >= 0; i-- {
		o.Log = append(o.Log, b.log[i])
	}
	return o
}
