// Command truthlog is the evaluation's recorder (IF-5). The physical model
// keeps the rooms' true state in BuildSim, its only neighbour. truthlog
// copies that record to truth/<level>/<room> on MQTT, where only storage may
// read it, and adds the run id.
//
// It is not part of the control system. It exists so that a run can be
// judged afterwards against what really happened (NFR-1 to NFR-3), and so
// that the oracle baseline has a finished run to replay (D-7). Nothing the
// controller can reach carries this data: the controller doesn't talk to
// BuildSim, and the broker refuses it the truth topics.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/mqttx"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

// Poll is how often the record is read, in real time. It holds the last
// msg.RecordMinutes minutes, 2.5 s at factor 120, so none is missed.
const Poll = 250 * time.Millisecond

type recorder struct {
	read    func(ctx context.Context) (string, error) // the record sensor's value
	publish func(topic string, v any)
	runID   string

	last  time.Time // newest minute published; zero before the first
	quiet int       // polls in a row that failed, to log it once
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	bs := buildsim.New(env.String("BUILDSIM_URL", "http://buildsim:9090"))
	mq := mqttx.Connect("truthlog")
	r := &recorder{
		read: func(ctx context.Context) (string, error) {
			s, err := bs.SensorValue(ctx, msg.RecordSensor)
			return s.Value, err
		},
		publish: mq.Publish,
		runID:   env.String("RUN_ID", "dev"),
	}
	log.Printf("recording the truth of run %s", r.runID)

	tick := time.NewTicker(Poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.poll(ctx)
		}
	}
}

// poll publishes the minutes of the record that are newer than the last one
// published. A new recorder starts at the newest minute: the older ones in
// the record may already be in storage from before a restart. If the model
// clock was moved back it starts over there.
func (r *recorder) poll(ctx context.Context) {
	value, err := r.read(ctx)
	var rec msg.Record
	if err == nil && value != "" {
		err = json.Unmarshal([]byte(value), &rec)
	}
	if err != nil {
		if r.quiet++; r.quiet == 1 || r.quiet%240 == 0 {
			log.Printf("room-state record in BuildSim: %v", err)
		}
		return
	}
	r.quiet = 0
	if len(rec.Minutes) == 0 {
		return // the physical model has not started
	}
	newest, err := time.Parse(time.RFC3339, rec.Minutes[len(rec.Minutes)-1].ModelTime)
	if err != nil {
		return
	}
	switch {
	case r.last.IsZero():
		rec.Minutes = rec.Minutes[len(rec.Minutes)-1:]
	case newest.Before(r.last):
		log.Printf("model clock moved back from %s to %s; recording from there", msg.FormatTime(r.last), msg.FormatTime(newest))
		rec.Minutes = rec.Minutes[len(rec.Minutes)-1:]
		r.last = time.Time{}
	}
	for _, m := range rec.Minutes {
		t, err := time.Parse(time.RFC3339, m.ModelTime)
		if err != nil || !t.After(r.last) {
			continue
		}
		if gap := t.Sub(r.last); !r.last.IsZero() && gap > time.Minute {
			log.Printf("missed %v of the record before %s", gap-time.Minute, m.ModelTime)
		}
		for _, room := range m.Rooms {
			k, err := rooms.Parse(room.Room)
			if err != nil {
				continue
			}
			room.RunID = r.runID
			r.publish(msg.TruthTopic(k), room)
		}
		r.last = t
	}
}
