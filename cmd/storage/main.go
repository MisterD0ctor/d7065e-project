// Command storage records every observation, truth, decision and event from
// MQTT as JSONL per run (D-4, FR-8) and serves history and models over HTTP
// (IF-10). It is the only process that touches the files.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/mqttx"
	"github.com/MisterD0ctor/d7065e-project/internal/store"
)

type service struct {
	st         *store.Store
	defaultRun string
	stored     atomic.Int64
	rejected   atomic.Int64
	duplicates atomic.Int64
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(env.String("DATA_DIR", "/data"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	s := &service{st: st, defaultRun: env.String("RUN_ID", "dev")}

	mq := mqttx.Connect("storage")
	for _, stream := range store.Streams {
		mq.Subscribe(stream+"/#", s.record)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /history", s.handleHistory)
	mux.HandleFunc("GET /runs", s.handleRuns)
	mux.HandleFunc("GET /runs/{run}/export", s.handleExport)
	mux.HandleFunc("PUT /models/{name}", s.handlePutModel)
	mux.HandleFunc("GET /models/{name}", s.handleGetModel)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "ok stored=%d duplicates=%d rejected=%d\n", s.stored.Load(), s.duplicates.Load(), s.rejected.Load())
	})
	srv := &http.Server{Addr: ":" + env.String("PORT", "8080"), Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func (s *service) record(topic string, payload []byte) {
	stream, _, _ := strings.Cut(topic, "/")
	ok, err := s.st.Append(stream, payload)
	switch {
	case err != nil:
		// A bad record is logged and counted, never silently dropped.
		s.rejected.Add(1)
		log.Printf("rejected %s: %v", topic, err)
	case !ok:
		s.duplicates.Add(1)
	default:
		s.stored.Add(1)
	}
}

func (s *service) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := store.Query{
		Run:  q.Get("run"),
		Room: q.Get("room"),
		Kind: q.Get("kind"),
		From: q.Get("from"),
		To:   q.Get("to"),
	}
	if query.Run == "" {
		query.Run = s.defaultRun
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	if err := s.st.History(w, query); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func (s *service) handleRuns(w http.ResponseWriter, _ *http.Request) {
	runs, err := s.st.Runs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, id := range runs {
		fmt.Fprintln(w, id)
	}
}

func (s *service) handleExport(w http.ResponseWriter, r *http.Request) {
	run := r.PathValue("run")
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", run+".tar"))
	if err := s.st.Export(w, run); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func (s *service) handlePutModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.st.PutModel(r.PathValue("name"), body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *service) handleGetModel(w http.ResponseWriter, r *http.Request) {
	name, body, err := s.st.Model(r.PathValue("name"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "no such model", http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Model-Name", name)
		w.Write(body)
	}
}
