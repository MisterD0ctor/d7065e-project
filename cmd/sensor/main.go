// Command sensor is one sensor device (D-2). It knows only its own id and
// what kind of hardware it is; at boot it checks in with the registry (IF-12)
// to learn which room it was installed in, and does nothing until an
// installer has registered it. Then, once per sample period, it samples its
// room's truth from physics (IF-2), adds noise and any injected fault, writes
// the reading to BuildSim (IF-3) and publishes it on MQTT (IF-4).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/devreg"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/faults"
	"github.com/MisterD0ctor/d7065e-project/internal/mqttx"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/registry"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

const (
	// CheckInEvery is the heartbeat to the registry; it also notices retirement.
	CheckInEvery = 30 * time.Second
	// PollEvery is how often the sensor looks at physics, in real time.
	PollEvery = 250 * time.Millisecond
)

type truth struct {
	ModelTime string `json:"model_time"`
	Rooms     map[string]struct {
		CO2       float64 `json:"co2_ppm"`
		Temp      float64 `json:"temp_c"`
		Occupancy int     `json:"occupancy"`
	} `json:"rooms"`
}

type sensor struct {
	id      string
	kind    string
	room    rooms.Key
	reg     *devreg.Client
	bs      *buildsim.Client
	mq      *mqttx.Client
	physics string
	http    *http.Client
	sample  time.Duration
	inject  *faults.Injector
	rng     *rand.Rand
	runID   string
	last    time.Time // model time of the last reading
	retired bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	id := env.Must("DEVICE_ID")
	kind := env.Must("DEVICE_KIND") // what the hardware is; the registry must agree
	if !registry.IsSensor(kind) {
		log.Fatalf("DEVICE_KIND=%q: want co2, temp or occupancy", kind)
	}
	reg := devreg.New(env.String("REGISTRY_URL", "http://registry:8080"))
	dev, err := reg.WaitForCommissioning(ctx, id, "")
	if err != nil {
		return
	}
	if dev.Kind != kind {
		// The installer typed the wrong kind for this id. Reporting CO₂ as a
		// temperature would be worse than reporting nothing.
		log.Fatalf("device %s is a %s sensor but is registered as %s; fix the registry entry", id, kind, dev.Kind)
	}
	room, _ := rooms.Parse(dev.Room)
	log.Printf("device %s: %s sensor in %s", id, kind, room)

	s := &sensor{
		id:      id,
		kind:    kind,
		room:    room,
		reg:     reg,
		bs:      buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090")),
		mq:      mqttx.Connect(id),
		physics: strings.TrimRight(env.String("PHYSICS_URL", "http://physics:8080"), "/"),
		http:    &http.Client{Timeout: 500 * time.Millisecond},
		sample:  time.Duration(env.Float("SAMPLE_S", 60) * float64(time.Second)),
		inject:  faults.NewInjector(fault(env.String("FAULT", ""), room)),
		rng:     rand.New(rand.NewPCG(seed(id), uint64(env.Float("SEED", 1)))),
		runID:   env.String("RUN_ID", "dev"),
	}

	// Poll physics four times per physics tick. Polling at the same 1 Hz as
	// physics updates aliases: a sensor whose phase lines up with the update
	// sometimes reads one snapshot twice and then skips a minute (found in
	// the 50-room run: temp-0017 missed ~1 reading in 5).
	tick := time.NewTicker(PollEvery)
	defer tick.Stop()
	heartbeat := time.NewTicker(CheckInEvery)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			s.checkIn(ctx)
		case <-tick.C:
			if !s.retired {
				s.tick(ctx)
			}
		}
	}
}

// fault turns FAULT (e.g. "stuck", "dropout", "drift:+50") into a fault on
// this device's room.
func fault(spec string, room rooms.Key) []faults.Fault {
	if spec == "" {
		return nil
	}
	mode, rest, _ := strings.Cut(spec, ":")
	full := mode + ":" + room.String()
	if rest != "" {
		full += ":" + rest
	}
	fs, err := faults.Parse(full)
	if err != nil {
		log.Fatalf("FAULT=%q: %v", spec, err)
	}
	log.Printf("fault injected: %s", spec)
	return fs
}

// seed gives every device its own noise stream, repeatable from run to run.
func seed(id string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(id))
	return h.Sum64()
}

func (s *sensor) checkIn(ctx context.Context) {
	_, err := s.reg.CheckIn(ctx, s.id, "")
	switch {
	case errors.Is(err, devreg.ErrRetired), errors.Is(err, devreg.ErrNotInstalled):
		if !s.retired {
			log.Printf("device %s was removed from the registry; no longer reporting", s.id)
		}
		s.retired = true
	case err != nil:
		log.Printf("check-in: %v", err) // keep reporting; the registry may be restarting
	default:
		s.retired = false
	}
}

func (s *sensor) tick(ctx context.Context) {
	tr, err := s.fetchTruth(ctx)
	if err != nil {
		// No retry inside a sample: a sensor that can't sense goes quiet,
		// which is what a dead device looks like (IF-2).
		log.Printf("truth: %v", err)
		return
	}
	t, err := time.Parse(time.RFC3339, tr.ModelTime)
	if err != nil {
		log.Printf("truth: bad model_time %q", tr.ModelTime)
		return
	}
	if !s.last.IsZero() && t.Sub(s.last) < s.sample && !t.Before(s.last) {
		return
	}
	s.last = t
	room, ok := tr.Rooms[s.room.String()]
	if !ok {
		log.Printf("%s: not simulated by physics", s.room)
		return
	}
	var v float64
	switch s.kind {
	case rooms.CO2:
		v = room.CO2
	case rooms.Temp:
		v = room.Temp
	case rooms.Occupancy:
		v = float64(room.Occupancy)
	}
	v = faults.Noise(s.kind, v, s.rng)
	v, publish := s.inject.Apply(s.room, t, v)
	if !publish {
		return
	}
	value := format(s.kind, v)
	s.write(ctx, value)
	s.publish(t, value)
}

func (s *sensor) fetchTruth(ctx context.Context) (truth, error) {
	var tr truth
	u := s.physics + "/rooms?room=" + url.QueryEscape(s.room.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return tr, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return tr, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tr, fmt.Errorf("status %d", resp.StatusCode)
	}
	return tr, json.NewDecoder(resp.Body).Decode(&tr)
}

// write retries ×3 with 100 ms backoff (IF-3). A 404 means BuildSim has lost
// the equipment, e.g. after a restart; the registry recreates it within its
// sync period, so the next sample gets through.
func (s *sensor) write(ctx context.Context, value string) {
	id := registry.SensorID(s.id)
	for attempt := 1; attempt <= 3; attempt++ {
		err := s.bs.SetSensorValue(ctx, id, value)
		if err == nil {
			return
		}
		if buildsim.IsNotFound(err) {
			log.Printf("%s: not in BuildSim yet; the registry will recreate it", id)
			return
		}
		log.Printf("%s: write attempt %d: %v", id, attempt, err)
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("%s: reading dropped", id)
}

// publish sends the same reading on MQTT, after the BuildSim write: if the
// broker is down, BuildSim still has it for the fallback path (D-3).
func (s *sensor) publish(t time.Time, value string) {
	v, _ := strconv.ParseFloat(value, 64)
	s.mq.Publish(msg.ObsTopic(s.room, s.kind), msg.Observation{
		RunID:     s.runID,
		SensorID:  s.id,
		Room:      s.room.String(),
		Kind:      s.kind,
		Value:     v,
		Unit:      rooms.Sensors[s.kind].Unit,
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
