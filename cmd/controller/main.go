// Command controller decides the airflow and heating setpoint per room (FR-3).
// It learns the building from the registry: which rooms have a CO₂ sensor and
// a damper, where to reach each actuator, and each room's sizing (IF-12). It
// takes the observations from MQTT (IF-4) and falls back to polling BuildSim
// when the broker is silent (D-3). Each decision is published for storage and
// viz (IF-6). This version has the constant and reactive policies (FR-9).
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/actuator"
	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/devreg"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/mqttx"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/registry"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

const (
	// Reactive ventilation: airflow rises linearly from the lowest allowed
	// flow at ReactiveLow ppm to maximum at ReactiveHigh ppm.
	ReactiveLow  = 600.0
	ReactiveHigh = 1000.0

	// Heating: comfort setpoint in occupied hours, setback otherwise.
	ComfortSetpoint = 21.0
	SetbackSetpoint = 18.0

	CommandTTL   = 300 // model seconds: five missed readings
	StaleAfter   = 180 * time.Second
	RefreshPlant = 30 * time.Second // how often to re-read the registry
)

// plant is what is installed in one room.
type plant struct {
	size       sizing.Room
	damper     string   // endpoint; "" = none installed or not checked in
	heating    string   // endpoint
	co2Sensors []string // device ids
	tempSens   []string
}

type controller struct {
	policy string
	reg    *devreg.Client
	bs     *buildsim.Client
	mq     *mqttx.Client
	http   *http.Client
	runID  string
	dedup  *mqttx.Dedup

	silence mqttx.Silence // when to stop waiting for MQTT and poll BuildSim

	mu          sync.Mutex
	lastRefresh time.Time // real time of the last registry read
	plants      map[rooms.Key]plant
	newest      time.Time            // newest model time seen on any observation
	polled      map[string]time.Time // BuildSim timestamp of the last polled reading acted on, by device
	fallback    bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	policy := env.String("POLICY", "reactive")
	if policy != "reactive" && policy != "constant" {
		log.Fatalf("POLICY=%q: want constant or reactive", policy)
	}
	c := &controller{
		policy: policy,
		reg:    devreg.New(env.String("REGISTRY_URL", "http://registry:8080")),
		bs:     buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090")),
		mq:     mqttx.Connect("controller"),
		http:   &http.Client{Timeout: 500 * time.Millisecond},
		runID:  env.String("RUN_ID", "dev"),
		dedup:  mqttx.NewDedup(),
		plants: map[rooms.Key]plant{},
		polled: map[string]time.Time{},
	}
	c.refreshPlants(ctx)
	c.mq.Subscribe("obs/+/+/"+rooms.CO2, func(_ string, p []byte) { c.onObservation(ctx, p) })
	c.mq.Subscribe("obs/+/+/"+rooms.Temp, func(_ string, p []byte) { c.onObservation(ctx, p) })
	log.Printf("policy %s", policy)

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	refresh := time.NewTicker(RefreshPlant)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-refresh.C:
			c.refreshPlants(ctx)
		case <-tick.C:
			c.pollIfSilent(ctx)
		}
	}
}

// refreshPlants rebuilds the room list from the registry. A registry outage
// keeps the last known plant: the controller doesn't forget the building.
func (c *controller) refreshPlants(ctx context.Context) {
	devs, err := c.reg.List(ctx, registry.Filter{Status: "active"})
	if err != nil {
		log.Printf("registry: %v (keeping %d known rooms)", err, len(c.plants))
		return
	}
	plants := map[rooms.Key]plant{}
	for _, d := range devs {
		k, err := rooms.Parse(d.Room)
		if err != nil {
			continue
		}
		p := plants[k]
		p.size = d.Size
		switch d.Kind {
		case rooms.Damper:
			p.damper = d.Endpoint
		case rooms.Heating:
			p.heating = d.Endpoint
		case rooms.CO2:
			p.co2Sensors = append(p.co2Sensors, d.ID)
		case rooms.Temp:
			p.tempSens = append(p.tempSens, d.ID)
		}
		plants[k] = p
	}
	c.mu.Lock()
	changed := len(plants) != len(c.plants)
	c.plants = plants
	c.lastRefresh = time.Now()
	c.mu.Unlock()
	if changed {
		log.Printf("controlling %d rooms", len(plants))
	}
}

func (c *controller) plant(k rooms.Key) (plant, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.plants[k]
	return p, ok
}

func (c *controller) onObservation(ctx context.Context, payload []byte) {
	var o msg.Observation
	if err := json.Unmarshal(payload, &o); err != nil {
		log.Printf("observation: %v", err)
		return
	}
	k, err := rooms.Parse(o.Room)
	if err != nil || !c.dedup.Fresh(o.SensorID, o.Seq) {
		return
	}
	t, err := time.Parse(time.RFC3339, o.ModelTime)
	if err != nil {
		return
	}
	c.silence.Arrived(time.Now())

	c.mu.Lock()
	if c.fallback {
		log.Printf("observations back on MQTT; stopped polling BuildSim")
		c.fallback = false
	}
	if t.After(c.newest) {
		c.newest = t
	}
	stale := c.newest.Sub(t) > StaleAfter
	c.mu.Unlock()
	if stale {
		log.Printf("%s: dropping stale %s reading from %s", k, o.Kind, o.ModelTime)
		return
	}
	c.act(ctx, k, o.Kind, o.Value, t, o.ModelTime)
}

