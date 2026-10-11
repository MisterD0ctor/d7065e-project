// Command sensor is one sensor device (D-2). It knows only its own id and
// what kind of hardware it is; at boot it checks in with the registry (IF-12)
// to learn which room it was installed in, and does nothing until an
// installer has registered it.
//
// It is the control system's only way to the building. Once per sample
// period it reads the value at its place in BuildSim (IF-3), where the
// physical model keeps it, adds its own measurement noise and any injected
// fault, and publishes the reading on MQTT (IF-4). It writes nothing to
// BuildSim: the value there is the building's, not the device's.
//
// A clock is a sensor too (kind "clock"). What it reads is the building's
// time, and it publishes that on time/model, which is how every other device
// knows what time it is (IF-1).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log"
	"math/rand/v2"
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
	// ClockPoll is how often a clock device looks at its value in BuildSim,
	// in real time: several times per model minute at factor 120.
	ClockPoll = 100 * time.Millisecond
	// ClockBeat is how often a clock device repeats the time when it has not
	// changed. The beat tells the other devices that the clock is alive and
	// the time standing still, which is not the same as the clock being gone.
	// It is also how a device that has just started learns the time: nothing
	// is retained on the broker, so no device can pick up a time left over
	// from an earlier run.
	ClockBeat = time.Second
)

// building is the part of BuildSim a sensor uses: its own value.
type building interface {
	SensorValue(ctx context.Context, sensorID string) (buildsim.Sensor, error)
}

type sensor struct {
	id      string
	kind    string
	room    rooms.Key
	reg     *devreg.Client
	bs      building
	mq      *mqttx.Client
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
		log.Fatalf("DEVICE_KIND=%q: want co2, temp, occupancy or clock", kind)
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
		id:     id,
		kind:   kind,
		room:   room,
		reg:    reg,
		bs:     buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090")),
		mq:     mqttx.Connect(id),
		sample: time.Duration(env.Float("SAMPLE_S", 60) * float64(time.Second)),
		inject: faults.NewInjector(fault(env.String("FAULT", ""), room)),
		rng:    rand.New(rand.NewPCG(seed(id), uint64(env.Float("SEED", 1)))),
		runID:  env.String("RUN_ID", "dev"),
	}
	if kind == rooms.Clock {
		s.runClock(ctx)
		return
	}

	// The building's time arrives on MQTT from the clock device. Only the
	// newest one matters: if the sensor is busy, older ones are dropped.
	times := make(chan msg.ModelTime, 1)
	s.mq.Subscribe(msg.TimeTopic, func(_ string, payload []byte) {
		var m msg.ModelTime
		if json.Unmarshal(payload, &m) != nil {
			return
		}
		select {
		case <-times:
		default:
		}
		select {
		case times <- m:
		default:
		}
	})
	heartbeat := time.NewTicker(CheckInEvery)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			s.checkIn(ctx)
		case m := <-times:
			if t, err := time.Parse(time.RFC3339, m.ModelTime); err == nil && !s.retired && s.due(t) {
				if o, ok := s.read(ctx, t); ok {
					s.mq.Publish(msg.ObsTopic(s.room, s.kind), o)
				}
			}
		}
	}
}

// due reports whether it is time for a reading at model time t: a sample
// period after the last one, or any time at all after the clock was moved back.
func (s *sensor) due(t time.Time) bool {
	return s.last.IsZero() || t.Before(s.last) || t.Sub(s.last) >= s.sample
}

// read takes one reading at model time t: the value at the device's place in
// BuildSim, with the device's noise and fault on top. The clock shows a
// minute only after the physical model has written that minute's values, so
// what is read here belongs to t. ok is false if there is nothing to report:
// no value yet, or a fault that silences the device.
func (s *sensor) read(ctx context.Context, t time.Time) (msg.Observation, bool) {
	raw, err := s.bs.SensorValue(ctx, registry.SensorID(s.id))
	if err != nil {
		// No retry inside a sample: a sensor that can't sense goes quiet,
		// which is what a dead device looks like.
		log.Printf("read: %v", err)
		return msg.Observation{}, false
	}
	truth, err := strconv.ParseFloat(raw.Value, 64)
	if err != nil {
		return msg.Observation{}, false // the physical model has not written here yet
	}
	s.last = t
	v, publish := s.inject.Apply(s.room, t, faults.Noise(s.kind, truth, s.rng))
	if !publish {
		return msg.Observation{}, false
	}
	// What the device reports is rounded to its resolution.
	v, _ = strconv.ParseFloat(format(s.kind, v), 64)
	return msg.Observation{
		RunID:     s.runID,
		SensorID:  s.id,
		Room:      s.room.String(),
		Kind:      s.kind,
		Value:     v,
		Unit:      rooms.Sensors[s.kind].Unit,
		ModelTime: msg.FormatTime(t),
		Seq:       t.Unix(),
	}, true
}

// runClock is the clock device: it watches its value in BuildSim and
// publishes the time whenever it changes, and once a second when it doesn't.
func (s *sensor) runClock(ctx context.Context) {
	poll := time.NewTicker(ClockPoll)
	defer poll.Stop()
	heartbeat := time.NewTicker(CheckInEvery)
	defer heartbeat.Stop()
	var shown string
	var sent time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			s.checkIn(ctx)
		case <-poll.C:
			if s.retired {
				continue
			}
			m, raw, ok := s.readClock(ctx)
			if !ok || (raw == shown && time.Since(sent) < ClockBeat) {
				continue
			}
			s.mq.Publish(msg.TimeTopic, m)
			shown, sent = raw, time.Now()
		}
	}
}

// readClock returns the time at the clock's place in BuildSim. A dropout
// fault silences the clock like any other sensor.
func (s *sensor) readClock(ctx context.Context) (m msg.ModelTime, raw string, ok bool) {
	v, err := s.bs.SensorValue(ctx, registry.SensorID(s.id))
	if err != nil || json.Unmarshal([]byte(v.Value), &m) != nil {
		return m, "", false // not written yet, or BuildSim is away
	}
	t, err := time.Parse(time.RFC3339, m.ModelTime)
	if err != nil {
		return m, "", false
	}
	if _, publish := s.inject.Apply(s.room, t, 0); !publish {
		return m, "", false
	}
	return m, v.Value, true
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

func format(kind string, v float64) string {
	switch kind {
	case rooms.Temp:
		return strconv.FormatFloat(v, 'f', 1, 64)
	default:
		return strconv.FormatFloat(max(v, 0), 'f', 0, 64)
	}
}
