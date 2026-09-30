// Command controller decides the airflow per room from the sensor readings
// and commands the damper actuator (FR-3). This first slice polls BuildSim
// (D-3, option A) and implements the constant and reactive policies (FR-9);
// prediction comes later.
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
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/actuator"
	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/site"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

// Reactive control: airflow rises linearly from the lowest allowed flow at
// ReactiveLow ppm to maximum at ReactiveHigh ppm.
const (
	ReactiveLow  = 600.0
	ReactiveHigh = 1000.0
	CommandTTL   = 180 // model seconds: three missed readings
)

type controller struct {
	policy string
	keys   []rooms.Key
	sizes  map[rooms.Key]sizing.Room
	bs     *buildsim.Client
	damper string
	http   *http.Client
	runID  string
	seen   map[rooms.Key]time.Time // timestamp of the last CO₂ reading acted on
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	policy := env.String("POLICY", "reactive")
	if policy != "reactive" && policy != "constant" {
		log.Fatalf("POLICY=%q: this slice has constant and reactive", policy)
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
		keys:   keys,
		sizes:  sizes,
		bs:     bs,
		damper: strings.TrimRight(env.String("DAMPER_URL", "http://actuator-damper:8080"), "/"),
		http:   &http.Client{Timeout: 500 * time.Millisecond},
		runID:  env.String("RUN_ID", "dev"),
		seen:   map[rooms.Key]time.Time{},
	}
	log.Printf("policy %s for %d rooms", policy, len(keys))

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.tick(ctx)
		}
	}
}

// tick acts once per new CO₂ reading. A room whose sensor goes quiet gets no
// commands, so its damper's TTL expires and it falls back to design flow:
// the degraded mode for missing CO₂ (FR-7) needs nothing from the controller.
func (c *controller) tick(ctx context.Context) {
	readings, err := c.readCO2(ctx)
	if err != nil {
		log.Printf("read sensors: %v", err)
		return
	}
	for _, k := range c.keys {
		r, ok := readings[k]
		if !ok || !r.at.After(c.seen[k]) {
			continue
		}
		c.seen[k] = r.at
		target := c.decide(k, r.co2)
		c.command(ctx, k, r.co2, target)
	}
}

type reading struct {
	co2 float64
	at  time.Time
}

func (c *controller) readCO2(ctx context.Context) (map[rooms.Key]reading, error) {
	want := map[string]rooms.Key{}
	levels := map[string]bool{}
	for _, k := range c.keys {
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
// apply it twice (IF-8).
func (c *controller) command(ctx context.Context, k rooms.Key, co2, target float64) {
	cmd := actuator.Command{
		CmdID:  newID(),
		Value:  target,
		Unit:   "l/s",
		TTLs:   CommandTTL,
		Reason: fmt.Sprintf("%s: CO₂ %.0f ppm", c.policy, co2),
	}
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
		c.logDecision(k, co2, cmd, resp.StatusCode, res)
		return
	}
	log.Printf("%s: command %s gave up after 3 attempts", k, cmd.CmdID)
}

// logDecision writes the decision record (IF-6) to our log until storage and
// MQTT exist.
func (c *controller) logDecision(k rooms.Key, co2 float64, cmd actuator.Command, status int, res actuator.Result) {
	b, _ := json.Marshal(map[string]any{
		"run_id":            c.runID,
		"room":              k.String(),
		"policy":            c.policy,
		"mode":              "normal",
		"observed":          map[string]float64{"co2": co2},
		"airflow_target_ls": cmd.Value,
		"cmd_id":            cmd.CmdID,
		"status":            status,
		"applied_target":    res.Target,
		"clamped":           res.Clamped,
		"reason":            cmd.Reason,
	})
	log.Printf("decision %s", b)
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
