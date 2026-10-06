// Command physics simulates CO₂ and temperature per room (FR-1), serves the
// truth to the sensors (IF-2) and publishes it for storage at every whole
// model minute (IF-5). It never writes to BuildSim.
//
// Its inputs are the two other things that are true about the building. Who
// is in each room comes from occupancysim's per-minute record (IF-1), together
// with the model clock; the two simulators form the truth layer. What the
// actuators have reached comes from BuildSim, where the actuators report it.
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
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/site"
)

// Tick is how often physics asks occupancysim for new minutes, in real time.
// It has to be shorter than a model minute lasts (0.5 s at factor 120), or
// the sensors, which sample the latest minute, would skip some.
const Tick = 250 * time.Millisecond

type roomTruth struct {
	CO2       float64 `json:"co2_ppm"`
	Temp      float64 `json:"temp_c"`
	Occupancy int     `json:"occupancy"`
}

// snapshot is what the sensors read (IF-2): every room at the latest minute.
type snapshot struct {
	RunID     string               `json:"run_id"`
	ModelTime string               `json:"model_time"`
	Rooms     map[string]roomTruth `json:"rooms"`
}

type sim struct {
	bs    *buildsim.Client
	clk   *clock.Client
	mq    *mqttx.Client
	runID string
	w     *world

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
		bs:    buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090")),
		clk:   clock.New(env.String("CLOCK_URL", "http://occupancysim:8081")),
		mq:    mqttx.Connect("physics"),
		runID: env.String("RUN_ID", "dev"),
	}
	sizes, err := site.Sizes(ctx, s.bs, s.clk, keys)
	if err != nil {
		log.Fatal(err)
	}
	s.w = newWorld(keys, sizes)
	for _, k := range keys {
		log.Printf("%s: %.1f m², capacity %d, design %.0f l/s", k, sizes[k].AreaM2, sizes[k].Capacity, sizes[k].Design)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /rooms", s.handleRooms)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	srv := &http.Server{Addr: ":" + env.String("PORT", "8080"), Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	tick := time.NewTicker(Tick)
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

// tick fetches the minutes occupancysim has recorded since the last one and
// advances the rooms through each of them.
func (s *sim) tick(ctx context.Context) {
	now, minutes, err := s.clk.Minutes(ctx, s.w.last)
	if err != nil {
		log.Printf("occupancysim: %v (holding state)", err)
		return
	}
	// Relay the model time to the devices, as a building network would.
	s.mq.PublishRetained(msg.TimeTopic, msg.ModelTime{ModelTime: msg.FormatTime(now.Time), Factor: now.Factor})

	if !s.w.last.IsZero() && now.Time.Before(s.w.last) {
		// The clock was moved back, e.g. for a new experiment. Forget where
		// we were; the next tick starts from occupancysim's newest minute.
		log.Printf("model clock moved back from %s to %s; restarting there", msg.FormatTime(s.w.last), msg.FormatTime(now.Time))
		s.w.last = time.Time{}
		return
	}
	if len(minutes) == 0 {
		return
	}
	// The actuators' states are read once per tick and held for the minutes
	// processed in it: at most a few, unless physics is catching up.
	s.readActuators(ctx)
	for _, m := range minutes {
		if gap := m.Time.Sub(s.w.last); !s.w.last.IsZero() && gap > time.Minute {
			log.Printf("fell behind occupancysim's record: stepping %v in one go", gap)
		}
		s.w.advance(m)
		for _, k := range s.w.keys {
			s.mq.Publish(msg.TruthTopic(k), s.w.truth(s.runID, k))
		}
	}
	s.updateSnapshot()
}

// readActuators refreshes what each room's damper and radiator valve have
// reached. The world feels the reached state, found by equipment type and
// room: physics doesn't need the registry to know which device is which. On
// an error the last known states are kept.
func (s *sim) readActuators(ctx context.Context) {
	levels := map[string]bool{}
	for _, k := range s.w.keys {
		levels[k.Level] = true
	}
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
			a, ok := s.w.act[k]
			if !ok || len(e.Actuators) == 0 {
				continue
			}
			v, err := strconv.ParseFloat(e.Actuators[0].State, 64)
			if err != nil {
				continue // not reported yet
			}
			switch e.Type {
			case airflow:
				a.AirflowLs = v
			case radiator:
				a.Setpoint = v
			}
			s.w.act[k] = a
		}
	}
}

func (s *sim) updateSnapshot() {
	snap := snapshot{RunID: s.runID, ModelTime: msg.FormatTime(s.w.last), Rooms: map[string]roomTruth{}}
	for _, k := range s.w.keys {
		m := s.w.models[k]
		snap.Rooms[k.String()] = roomTruth{CO2: m.CO2, Temp: m.Temp, Occupancy: s.w.occ[k].Occupants}
	}
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
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
