// Command viz is the dashboard (D-8, FR-10). It follows the observations,
// decisions, actuator states and events on MQTT and shows the building in two places
// (IF-11). In the BuildSim viewer: a heatmap layer per measure and the
// current alerts. On its own web page: graphs of each room and of the whole
// building over time, which the viewer has no place for.
//
// It is the only writer of room layers and alerts: BuildSim keeps one
// collection of each, and a second writer would erase the first. The viewer
// itself is provided infrastructure; what is ours is what gets mapped into it.
// viz writes to BuildSim and reads nothing from it: all it knows came over
// MQTT from the sensors, the controller and the actuators.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os/signal"
	"strings"
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
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
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

	lastAlerts   string    // JSON of the alerts last sent
	alertsSentAt time.Time // real time
	failing      bool      // the last write to BuildSim failed
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	v := &viz{
		board: dashboard.New(),
		bs:    buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090")),
		reg:   devreg.New(env.String("REGISTRY_URL", "http://registry:8080")),
	}
	v.refreshRooms(ctx)
	go v.serve(ctx, ":"+env.String("PORT", "8080"))

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
	mq.Subscribe("state/#", func(_ string, p []byte) {
		var st msg.State
		if json.Unmarshal(p, &st) == nil {
			v.board.State(st)
		}
	})
	mq.Subscribe("event/#", func(_ string, p []byte) {
		var e msg.Event
		if json.Unmarshal(p, &e) == nil {
			v.board.Event(e)
		}
	})
	// The graphs' past, from storage: after a restart they continue where
	// they were instead of starting empty.
	if storage := strings.TrimRight(env.String("STORAGE_URL", ""), "/"); storage != "" {
		go v.backfill(ctx, storage, env.String("RUN_ID", "dev"))
	}

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

// refreshRooms learns each room's sizing from the registry. The airflow is
// shown as a share of the room's maximum: an office's 20 l/s and a lecture
// room's 900 l/s would not fit one colour scale. On an error the last known
// list is kept.
func (v *viz) refreshRooms(ctx context.Context) {
	devs, err := v.reg.List(ctx, registry.Filter{Kind: rooms.Damper, Status: "active"})
	if err != nil {
		log.Printf("registry: %v (keeping the known rooms)", err)
		return
	}
	sizes := map[rooms.Key]sizing.Room{}
	for _, d := range devs {
		if k, err := rooms.Parse(d.Room); err == nil && d.Size.Max > 0 {
			sizes[k] = d.Size
		}
	}
	v.board.SetRooms(sizes)
}

func (v *viz) publish(ctx context.Context) {
	// The history and the alert log move on whether or not BuildSim answers.
	v.board.Tick()
	err := v.bs.PutRoomLayers(ctx, v.board.Layers())
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
