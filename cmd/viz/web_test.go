package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/dashboard"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

var a109 = rooms.Key{Level: "level0", Name: "A109"}

func reading(kind string, value float64, minute int) msg.Observation {
	t := time.Date(2026, 9, 21, 10, minute, 0, 0, time.UTC)
	return msg.Observation{Room: a109.String(), Kind: kind, Value: value, ModelTime: msg.FormatTime(t)}
}

func get(t *testing.T, srv *httptest.Server, path string, into any) int {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	return resp.StatusCode
}

// The page and its two requests (IF-11).
func TestPageAndItsRequests(t *testing.T) {
	v := &viz{board: dashboard.New()}
	v.board.SetRooms(map[rooms.Key]sizing.Room{a109: sizing.ForCapacity(12, 1)})
	for m := range 5 {
		v.board.Observe(reading(rooms.CO2, 600+float64(m), m))
	}
	v.board.Tick(map[rooms.Key]float64{a109: 8})
	srv := httptest.NewServer(v.handler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil || resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("page: %v %v", resp, err)
	}
	resp.Body.Close()

	var o dashboard.Overview
	if code := get(t, srv, "/api/overview?minutes=60", &o); code != http.StatusOK {
		t.Fatalf("overview: status %d", code)
	}
	if len(o.Rooms) != 1 || o.Rooms[0].Name != "A109" || o.Rooms[0].CO2 == nil || *o.Rooms[0].CO2 != 604 || len(o.Building.CO2Max) != 5 {
		t.Errorf("overview: %+v", o)
	}

	var h dashboard.RoomHistory
	if code := get(t, srv, "/api/room?room=level0/A109&minutes=3", &h); code != http.StatusOK {
		t.Fatalf("room: status %d", code)
	}
	// minutes below the shortest range are read as the shortest, ten minutes.
	if len(h.CO2) != 5 || h.CO2[4] != 604 || h.Flow[4] != 8 || h.Size.Capacity != 1 {
		t.Errorf("room history: %+v", h)
	}
	if code := get(t, srv, "/api/room?room=level0/nowhere", nil); code != http.StatusNotFound {
		t.Errorf("a room nobody has heard from: status %d, want 404", code)
	}
	if code := get(t, srv, "/api/room?room=A109", nil); code != http.StatusBadRequest {
		t.Errorf("a room without its level: status %d, want 400", code)
	}
}

// After a restart the graphs get their past from storage (IF-10), even if
// storage is not up yet at the first try.
func TestBackfillFromStorage(t *testing.T) {
	backfillPause = time.Millisecond
	var calls atomic.Int32
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/history" || r.URL.Query().Get("run") != "eval-1" {
			http.Error(w, "unexpected request "+r.URL.String(), http.StatusBadRequest)
			return
		}
		enc := json.NewEncoder(w)
		switch kind := r.URL.Query().Get("kind"); kind {
		case rooms.CO2:
			enc.Encode(reading(kind, 640, 0))
			enc.Encode(reading(kind, 655, 1))
			fmt.Fprintln(w, "not json") // a damaged line is skipped, not fatal
		case rooms.Occupancy:
			enc.Encode(reading(kind, 7, 1))
		case "decision":
			flow, expected := 35.0, 9.0
			enc.Encode(msg.Decision{
				Room: a109.String(), ModelTime: reading("", 0, 1).ModelTime, Mode: "degraded_co2",
				AirflowTarget: &flow, AppliedTarget: flow, PredictedOccupancy: &expected, Status: 202,
			})
		}
	}))
	defer storage.Close()

	v := &viz{board: dashboard.New()}
	v.backfill(context.Background(), storage.URL, "eval-1")

	h, ok := v.board.RoomHistory(a109, 60)
	if !ok || len(h.CO2) != 2 || h.CO2[0] != 640 || h.CO2[1] != 655 || h.People[1] != 7 || h.Target[1] != 35 || h.Expected[1] != 9 {
		t.Fatalf("history after the backfill: ok=%v %+v", ok, h)
	}
	// The old decision's mode is history, not an alert now.
	if alerts := v.board.Alerts(); len(alerts) != 0 {
		t.Errorf("the backfill raised alerts: %+v", alerts)
	}
}

// Without storage the dashboard still starts; the graphs begin empty.
func TestBackfillGivesUp(t *testing.T) {
	backfillPause = time.Millisecond
	storage := httptest.NewServer(http.NotFoundHandler())
	defer storage.Close()
	v := &viz{board: dashboard.New()}
	v.backfill(context.Background(), storage.URL, "eval-1") // must return
	if _, ok := v.board.RoomHistory(a109, 60); ok {
		t.Error("history from a storage that answered 404")
	}
}
