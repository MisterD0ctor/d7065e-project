// Command actuator runs one kind of actuator (KIND=damper or heating) for every
// configured room (D-9). It accepts commands over HTTP (IF-8), enforces the
// safety limits itself (D-6), and writes the state it has reached to BuildSim
// (IF-9), where physics reads it back. The damper follows the CO₂
// observations on MQTT for its override, falling back to BuildSim when the
// broker is silent; events go out on MQTT (IF-7).
package main

import (
	"context"
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
	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/mqttx"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/site"
)

type service struct {
	dev     *actuator.Device
	keys    []rooms.Key
	bs      *buildsim.Client
	clk     *clock.Client
	written map[rooms.Key]string // last state written to BuildSim
	mq      *mqttx.Client
	runID   string
	now     time.Time // latest model time, for event records

	silence mqttx.Silence // when to read CO₂ from BuildSim instead

	mu     sync.Mutex
	latest map[rooms.Key]float64 // latest CO₂ observation per room
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	kind := env.Must("KIND")
	if _, ok := rooms.Actuators[kind]; !ok {
		log.Fatalf("KIND=%q: want damper or heating", kind)
	}
	keys, err := rooms.ParseList(env.Must("ROOMS"))
	if err != nil {
		log.Fatal(err)
	}
	bs := buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090"))
	clk := clock.New(env.String("CLOCK_URL", "http://occupancysim:8081"))
	sizes, err := site.Sizes(ctx, bs, clk, keys)
	if err != nil {
		log.Fatal(err)
	}
	s := &service{
		dev:     actuator.New(kind, sizes),
		keys:    keys,
		bs:      bs,
		clk:     clk,
		written: map[rooms.Key]string{},
		mq:      mqttx.Connect("actuator-" + kind),
		runID:   env.String("RUN_ID", "dev"),
		latest:  map[rooms.Key]float64{},
	}
	s.register(ctx)
	if kind == rooms.Damper {
		s.mq.Subscribe("obs/+/+/"+rooms.CO2, s.onCO2)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /rooms/{level}/{room}/command", s.handleCommand)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	srv := &http.Server{Addr: ":" + env.String("PORT", "8080"), Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			srv.Shutdown(context.Background())
			return
		case <-tick.C:
			s.tick(ctx)
		}
	}
}

func (s *service) register(ctx context.Context) {
	spec := rooms.Actuators[s.dev.Kind]
	var eq []buildsim.Equipment
	for _, k := range s.keys {
		eq = append(eq, buildsim.Equipment{
			ID:       rooms.EquipmentID(spec.EquipmentPrefix, k),
			Name:     fmt.Sprintf("%s %s", s.dev.Kind, k),
			Type:     spec.EquipmentType,
			Category: "hvac",
			Level:    k.Level,
			Room:     k.Name,
			Status:   "running",
			Sensors:  []buildsim.Sensor{},
			Actuators: []buildsim.Actuator{{
				ID: rooms.ActuatorID(s.dev.Kind, k), Type: spec.ActuatorType,
				State: "",
			}},
		})
	}
	for {
		err := s.bs.BulkCreate(ctx, eq)
		if err == nil {
			log.Printf("registered %d %s actuators", len(eq), s.dev.Kind)
			s.written = map[rooms.Key]string{} // rewrite every state
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

func (s *service) handleCommand(w http.ResponseWriter, r *http.Request) {
	k := rooms.Key{Level: r.PathValue("level"), Name: r.PathValue("room")}
	var c actuator.Command
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, `{"reason":"body is not a command"}`, http.StatusUnprocessableEntity)
		return
	}
	res := s.dev.Handle(k, c)
	if res.Status != http.StatusAccepted && !res.Duplicate {
		s.event(actuator.Event{Room: k, Kind: "rejected", Detail: fmt.Sprintf("%s (cmd %s)", res.Reason, c.CmdID)})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.Status)
	json.NewEncoder(w).Encode(res)
}

func (s *service) tick(ctx context.Context) {
	now, err := s.clk.Now(ctx)
	if err != nil {
		// Without the clock no TTL can expire; keep the current targets.
		log.Printf("clock: %v", err)
		return
	}
	s.now = now.Time
	reached, events := s.dev.Tick(now.Time, s.co2(ctx))
	for _, e := range events {
		s.event(e)
	}
	for _, k := range s.keys {
		state := strconv.FormatFloat(reached[k], 'f', 1, 64)
		if s.written[k] == state {
			continue
		}
		err := s.bs.SetActuatorState(ctx, rooms.ActuatorID(s.dev.Kind, k), state)
		if buildsim.IsNotFound(err) {
			log.Printf("%s: equipment missing, re-registering", k)
			s.register(ctx)
			continue
		}
		if err != nil {
			log.Printf("%s: write state: %v", k, err)
			continue
		}
		s.written[k] = state
	}
}

func (s *service) event(e actuator.Event) {
	log.Printf("event %s %s %s: %s", s.dev.Kind, e.Room, e.Kind, e.Detail)
	s.mq.Publish(msg.EventTopic(e.Room), msg.Event{
		RunID: s.runID, Room: e.Room.String(), ModelTime: msg.FormatTime(s.now),
		Actuator: s.dev.Kind, Kind: e.Kind, Detail: e.Detail,
	})
}

func (s *service) onCO2(_ string, payload []byte) {
	var o msg.Observation
	if err := json.Unmarshal(payload, &o); err != nil {
		return
	}
	k, err := rooms.Parse(o.Room)
	if err != nil {
		return
	}
	s.silence.Arrived(time.Now())
	s.mu.Lock()
	s.latest[k] = o.Value
	s.mu.Unlock()
}

// co2 returns the latest CO₂ reading per room for the override. Only the
// damper needs them: from MQTT normally, from BuildSim when MQTT is silent.
// The override doesn't depend on the controller either way (D-6).
func (s *service) co2(ctx context.Context) map[rooms.Key]float64 {
	if s.dev.Kind != rooms.Damper {
		return nil
	}
	if s.silence.Silent(time.Now()) {
		return s.co2FromBuildSim(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[rooms.Key]float64, len(s.latest))
	for k, v := range s.latest {
		out[k] = v
	}
	return out
}

func (s *service) co2FromBuildSim(ctx context.Context) map[rooms.Key]float64 {
	want := map[string]rooms.Key{}
	levels := map[string]bool{}
	for _, k := range s.keys {
		want[rooms.SensorID(rooms.CO2, k)] = k
		levels[k.Level] = true
	}
	out := map[rooms.Key]float64{}
	for level := range levels {
		eq, err := s.bs.Equipment(ctx, level)
		if err != nil {
			log.Printf("co2 for override: %v", err)
			continue
		}
		for _, e := range eq {
			for _, sn := range e.Sensors {
				k, ok := want[sn.ID]
				if !ok {
					continue
				}
				if v, err := strconv.ParseFloat(sn.Value, 64); err == nil {
					out[k] = v
				}
			}
		}
	}
	return out
}
