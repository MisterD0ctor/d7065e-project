package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/dashboard"
	"github.com/MisterD0ctor/d7065e-project/internal/msg"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

//go:embed index.html
var page []byte

// handler is the web page with the graphs and the two requests it makes
// (IF-11). The page only reads: nothing here changes the building.
func (v *viz) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	mux.HandleFunc("GET /api/overview", func(w http.ResponseWriter, r *http.Request) {
		reply(w, v.board.Overview(minutes(r)))
	})
	mux.HandleFunc("GET /api/room", func(w http.ResponseWriter, r *http.Request) {
		k, err := rooms.Parse(r.URL.Query().Get("room"))
		if err != nil {
			http.Error(w, "room: want <level>/<room>", http.StatusBadRequest)
			return
		}
		h, ok := v.board.RoomHistory(k, minutes(r))
		if !ok {
			http.Error(w, "nothing heard from that room", http.StatusNotFound)
			return
		}
		reply(w, h)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	return mux
}

func (v *viz) serve(ctx context.Context, addr string) {
	srv := &http.Server{Addr: addr, Handler: v.handler()}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// minutes is the time range a request asks for, six hours unless it says.
func minutes(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("minutes"))
	if err != nil {
		return 360
	}
	return min(max(n, 10), dashboard.Keep)
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// backfill reads the run's sensor readings and decisions so far from storage
// (IF-10) into the graphs' history, and nothing else: the layers and the
// alerts come from live messages only. The airflow the dampers had reached
// and the alert log are not in storage, so those start again from now.
//
// Storage may still be starting, so it is tried a few times. Without it the
// graphs simply start empty.
var backfillPause = 2 * time.Second

func (v *viz) backfill(ctx context.Context, storage, run string) {
	client := &http.Client{Timeout: 5 * time.Minute}
	var n int
	for _, kind := range []string{rooms.CO2, rooms.Temp, rooms.Occupancy, "decision"} {
		u := fmt.Sprintf("%s/history?run=%s&kind=%s", storage, url.QueryEscape(run), kind)
		var err error
		for attempt := 1; attempt <= 5; attempt++ {
			var got int
			if got, err = v.backfillFrom(ctx, client, u, kind == "decision"); err == nil {
				n += got
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backfillPause):
			}
		}
		if err != nil {
			log.Printf("graphs: no history from storage (%v); starting from now", err)
			return
		}
	}
	log.Printf("graphs: %d earlier records of run %s from storage", n, run)
}

func (v *viz) backfillFrom(ctx context.Context, client *http.Client, u string, decisions bool) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("storage: status %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		if decisions {
			var d msg.Decision
			if json.Unmarshal(sc.Bytes(), &d) == nil {
				v.board.BackfillDecision(d)
				n++
			}
		} else {
			var o msg.Observation
			if json.Unmarshal(sc.Bytes(), &o) == nil {
				v.board.BackfillObservation(o)
				n++
			}
		}
	}
	return n, sc.Err()
}
