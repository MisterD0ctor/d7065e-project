// Command controller decides the airflow per room from the sensor readings
// and commands the damper actuator (FR-3). It takes the CO₂ observations from
// MQTT (IF-4) and falls back to polling BuildSim when the broker is down or
// silent (D-3). Each decision is published for storage and viz (IF-6). This
// version has the constant and reactive policies (FR-9); prediction comes
// later.
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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/actuator"
	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/mqttx"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/site"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

// Reactive control: airflow rises linearly from the lowest allowed flow at
// ReactiveLow ppm to maximum at ReactiveHigh ppm.
const (
	ReactiveLow  = 600.0
	ReactiveHigh = 1000.0
	CommandTTL   = 300 // model seconds: five missed readings
	StaleAfter   = 180 * time.Second
)

type controller struct {
	policy string
	keys   map[rooms.Key]bool
	sizes  map[rooms.Key]sizing.Room
	bs     *buildsim.Client
	mq     *mqttx.Client
	damper string
	http   *http.Client
	runID  string
	dedup  *mqttx.Dedup

	silence mqttx.Silence // when to stop waiting for MQTT and poll BuildSim

	mu       sync.Mutex
	newest   time.Time               // newest model time seen on any observation
	polled   map[rooms.Key]time.Time // BuildSim timestamp of the last polled reading acted on
	fallback bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	policy := env.String("POLICY", "reactive")
	if policy != "reactive" && policy != "constant" {
		log.Fatalf("POLICY=%q: want constant or reactive", policy)
	}
	keys, err := rooms.ParseList(env.Must("ROOMS"))
	if err != nil {
		log.Fatal(err)
	}
	bs := buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090"))
	// occupancysim is read once, for room capacities; the controller never
	// reads its clock or its occupancy.
	sizes, err := site.Sizes(ctx, bs, clock.New(env.String("CLOCK_URL", "http://occupancysim:8081")), keys)
	if err != nil {
		log.Fatal(err)
	}
	c := &controller{
		policy: policy,
		keys:   map[rooms.Key]bool{},
		sizes:  sizes,
		bs:     bs,
		mq:     mqttx.Connect("controller"),
		damper: strings.TrimRight(env.String("DAMPER_URL", "http://actuator-damper:8080"), "/"),
		http:   &http.Client{Timeout: 500 * time.Millisecond},
		runID:  env.String("RUN_ID", "dev"),
		dedup:  mqttx.NewDedup(),
		polled: map[rooms.Key]time.Time{},
	}
	for _, k := range keys {
		c.keys[k] = true
	}
	c.mq.Subscribe("obs/+/+/"+rooms.CO2, func(_ string, payload []byte) { c.onObservation(ctx, payload) })
	log.Printf("policy %s for %d rooms", policy, len(keys))

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.pollIfSilent(ctx)
		}
	}
}

func (c *controller) onObservation(ctx context.Context, payload []byte) {
	var o msg.Observation
	if err := json.Unmarshal(payload, &o); err != nil {
		log.Printf("observation: %v", err)
		return
	}
	k, err := rooms.Parse(o.Room)
	if err != nil || !c.keys[k] || !c.dedup.Fresh(o.SensorID, o.Seq) {
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
		log.Printf("%s: dropping stale reading from %s", k, o.ModelTime)
		return
	}
	c.act(ctx, k, o.Value, o.ModelTime)
}

// pollIfSilent is the fallback path: with the broker down or silent, read
// the CO₂ sensors from BuildSim and act on readings not seen before. BuildSim
// has no model time, so these commands carry no issued_at (IF-8).
func (c *controller) pollIfSilent(ctx context.Context) {
	silent := c.silence.Silent(time.Now())
	c.mu.Lock()
	if silent && !c.fallback {
		log.Printf("observations on MQTT stopped; polling BuildSim")
		c.fallback = true
	}
	c.mu.Unlock()
	if !silent {
		return
	}
	readings, err := c.readCO2(ctx)
	if err != nil {
		log.Printf("read sensors: %v", err)
		return
	}
	for k, r := range readings {
		c.mu.Lock()
		isNew := r.at.After(c.polled[k])
		c.polled[k] = r.at
		c.mu.Unlock()
		if isNew {
			c.act(ctx, k, r.co2, "")
		}
	}
}

type reading struct {
	co2 float64
	at  time.Time
}

func (c *controller) readCO2(ctx context.Context) (map[rooms.Key]reading, error) {
	want := map[string]rooms.Key{}
	levels := map[string]bool{}
	for k := range c.keys {
		want[rooms.SensorID(rooms.CO2, k)] = k
		levels[k.Level] = true
	}
	out := map[rooms.Key]reading{}
	for level := range levels {
		eq, err := c.bs.Equipment(ctx, level)
		if err != nil {
			return nil, err
		}
		for _, e := range eq {
			for _, s := range e.Sensors {
				k, ok := want[s.ID]
				if !ok || s.Value == "" {
					continue
				}
				v, err := strconv.ParseFloat(s.Value, 64)
				if err != nil {
					log.Printf("%s: unparsable reading %q", s.ID, s.Value)
					continue
				}
				out[k] = reading{co2: v, at: s.Timestamp}
			}
		}
	}
	return out, nil
}

// act decides and commands one room. A room whose sensor goes quiet gets no
// commands, so its damper's TTL expires and it falls back to design flow:
// the degraded mode for missing CO₂ (FR-7) needs nothing from the controller.
func (c *controller) act(ctx context.Context, k rooms.Key, co2 float64, modelTime string) {
	cmd := actuator.Command{
		CmdID:    newID(),
		Value:    c.decide(k, co2),
		Unit:     "l/s",
		IssuedAt: modelTime,
		TTLs:     CommandTTL,
		Reason:   fmt.Sprintf("%s: CO₂ %.0f ppm", c.policy, co2),
	}
	status, res := c.command(ctx, k, cmd)
	c.mq.Publish(msg.DecisionTopic(k), msg.Decision{
		RunID:         c.runID,
		Room:          k.String(),
		ModelTime:     modelTime,
		Policy:        c.policy,
		Mode:          "normal",
		Observed:      map[string]float64{rooms.CO2: co2},
		AirflowTarget: cmd.Value,
		CmdID:         cmd.CmdID,
		Status:        status,
		AppliedTarget: res.Target,
		Clamped:       res.Clamped,
		Reason:        cmd.Reason,
	})
}

// decide proposes an airflow. It asks for as little as the policy wants and
// leaves the floors and the CO₂ override to the actuator (D-6).
func (c *controller) decide(k rooms.Key, co2 float64) float64 {
	size := c.sizes[k]
	if c.policy == "constant" {
		return size.Design
	}
	frac := min(max((co2-ReactiveLow)/(ReactiveHigh-ReactiveLow), 0), 1)
	return size.Empty + frac*(size.Max-size.Empty)
}

// command sends one command, retrying the same cmd_id so a lost reply can't
// apply it twice (IF-8). Status 0 means every attempt failed.
func (c *controller) command(ctx context.Context, k rooms.Key, cmd actuator.Command) (int, actuator.Result) {
	body, _ := json.Marshal(cmd)
	url := fmt.Sprintf("%s/rooms/%s/%s/command", c.damper, k.Level, k.Name)
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
	return 0, actuator.Result{}
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
