// Command actuator is one actuator device (D-9): a ventilation damper or a
// radiator valve in one room. Like a sensor, it knows only its id and kind;
// at boot it checks in with the registry (IF-12) to learn its room and the
// room's ventilation sizing, and tells the registry where to send commands.
// It accepts commands over HTTP (IF-8), enforces the safety limits itself
// (D-6), and writes the state it has reached to BuildSim (IF-9), where
// physics reads it back.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/actuator"
	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/devreg"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/mqttx"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/registry"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

const CheckInEvery = 30 * time.Second

type device struct {
	id       string
	room     rooms.Key
	endpoint string
	logic    *actuator.Device
	reg      *devreg.Client
	bs       *buildsim.Client
	clk      *clock.Client // fallback when the model time stops arriving on MQTT
	mq       *mqttx.Client
	runID    string
	written  string // last state written to BuildSim

	timeSilence mqttx.Silence
	co2Silence  mqttx.Silence

	mu      sync.Mutex
	now     time.Time // latest model time
	co2     float64
	haveCO2 bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	id := env.Must("DEVICE_ID")
	kind := env.Must("DEVICE_KIND")
	if !registry.IsActuator(kind) {
		log.Fatalf("DEVICE_KIND=%q: want damper or heating", kind)
	}
	port := env.String("PORT", "8080")
	host, _ := os.Hostname()
	endpoint := env.String("ADVERTISE_URL", fmt.Sprintf("http://%s:%s", host, port))

	reg := devreg.New(env.String("REGISTRY_URL", "http://registry:8080"))
	dev, err := reg.WaitForCommissioning(ctx, id, endpoint)
	if err != nil {
		return
	}
	if dev.Kind != kind {
		log.Fatalf("device %s is a %s but is registered as %s; fix the registry entry", id, kind, dev.Kind)
	}
	room, _ := rooms.Parse(dev.Room)
	log.Printf("device %s: %s in %s, design %.0f l/s, reachable at %s", id, kind, room, dev.Size.Design, endpoint)

	d := &device{
		id:       id,
		room:     room,
		endpoint: endpoint,
		logic:    actuator.New(kind, map[rooms.Key]sizing.Room{room: dev.Size}),
		reg:      reg,
		bs:       buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090")),
		clk:      clock.New(env.String("CLOCK_URL", "http://occupancysim:8081")),
		mq:       mqttx.Connect(id),
		runID:    env.String("RUN_ID", "dev"),
	}
	d.mq.Subscribe(msg.TimeTopic, d.onTime)
	if kind == rooms.Damper {
		d.mq.Subscribe(msg.ObsTopic(room, rooms.CO2), d.onCO2)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /rooms/{level}/{room}/command", d.handleCommand)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	heartbeat := time.NewTicker(CheckInEvery)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			srv.Shutdown(context.Background())
			return
		case <-heartbeat.C:
			if _, err := d.reg.CheckIn(ctx, d.id, d.endpoint); err != nil {
				log.Printf("check-in: %v", err)
			}
		case <-tick.C:
			d.tick(ctx)
		}
	}
}

func (d *device) onTime(_ string, payload []byte) {
	var m msg.ModelTime
	if json.Unmarshal(payload, &m) != nil {
		return
	}
	t, err := time.Parse(time.RFC3339, m.ModelTime)
	if err != nil {
		return
	}
	d.timeSilence.Arrived(time.Now())
	d.mu.Lock()
	d.now = t
	d.mu.Unlock()
}

func (d *device) onCO2(_ string, payload []byte) {
	var o msg.Observation
	if json.Unmarshal(payload, &o) != nil {
		return
	}
	d.co2Silence.Arrived(time.Now())
	d.mu.Lock()
	d.co2, d.haveCO2 = o.Value, true
	d.mu.Unlock()
}

func (d *device) handleCommand(w http.ResponseWriter, r *http.Request) {
	k := rooms.Key{Level: r.PathValue("level"), Name: r.PathValue("room")}
	var c actuator.Command
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, `{"reason":"body is not a command"}`, http.StatusUnprocessableEntity)
		return
	}
	res := d.logic.Handle(k, c)
	if res.Status != http.StatusAccepted && !res.Duplicate {
		d.event(actuator.Event{Room: k, Kind: "rejected", Detail: fmt.Sprintf("%s (cmd %s)", res.Reason, c.CmdID)})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.Status)
	json.NewEncoder(w).Encode(res)
}

func (d *device) tick(ctx context.Context) {
	now, ok := d.modelTime(ctx)
	if !ok {
		// Without the time no TTL can expire; keep the current target.
		return
	}
	var co2 map[rooms.Key]float64
	if v, ok := d.latestCO2(ctx); ok {
		co2 = map[rooms.Key]float64{d.room: v}
	}
	reached, events := d.logic.Tick(now, co2)
	for _, e := range events {
		d.event(e)
	}
	state := strconv.FormatFloat(reached[d.room], 'f', 1, 64)
	if state == d.written {
		return
	}
	err := d.bs.SetActuatorState(ctx, registry.ActuatorID(d.id), state)
	switch {
	case buildsim.IsNotFound(err):
		log.Printf("not in BuildSim yet; the registry will recreate it")
	case err != nil:
		log.Printf("write state: %v", err)
	default:
		d.written = state
	}
}

// modelTime comes from physics over MQTT; if that goes quiet, the device asks
// occupancysim directly.
func (d *device) modelTime(ctx context.Context) (time.Time, bool) {
	if !d.timeSilence.Silent(time.Now()) {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.now, !d.now.IsZero()
	}
	r, err := d.clk.Now(ctx)
	if err != nil {
		log.Printf("clock: %v", err)
		return time.Time{}, false
	}
	return r.Time, true
}

// latestCO2 is the damper's input for the override: from MQTT normally, from
// BuildSim when MQTT is silent. It doesn't involve the controller (D-6).
func (d *device) latestCO2(ctx context.Context) (float64, bool) {
	if d.logic.Kind != rooms.Damper {
		return 0, false
	}
	if !d.co2Silence.Silent(time.Now()) {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.co2, d.haveCO2
	}
	sensors, err := d.reg.List(ctx, registry.Filter{Kind: rooms.CO2, Room: d.room.String(), Status: "active"})
	if err != nil || len(sensors) == 0 {
		return 0, false
	}
	s, err := d.bs.SensorValue(ctx, registry.SensorID(sensors[0].ID))
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(s.Value, 64)
	return v, err == nil
}

func (d *device) event(e actuator.Event) {
	log.Printf("event %s %s: %s", e.Room, e.Kind, e.Detail)
	d.mu.Lock()
	now := d.now
	d.mu.Unlock()
	d.mq.Publish(msg.EventTopic(e.Room), msg.Event{
		RunID: d.runID, Room: e.Room.String(), ModelTime: msg.FormatTime(now),
		Actuator: d.logic.Kind, Kind: e.Kind, Detail: e.Detail,
	})
}
