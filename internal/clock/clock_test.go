package clock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The shape occupancysim actually returns: everything is nested under "sim".
const state = `{"sim":{"clock":{"time":"2026-09-30T07:42:11Z","factor":60,"running":true,"weekend":false},
"rooms":[{"name":"1570","level":"level0","role":"fika","capacity":25}]}}`

func TestReadsNestedState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(state))
	}))
	defer srv.Close()
	c := New(srv.URL)

	now, err := c.Now(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 9, 30, 7, 42, 11, 0, time.UTC); !now.Time.Equal(want) || now.Factor != 60 || !now.Running {
		t.Errorf("got %+v", now)
	}

	caps, err := c.Capacities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps["level0/1570"] != 25 {
		t.Errorf("capacity of level0/1570 = %d, want 25", caps["level0/1570"])
	}
}

func TestMinutes(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/minutes" {
			http.NotFound(w, r)
			return
		}
		asked = r.URL.Query().Get("after")
		w.Write([]byte(`{"clock":{"time":"2026-09-21T10:16:25Z","factor":120,"running":true},
"minutes":[{"time":"2026-09-21T10:15:00Z","rooms":{"level0/1570":{"occupants":16}}},
           {"time":"2026-09-21T10:16:00Z","rooms":{"level0/1570":{"occupants":17,"guards":1}}}]}`))
	}))
	defer srv.Close()

	after := time.Date(2026, 9, 21, 10, 14, 0, 0, time.UTC)
	now, minutes, err := New(srv.URL).Minutes(context.Background(), after)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "2026-09-21T10:14:00Z" {
		t.Errorf("asked after=%q", asked)
	}
	if now.Factor != 120 || len(minutes) != 2 {
		t.Fatalf("clock %+v, %d minutes", now, len(minutes))
	}
	if got := minutes[1].Rooms["level0/1570"]; got.Occupants != 17 || got.Guards != 1 {
		t.Errorf("10:16 in 1570: %+v, want 17 occupants of which 1 guard", got)
	}
	if !minutes[0].Time.Equal(after.Add(time.Minute)) {
		t.Errorf("first minute %v", minutes[0].Time)
	}
}
