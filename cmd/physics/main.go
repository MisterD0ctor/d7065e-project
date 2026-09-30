// Command physics simulates CO₂ and temperature per room (FR-1), serves the
// truth to the sensor gateways (IF-2) and publishes it for storage once per
// model minute (IF-5). It never writes to BuildSim: it reads occupancy and the
// actuators' reached states from there, and the model clock from occupancysim
// (D-1).
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/mqttx"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/roommodel"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/site"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

type roomTruth struct {
	CO2       float64 `json:"co2_ppm"`
	Temp      float64 `json:"temp_c"`
	Occupancy int     `json:"occupancy"`
	Airflow   float64 `json:"airflow_ls"`
	Setpoint  float64 `json:"setpoint_c"`
	FanW      float64 `json:"fan_w"`
	AHUHeatW  float64 `json:"ahu_heat_w"`
	RadiatorW float64 `json:"radiator_w"`
}

type snapshot struct {
	RunID     string               `json:"run_id"`
	ModelTime string               `json:"model_time"`
	Rooms     map[string]roomTruth `json:"rooms"`
}

type sim struct {
	bs     *buildsim.Client
	clk    *clock.Client
	runID  string
	keys   []rooms.Key
	models map[rooms.Key]*roommodel.Room
	inputs map[rooms.Key]roommodel.Inputs
	last   time.Time // model time of the last step
	mq     *mqttx.Client

	mu   sync.RWMutex
	snap snapshot
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	keys, err := rooms.ParseList(env.Must("ROOMS"))
	if err != nil {
		log.Fatal(err)
	}
	s := &sim{
		bs:     buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090")),
		clk:    clock.New(env.String("CLOCK_URL", "http://occupancysim:8081")),
		runID:  env.String("RUN_ID", "dev"),
		keys:   keys,
		models: map[rooms.Key]*roommodel.Room{},
		inputs: map[rooms.Key]roommodel.Inputs{},
		mq:     mqttx.Connect("physics"),
	}
	s.loadRooms(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /rooms", s.handleRooms)
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

// loadRooms builds one room model per configured room.
func (s *sim) loadRooms(ctx context.Context) {
	sizes, err := site.Sizes(ctx, s.bs, s.clk, s.keys)
	if err != nil {
		log.Fatal(err)
	}
	for _, k := range s.keys {
		size := sizes[k]
		s.models[k] = roommodel.New(size)
		// Until an actuator reports, assume design flow and the default setpoint.
		s.inputs[k] = roommodel.Inputs{AirflowLs: size.Design, Setpoint: sizing.DefaultSetpoint}
		log.Printf("%s: %.1f m², capacity %d, design %.0f l/s", k, size.AreaM2, size.Capacity, size.Design)
	}
}

func (s *sim) tick(ctx context.Context) {
	now, err := s.clk.Now(ctx)
	if err != nil {
		log.Printf("clock: %v (holding state)", err)
		return
	}
	if s.last.IsZero() || now.Time.Before(s.last) {
		// First reading, or the clock was moved back: restart from here
		// instead of integrating over a negative interval.
		s.last = now.Time
		s.publish(now.Time)
		return
	}
	s.mq.PublishRetained(msg.TimeTopic, msg.ModelTime{ModelTime: msg.FormatTime(now.Time), Factor: now.Factor})
	d := now.Time.Sub(s.last)
	if d == 0 {
		return
	}
	s.readInputs(ctx)
	for _, k := range s.keys {
		s.models[k].Step(d, s.inputs[k])
	}
	s.last = now.Time
	s.publish(now.Time)
}

// readInputs refreshes occupancy and actuator states. On any error the last
// known inputs are kept, so a BuildSim hiccup doesn't reset the rooms.
func (s *sim) readInputs(ctx context.Context) {
	occ, err := s.bs.Occupancy(ctx)
	if err != nil {
		log.Printf("occupancy: %v (keeping last)", err)
	} else {
		for _, k := range s.keys {
			in := s.inputs[k]
			in.Occupancy = len(occ[k.String()].Persons)
			s.inputs[k] = in
		}
	}

	levels := map[string]bool{}
	for _, k := range s.keys {
		levels[k.Level] = true
	}
	// The world feels whatever state the room's damper and radiator valve
	// have reached, found by equipment type and room: physics doesn't need
	// the registry to know which device is which.
	airflow := rooms.Actuators[rooms.Damper].EquipmentType
	radiator := rooms.Actuators[rooms.Heating].EquipmentType
	for level := range levels {
		eq, err := s.bs.Equipment(ctx, level)
		if err != nil {
			log.Printf("equipment %s: %v (keeping last)", level, err)
			continue
		}
		for _, e := range eq {
			k := rooms.Key{Level: e.Level, Name: e.Room}
			in, ok := s.inputs[k]
			if !ok || len(e.Actuators) == 0 {
				continue
			}
			v, ok := parse(e.Actuators[0].State)
			if !ok {
				continue
			}
			switch e.Type {
			case airflow:
				in.AirflowLs = v
			case radiator:
				in.Setpoint = v
			}
			s.inputs[k] = in
		}
	}
}

func parse(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func (s *sim) publish(t time.Time) {
	snap := snapshot{RunID: s.runID, ModelTime: t.Format(time.RFC3339), Rooms: map[string]roomTruth{}}
	for _, k := range s.keys {
		m, in := s.models[k], s.inputs[k]
		snap.Rooms[k.String()] = roomTruth{
			CO2: m.CO2, Temp: m.Temp, Occupancy: in.Occupancy,
			Airflow: in.AirflowLs, Setpoint: in.Setpoint,
			FanW: m.FanW, AHUHeatW: m.AHUHeatW, RadiatorW: m.RadiatorW,
		}
	}
	s.mu.Lock()
	prev := s.snap.ModelTime
	s.snap = snap
	s.mu.Unlock()

	// Once per model minute the truth goes to storage (IF-5).
	if prev != "" && t.Truncate(time.Minute).Format(time.RFC3339) == mustTrunc(prev) {
		return
	}
	for _, k := range s.keys {
		r := snap.Rooms[k.String()]
		s.mq.Publish(msg.TruthTopic(k), msg.Truth{
			RunID: s.runID, Room: k.String(), ModelTime: snap.ModelTime,
			CO2: r.CO2, Temp: r.Temp, Occupancy: r.Occupancy,
			Airflow: r.Airflow, Setpoint: r.Setpoint,
			FanW: r.FanW, AHUHeatW: r.AHUHeatW, RadiatorW: r.RadiatorW,
		})
	}
}

func mustTrunc(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return t.Truncate(time.Minute).Format(time.RFC3339)
}

// handleRooms serves IF-2. ?room= returns one room, ?level= one floor.
func (s *sim) handleRooms(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	snap := s.snap
	s.mu.RUnlock()
	if snap.ModelTime == "" {
		http.Error(w, "no model time yet", http.StatusServiceUnavailable)
		return
	}
	if room := r.URL.Query().Get("room"); room != "" {
		v, ok := snap.Rooms[room]
		if !ok {
			http.Error(w, "room not simulated", http.StatusNotFound)
			return
		}
		snap.Rooms = map[string]roomTruth{room: v}
	} else if level := r.URL.Query().Get("level"); level != "" {
		filtered := map[string]roomTruth{}
		for key, v := range snap.Rooms {
			if k, err := rooms.Parse(key); err == nil && k.Level == level {
				filtered[key] = v
			}
		}
		snap.Rooms = filtered
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}
