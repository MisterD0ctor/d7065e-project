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
