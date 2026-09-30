// Command registry is the device registry (D-10, IF-12). An installer records
// each device's id, kind and room, through the web form at / or the API. A
// device that boots looks itself up here to learn its room, and checks in so
// the controller can find it. The registry keeps BuildSim's equipment list in
// step with what is installed, recreating it after a BuildSim restart.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/registry"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

//go:embed install.html
var installPage []byte

type service struct {
	db  *registry.DB
	bs  *buildsim.Client
	occ *clock.Client

	mu     sync.Mutex
	floors map[string]buildsim.Floor // cached floor plans, by level
	caps   map[string]int            // occupancysim capacities, by <level>/<room>
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := registry.Open(env.String("DB_PATH", "/data/registry.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	s := &service{
		db:     db,
		bs:     buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090")),
		occ:    clock.New(env.String("CLOCK_URL", "http://occupancysim:8081")),
		floors: map[string]buildsim.Floor{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(installPage)
	})
	mux.HandleFunc("POST /devices", s.handleInstall)
	mux.HandleFunc("GET /devices", s.handleList)
	mux.HandleFunc("GET /devices/{id}", s.handleGet)
	mux.HandleFunc("DELETE /devices/{id}", s.handleRetire)
	mux.HandleFunc("PUT /devices/{id}/checkin", s.handleCheckIn)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	srv := &http.Server{Addr: ":" + env.String("PORT", "8080"), Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	resync := time.NewTicker(10 * time.Second)
	defer resync.Stop()
	s.syncBuildSim(ctx)
	for {
		select {
		case <-ctx.Done():
			srv.Shutdown(context.Background())
			return
		case <-resync.C:
			s.syncBuildSim(ctx)
		}
	}
}

type installRequest struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Room        string `json:"room"`
	InstalledBy string `json:"installed_by"`
}

func (s *service) handleInstall(w http.ResponseWriter, r *http.Request) {
	var req installRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "body is not JSON: %v", err)
		return
	}
	k, err := rooms.Parse(req.Room)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, "%v", err)
		return
	}
	info, err := s.roomInfo(r.Context(), k)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, "%v", err)
		return
	}
	dev, err := s.db.Install(req.ID, req.Kind, k, req.InstalledBy, info, time.Now())
	switch {
	case errors.Is(err, registry.ErrExists):
		httpError(w, http.StatusConflict, "%v", err)
		return
	case errors.Is(err, registry.ErrInvalid):
		httpError(w, http.StatusUnprocessableEntity, "%v", err)
		return
	case err != nil:
		httpError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	log.Printf("installed %s (%s) in %s by %s", dev.ID, dev.Kind, dev.Room, dev.InstalledBy)
	s.syncBuildSim(r.Context())
	writeJSON(w, http.StatusCreated, dev)
}

// roomInfo checks that the room exists in the floor plan and finds its area
// and design capacity. A typo in the room is caught here, at installation,
// instead of as a device that silently reports for a room nobody controls.
func (s *service) roomInfo(ctx context.Context, k rooms.Key) (registry.RoomInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	floor, ok := s.floors[k.Level]
	if !ok {
		var err error
		if floor, err = s.bs.Floor(ctx, k.Level); err != nil {
			return registry.RoomInfo{}, fmt.Errorf("level %q: %v", k.Level, err)
		}
		s.floors[k.Level] = floor
	}
	var area float64
	for _, r := range floor.Rooms {
		if r.Name == k.Name && r.Type != "corridor" {
			area = sizing.AreaM2(r.Area)
		}
	}
	if area == 0 {
		return registry.RoomInfo{}, fmt.Errorf("room %s is not in the floor plan", k)
	}
	if s.caps == nil {
		caps, err := s.occ.Capacities(ctx)
		if err != nil {
			log.Printf("capacities from occupancysim: %v (estimating from area)", err)
		} else {
			s.caps = caps
		}
	}
	capacity, ok := s.caps[k.String()]
	if !ok || capacity == 0 {
		capacity = sizing.Capacity(area)
	}
	return registry.RoomInfo{AreaM2: area, Capacity: capacity}, nil
}

func (s *service) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.db.List(registry.Filter{Kind: q.Get("kind"), Room: q.Get("room"), Status: q.Get("status")})
	if errors.Is(err, registry.ErrInvalid) {
		httpError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if list == nil {
		list = []registry.Device{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *service) handleGet(w http.ResponseWriter, r *http.Request) {
	dev, err := s.db.Get(r.PathValue("id"))
	if errors.Is(err, registry.ErrNotFound) {
		httpError(w, http.StatusNotFound, "%v", err)
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, dev)
}

func (s *service) handleRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.Retire(id); errors.Is(err, registry.ErrNotFound) {
		httpError(w, http.StatusNotFound, "%v", err)
		return
	} else if err != nil {
		httpError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if err := s.bs.DeleteEquipment(r.Context(), id); err != nil && !buildsim.IsNotFound(err) {
		log.Printf("retire %s: BuildSim: %v", id, err)
	}
	log.Printf("retired %s", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *service) handleCheckIn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	json.NewDecoder(r.Body).Decode(&body) // sensors check in without an endpoint
	dev, err := s.db.CheckIn(r.PathValue("id"), body.Endpoint, time.Now())
	switch {
	case errors.Is(err, registry.ErrNotFound):
		httpError(w, http.StatusNotFound, "device not installed: %v", err)
	case errors.Is(err, registry.ErrRetired):
		httpError(w, http.StatusGone, "%v", err)
	case err != nil:
		httpError(w, http.StatusInternalServerError, "%v", err)
	default:
		writeJSON(w, http.StatusOK, dev)
	}
}

// syncBuildSim registers every active device's equipment. BuildSim skips ids
// it already has, so this is cheap when nothing changed, and it restores the
// whole list after BuildSim restarts blank.
func (s *service) syncBuildSim(ctx context.Context) {
	devs, err := s.db.List(registry.Filter{Status: "active"})
	if err != nil {
		log.Printf("sync: %v", err)
		return
	}
	if len(devs) == 0 {
		return
	}
	eq := make([]buildsim.Equipment, 0, len(devs))
	for _, d := range devs {
		eq = append(eq, equipment(d))
	}
	if err := s.bs.BulkCreate(ctx, eq); err != nil {
		log.Printf("sync to BuildSim: %v", err)
	}
}

func equipment(d registry.Device) buildsim.Equipment {
	k, _ := rooms.Parse(d.Room)
	e := buildsim.Equipment{
		ID:        d.ID,
		Name:      fmt.Sprintf("%s %s", d.Kind, d.ID),
		Level:     k.Level,
		Room:      k.Name,
		Status:    "running",
		Sensors:   []buildsim.Sensor{},
		Actuators: []buildsim.Actuator{},
	}
	if spec, ok := rooms.Sensors[d.Kind]; ok {
		e.Type, e.Category = spec.EquipmentType, "sensor"
		e.Sensors = []buildsim.Sensor{{ID: registry.SensorID(d.ID), Type: spec.SensorType, DataType: "text", Unit: spec.Unit}}
	}
	if spec, ok := rooms.Actuators[d.Kind]; ok {
		e.Type, e.Category = spec.EquipmentType, "hvac"
		e.Actuators = []buildsim.Actuator{{ID: registry.ActuatorID(d.ID), Type: spec.ActuatorType}}
	}
	return e
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]string{"error": fmt.Sprintf(format, args...)})
}
