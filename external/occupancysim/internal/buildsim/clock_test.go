package buildsim_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/eislab-cps/D7065E/occupancysim/internal/buildsim"
	"github.com/eislab-cps/D7065E/occupancysim/internal/sim"
)

// minuteLog is a simulation reduced to its clock and its minute log.
type minuteLog struct {
	now     time.Time
	minutes []sim.MinuteOccupancy
}

func (m *minuteLog) tick(people int) {
	m.now = m.now.Add(time.Minute)
	m.minutes = append(m.minutes, sim.MinuteOccupancy{Time: m.now, Rooms: map[string]sim.RoomCount{"level0/A109": {Occupants: people}}})
}

func (m *minuteLog) Minutes(after time.Time) (sim.ClockState, []sim.MinuteOccupancy) {
	clock := sim.ClockState{Time: m.now.Format(time.RFC3339), Factor: 60, Running: true}
	if len(m.minutes) == 0 {
		return clock, nil
	}
	if after.IsZero() {
		return clock, m.minutes[len(m.minutes)-1:]
	}
	var out []sim.MinuteOccupancy
	for _, x := range m.minutes {
		if x.Time.After(after) {
			out = append(out, x)
		}
	}
	return clock, out
}

func (m *minuteLog) Rooms() []sim.Room { return []sim.Room{{Name: "A109", Level: "level0"}} }

// sensorStore is BuildSim reduced to equipment registration and text sensors.
type sensorStore struct {
	mu       sync.Mutex
	values   map[string]string // sensor id → value; a sensor exists once registered
	creates  int
	writes   int
	location string
}

func (s *sensorStore) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/equipment/bulk", func(w http.ResponseWriter, r *http.Request) {
		var eq []struct {
			Level, Room string
			Sensors     []struct{ ID string }
		}
		if err := json.NewDecoder(r.Body).Decode(&eq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.creates++
		for _, e := range eq {
			s.location = e.Level + "/" + e.Room
			for _, sensor := range e.Sensors {
				if _, ok := s.values[sensor.ID]; !ok {
					s.values[sensor.ID] = ""
				}
			}
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("PUT /api/sensors/{id}/value", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Value string }
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.values[r.PathValue("id")]; !ok {
			http.Error(w, `{"error":"sensor not found"}`, http.StatusNotFound)
			return
		}
		s.values[r.PathValue("id")] = body.Value
		s.writes++
	})
	return httptest.NewServer(mux)
}

// The clock equipment appears in BuildSim with the simulated time and a
// record of the last minutes, is written once per recorded minute, and comes
// back by itself after BuildSim restarts blank.
func TestClockIsPublishedToBuildSim(t *testing.T) {
	store := &sensorStore{values: map[string]string{}}
	srv := store.server()
	defer srv.Close()
	log := &minuteLog{now: time.Date(2026, 9, 21, 9, 59, 0, 0, time.UTC)}
	pub := buildsim.NewClockPublisher(buildsim.New(srv.URL), log)
	ctx := context.Background()

	if err := pub.PublishOnce(ctx); err != nil || store.creates != 0 {
		t.Fatalf("before the first whole minute nothing is written: err=%v creates=%d", err, store.creates)
	}
	for i := range 14 {
		log.tick(i)
		if err := pub.PublishOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if store.location != "level0/A109" || store.creates != 1 {
		t.Errorf("clock equipment: in %q, registered %d times", store.location, store.creates)
	}
	if got := store.values[buildsim.ClockTimeSensorID]; got != "2026-09-21T10:13:00Z" {
		t.Errorf("time sensor: %q", got)
	}
	var doc struct {
		Clock   sim.ClockState        `json:"clock"`
		Minutes []sim.MinuteOccupancy `json:"minutes"`
	}
	if err := json.Unmarshal([]byte(store.values[buildsim.ClockMinutesSensorID]), &doc); err != nil {
		t.Fatalf("minutes sensor is not the /api/minutes document: %v", err)
	}
	if len(doc.Minutes) != buildsim.ClockMinutesKept || doc.Clock.Factor != 60 {
		t.Fatalf("record: %d minutes, clock %+v", len(doc.Minutes), doc.Clock)
	}
	first, last := doc.Minutes[0], doc.Minutes[len(doc.Minutes)-1]
	if !last.Time.Equal(log.now) || last.Rooms["level0/A109"].Occupants != 13 || first.Rooms["level0/A109"].Occupants != 4 {
		t.Errorf("record runs from %v (%d people) to %v (%d people)", first.Time, first.Rooms["level0/A109"].Occupants, last.Time, last.Rooms["level0/A109"].Occupants)
	}

	// No new minute: nothing is written again.
	writes := store.writes
	if err := pub.PublishOnce(ctx); err != nil || store.writes != writes {
		t.Errorf("a second publish of the same minute wrote again: err=%v", err)
	}

	// BuildSim restarts blank. The next minute fails once, then the clock is back.
	store.mu.Lock()
	store.values = map[string]string{}
	store.mu.Unlock()
	log.tick(20)
	if err := pub.PublishOnce(ctx); err == nil {
		t.Fatal("a write to a blank BuildSim succeeded")
	}
	if err := pub.PublishOnce(ctx); err != nil || store.creates != 2 || store.values[buildsim.ClockTimeSensorID] != "2026-09-21T10:14:00Z" {
		t.Errorf("after the restart: err=%v, registered %d times, time %q", err, store.creates, store.values[buildsim.ClockTimeSensorID])
	}
}