// pollIfSilent is the fallback path: with the broker down or silent, read the
// sensors from BuildSim and act on readings not seen before. BuildSim has no
// model time, so these commands carry no issued_at, and the heating rule uses
// the last model time seen (IF-8).
func (c *controller) pollIfSilent(ctx context.Context) {
	silent := c.silence.Silent(time.Now())
	c.mu.Lock()
	if silent && !c.fallback {
		log.Printf("observations on MQTT stopped; polling BuildSim")
		c.fallback = true
	}
	plants := make(map[rooms.Key]plant, len(c.plants))
	for k, p := range c.plants {
		plants[k] = p
	}
	last := c.newest
	c.mu.Unlock()
	if !silent {
		return
	}
	for k, p := range plants {
		for kind, ids := range map[string][]string{rooms.CO2: p.co2Sensors, rooms.Temp: p.tempSens} {
			for _, id := range ids {
				s, err := c.bs.SensorValue(ctx, registry.SensorID(id))
				if err != nil || s.Value == "" {
					continue
				}
				v, err := strconv.ParseFloat(s.Value, 64)
				if err != nil {
					continue
				}
				c.mu.Lock()
				isNew := s.Timestamp.After(c.polled[id])
				c.polled[id] = s.Timestamp
				c.mu.Unlock()
				if isNew {
					c.act(ctx, k, kind, v, last, "")
				}
			}
		}
	}
}

// act decides and commands one room for one reading. A room whose sensor goes
// quiet gets no commands, so its actuator's TTL expires and it falls back to
// design flow or 21 °C: the degraded mode for a missing sensor (FR-7) needs
// nothing from the controller.
func (c *controller) act(ctx context.Context, k rooms.Key, kind string, value float64, t time.Time, modelTime string) {
	p, ok := c.plant(k)
	if !ok {
		return // a room with sensors but nothing registered to control
	}
	var endpoint, unit, reason string
	var target float64
	switch kind {
	case rooms.CO2:
		endpoint, unit = p.damper, "l/s"
		target = c.airflow(p.size, value)
		reason = fmt.Sprintf("%s: CO₂ %.0f ppm", c.policy, value)
	case rooms.Temp:
		endpoint, unit = p.heating, "°C"
		target = SetbackSetpoint
		if sizing.OccupiedHours(t) {
			target = ComfortSetpoint
		}
		reason = fmt.Sprintf("schedule: %.1f °C measured", value)
	default:
		return
	}
	if endpoint == "" {
		return // actuator not installed or not checked in yet
	}
	cmd := actuator.Command{
		CmdID: newID(), Value: target, Unit: unit,
		IssuedAt: modelTime, TTLs: CommandTTL, Reason: reason,
	}
	status, res := c.command(ctx, endpoint, k, cmd)
	d := msg.Decision{
		RunID: c.runID, Room: k.String(), ModelTime: modelTime, Policy: c.policy,
		Mode: "normal", Observed: map[string]float64{kind: value},
		CmdID: cmd.CmdID, Status: status, AppliedTarget: res.Target,
		Clamped: res.Clamped, Reason: reason,
	}
	if kind == rooms.CO2 {
		d.AirflowTarget = &cmd.Value
	} else {
		d.SetpointTarget = &cmd.Value
	}
	c.mq.Publish(msg.DecisionTopic(k), d)
}

// airflow proposes a flow. It asks for as little as the policy wants and
// leaves the floors and the CO₂ override to the actuator (D-6).
func (c *controller) airflow(size sizing.Room, co2 float64) float64 {
	if c.policy == "constant" {
		return size.Design
	}
	frac := min(max((co2-ReactiveLow)/(ReactiveHigh-ReactiveLow), 0), 1)
	return size.Empty + frac*(size.Max-size.Empty)
}

// command sends one command, retrying the same cmd_id so a lost reply can't
// apply it twice (IF-8). Status 0 means every attempt failed.
func (c *controller) command(ctx context.Context, endpoint string, k rooms.Key, cmd actuator.Command) (int, actuator.Result) {
	body, _ := json.Marshal(cmd)
	url := fmt.Sprintf("%s/rooms/%s/%s/command", endpoint, k.Level, k.Name)
	for attempt := 1; attempt <= 3; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			log.Printf("%s: command attempt %d: %v", k, attempt, err)
			continue
		}
		var res actuator.Result
		json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			log.Printf("%s: command attempt %d: status %d", k, attempt, resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusAccepted && !res.Duplicate {
			log.Printf("%s: command %s refused: %d %s", k, cmd.CmdID, resp.StatusCode, res.Reason)
		}
		return resp.StatusCode, res
	}
	log.Printf("%s: command %s gave up after 3 attempts", k, cmd.CmdID)
	// The actuator may have restarted somewhere else: ask the registry now
	// instead of at the next periodic refresh, at most every 5 s.
	c.mu.Lock()
	due := time.Since(c.lastRefresh) > 5*time.Second
	c.mu.Unlock()
	if due {
		c.refreshPlants(ctx)
	}
	return 0, actuator.Result{}
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
