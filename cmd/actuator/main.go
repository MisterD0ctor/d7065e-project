// Command actuator runs one kind of actuator (KIND=damper or heating) for every
// configured room (D-9). It accepts commands over HTTP (IF-8), enforces the
// safety limits itself (D-6), and writes the state it has reached to BuildSim
// (IF-9), where physics reads it back.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/actuator"
	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/site"
)

type service struct {
	dev     *actuator.Device
	keys    []rooms.Key
	bs      *buildsim.Client
	clk     *clock.Client
	written map[rooms.Key]string // last state written to BuildSim
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
	}
	s.register(ctx)

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
		log.Printf("event %s %s rejected: %s (cmd %s)", s.dev.Kind, k, res.Reason, c.CmdID)
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
	reached, events := s.dev.Tick(now.Time, s.co2(ctx))
	for _, e := range events {
		log.Printf("event %s %s %s: %s", s.dev.Kind, e.Room, e.Kind, e.Detail)
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

// co2 reads the latest CO₂ readings from BuildSim for the override. Only the
// damper needs them. Once observations go over MQTT this becomes a
// subscription to obs/+/+/co2 (IF-4).
func (s *service) co2(ctx context.Context) map[rooms.Key]float64 {
	if s.dev.Kind != rooms.Damper {
		return nil
	}
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
