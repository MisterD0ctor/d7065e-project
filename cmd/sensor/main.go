// Command sensor is a sensor gateway for one kind of sensor (KIND=co2, temp or
// occupancy) in every configured room (D-2). It samples the truth from
// physics (IF-2), adds noise and injected faults, writes the reading to
// BuildSim (IF-3) and publishes it on MQTT (IF-4). It registers its equipment
// at start, so a restart needs no manual steps (FR-11).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/faults"
	"github.com/MisterD0ctor/d7065e-project/internal/mqttx"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

type truth struct {
	ModelTime string `json:"model_time"`
	Rooms     map[string]struct {
		CO2       float64 `json:"co2_ppm"`
		Temp      float64 `json:"temp_c"`
		Occupancy int     `json:"occupancy"`
	} `json:"rooms"`
}

type gateway struct {
	kind    string
	keys    []rooms.Key
	bs      *buildsim.Client
	physics string
	http    *http.Client
	sample  time.Duration
	inject  *faults.Injector
	rng     *rand.Rand
	mq      *mqttx.Client
	runID   string
	last    time.Time // model time of the last published sample
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	kind := env.Must("KIND")
	if _, ok := rooms.Sensors[kind]; !ok {
		log.Fatalf("KIND=%q: want co2, temp or occupancy", kind)
	}
	keys, err := rooms.ParseList(env.Must("ROOMS"))
	if err != nil {
		log.Fatal(err)
	}
	fs, err := faults.Parse(env.String("FAULTS", ""))
	if err != nil {
		log.Fatal(err)
	}
	seed := uint64(env.Float("SEED", 1))
	g := &gateway{
		kind:    kind,
		keys:    keys,
		bs:      buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090")),
		physics: strings.TrimRight(env.String("PHYSICS_URL", "http://physics:8080"), "/"),
		http:    &http.Client{Timeout: 500 * time.Millisecond},
		sample:  time.Duration(env.Float("SAMPLE_S", 60) * float64(time.Second)),
		inject:  faults.NewInjector(fs),
		rng:     rand.New(rand.NewPCG(seed, uint64(len(kind)))),
		mq:      mqttx.Connect("sensor-" + kind),
		runID:   env.String("RUN_ID", "dev"),
	}
	for _, f := range fs {
		log.Printf("fault injected: %s on %s", f.Mode, f.Room)
	}

	g.register(ctx)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			g.tick(ctx)
		}
	}
}

// register creates this gateway's equipment, retrying until BuildSim answers.
func (g *gateway) register(ctx context.Context) {
	spec := rooms.Sensors[g.kind]
	var eq []buildsim.Equipment
	for _, k := range g.keys {
		eq = append(eq, buildsim.Equipment{
			ID:       rooms.EquipmentID(spec.EquipmentPrefix, k),
			Name:     fmt.Sprintf("%s sensor %s", g.kind, k),
			Type:     spec.EquipmentType,
			Category: "sensor",
			Level:    k.Level,
			Room:     k.Name,
			Status:   "running",
			Sensors: []buildsim.Sensor{{
				ID: rooms.SensorID(g.kind, k), Type: spec.SensorType,
				DataType: "text", Unit: spec.Unit,
			}},
			Actuators: []buildsim.Actuator{},
		})
	}
	for {
		err := g.bs.BulkCreate(ctx, eq)
		if err == nil {
			log.Printf("registered %d %s sensors", len(eq), g.kind)
			return
		}
		log.Printf("register: %v (retrying)", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (g *gateway) tick(ctx context.Context) {
	tr, err := g.fetchTruth(ctx)
	if err != nil {
		// No retry inside a sample: a gateway that can't see the air goes
		// quiet, which is what a dead device looks like (IF-2).
		log.Printf("truth: %v", err)
		return
	}
	t, err := time.Parse(time.RFC3339, tr.ModelTime)
	if err != nil {
		log.Printf("truth: bad model_time %q", tr.ModelTime)
		return
	}
	if !g.last.IsZero() && t.Sub(g.last) < g.sample && !t.Before(g.last) {
		return
	}
	g.last = t
	for _, k := range g.keys {
		room, ok := tr.Rooms[k.String()]
		if !ok {
			log.Printf("%s: missing from physics", k)
			continue
		}
		var v float64
		switch g.kind {
		case rooms.CO2:
			v = room.CO2
		case rooms.Temp:
			v = room.Temp
		case rooms.Occupancy:
			v = float64(room.Occupancy)
		}
		v = faults.Noise(g.kind, v, g.rng)
		v, publish := g.inject.Apply(k, t, v)
		if !publish {
			continue
		}
		value := format(g.kind, v)
		g.write(ctx, k, value)
		g.publish(k, t, value)
	}
}

func (g *gateway) fetchTruth(ctx context.Context) (truth, error) {
	var tr truth
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.physics+"/rooms", nil)
	if err != nil {
		return tr, err
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return tr, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tr, fmt.Errorf("status %d", resp.StatusCode)
	}
	return tr, json.NewDecoder(resp.Body).Decode(&tr)
}

// write retries ×3 with 100 ms backoff; a 404 means the equipment is gone, so
// it re-registers first (IF-3).
func (g *gateway) write(ctx context.Context, k rooms.Key, value string) {
	id := rooms.SensorID(g.kind, k)
	for attempt := range 3 {
		err := g.bs.SetSensorValue(ctx, id, value)
		if err == nil {
			return
		}
		if buildsim.IsNotFound(err) {
			log.Printf("%s: equipment missing, re-registering", id)
			g.register(ctx)
		}
		log.Printf("%s: write attempt %d: %v", id, attempt+1, err)
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("%s: reading dropped", id)
}

// publish sends the same reading on MQTT, after the BuildSim write: if the
// broker is down, BuildSim still has it for the fallback path (D-3).
func (g *gateway) publish(k rooms.Key, t time.Time, value string) {
	v, _ := strconv.ParseFloat(value, 64)
	g.mq.Publish(msg.ObsTopic(k, g.kind), msg.Observation{
		RunID:     g.runID,
		SensorID:  rooms.SensorID(g.kind, k),
		Room:      k.String(),
		Kind:      g.kind,
		Value:     v,
		Unit:      rooms.Sensors[g.kind].Unit,
		ModelTime: msg.FormatTime(t),
		Seq:       t.Unix(),
	})
}

func format(kind string, v float64) string {
	switch kind {
	case rooms.Temp:
		return strconv.FormatFloat(v, 'f', 1, 64)
	default:
		return strconv.FormatFloat(max(v, 0), 'f', 0, 64)
	}
}
