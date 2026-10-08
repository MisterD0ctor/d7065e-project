// Command viz is the dashboard (D-8, FR-10). It follows the observations,
// decisions and actuator events on MQTT and shows the building in the BuildSim
// viewer: a heatmap layer per measure and the current alerts (IF-11).
//
// It is the only writer of room layers and alerts: BuildSim keeps one
// collection of each, and a second writer would erase the first. The viewer
// itself is provided infrastructure; what is ours is what gets mapped into it.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/dashboard"
	"github.com/MisterD0ctor/d7065e-project/internal/devreg"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/mqttx"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/registry"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

const (
	// PublishEvery is how often the layers go to the viewer, in real time.
	PublishEvery = time.Second
	// RefreshAlerts re-sends unchanged alerts, so a BuildSim that restarted
	// blank gets them back. (The layers are re-sent every PublishEvery.)
	RefreshAlerts = 30 * time.Second
	// RefreshRooms is how often the room list is re-read from the registry.
	RefreshRooms = 30 * time.Second
)

type viz struct {
	board *dashboard.Board
	bs    *buildsim.Client
	reg   *devreg.Client

	mu      sync.Mutex
	maxFlow map[rooms.Key]float64 // each room's maximum airflow, l/s

	lastAlerts   string    // JSON of the alerts last sent
	alertsSentAt time.Time // real time
	failing      bool      // the last write to BuildSim failed
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	v := &viz{
		board:   dashboard.New(),
		bs:      buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090")),
		reg:     devreg.New(env.String("REGISTRY_URL", "http://registry:8080")),
		maxFlow: map[rooms.Key]float64{},
	}
	v.refreshRooms(ctx)

	mq := mqttx.Connect("viz")
	mq.Subscribe("obs/#", func(_ string, p []byte) {
		var o msg.Observation
		if json.Unmarshal(p, &o) == nil {
			v.board.Observe(o)
		}
	})
	mq.Subscribe("decision/#", func(_ string, p []byte) {
		var d msg.Decision
		if json.Unmarshal(p, &d) == nil {
			v.board.Decide(d)
		}
	})
	mq.Subscribe("event/#", func(_ string, p []byte) {
		var e msg.Event
		if json.Unmarshal(p, &e) == nil {
			v.board.Event(e)
		}
	})

	publish := time.NewTicker(PublishEvery)
	defer publish.Stop()
	refresh := time.NewTicker(RefreshRooms)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-refresh.C:
			v.refreshRooms(ctx)
		case <-publish.C:
			v.publish(ctx)
		}
	}
}

// refreshRooms learns each room's maximum airflow from the registry, to show
// airflow as a share of it: an office's 20 l/s and a lecture room's 900 l/s
// would not fit one colour scale. On an error the last known list is kept.
func (v *viz) refreshRooms(ctx context.Context) {
	devs, err := v.reg.List(ctx, registry.Filter{Kind: rooms.Damper, Status: "active"})
	if err != nil {
		log.Printf("registry: %v (keeping %d known rooms)", err, len(v.maxFlow))
		return
	}
	maxFlow := map[rooms.Key]float64{}
	for _, d := range devs {
		if k, err := rooms.Parse(d.Room); err == nil && d.Size.Max > 0 {
			maxFlow[k] = d.Size.Max
		}
	}
	v.mu.Lock()
	v.maxFlow = maxFlow
	v.mu.Unlock()
}

// airflow reads what each room's damper has reached from BuildSim, where the
// dampers report it, as a percentage of the room's maximum.
func (v *viz) airflow(ctx context.Context) map[rooms.Key]float64 {
	v.mu.Lock()
	maxFlow := v.maxFlow
	v.mu.Unlock()
	levels := map[string]bool{}
	for k := range maxFlow {
		levels[k.Level] = true
	}
	fan := rooms.Actuators[rooms.Damper].EquipmentType
	out := map[rooms.Key]float64{}
	for level := range levels {
		eq, err := v.bs.Equipment(ctx, level)
		if err != nil {
			continue // the layer is left out this round
		}
		for _, e := range eq {
			k := rooms.Key{Level: e.Level, Name: e.Room}
			if e.Type != fan || len(e.Actuators) == 0 || maxFlow[k] == 0 {
				continue
			}
			if flow, err := strconv.ParseFloat(e.Actuators[0].State, 64); err == nil {
				out[k] = 100 * flow / maxFlow[k]
			}
		}
	}
	return out
}

func (v *viz) publish(ctx context.Context) {
	err := v.bs.PutRoomLayers(ctx, v.board.Layers(v.airflow(ctx)))
	if err == nil {
		alerts := v.board.Alerts()
		body, _ := json.Marshal(alerts)
		if string(body) != v.lastAlerts || time.Since(v.alertsSentAt) >= RefreshAlerts {
			if err = v.bs.PutAlerts(ctx, alerts); err == nil {
				v.lastAlerts, v.alertsSentAt = string(body), time.Now()
			}
		}
	}
	// Log a failure once, and its end once: BuildSim being down for a minute
	// shouldn't write sixty lines.
	switch {
	case err != nil && !v.failing:
		log.Printf("BuildSim: %v (retrying every %v)", err, PublishEvery)
	case err == nil && v.failing:
		log.Printf("BuildSim: publishing again")
	}
	v.failing = err != nil
}
