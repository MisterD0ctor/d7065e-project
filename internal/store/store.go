// Package store keeps the raw records of every run as append-only JSONL, one
// file per stream (D-4): <dir>/<run_id>/{obs,truth,decision,event}.jsonl.
// It also keeps trained models under <dir>/models. Nothing else reads these
// files; other processes go through the storage HTTP API (IF-10).
package store

import (
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Streams, named after the MQTT topic prefixes.
var Streams = []string{"obs", "truth", "decision", "event"}

// idPattern guards run ids and model names used as path elements.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var ErrBadID = errors.New("id must be letters, digits, '.', '_' or '-'")

type Store struct {
	dir string

	mu    sync.Mutex
	files map[string]*os.File // "<run>/<stream>" → open file
	seen  map[string]int64    // "<run>/<sensor_id>" → highest seq stored
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "models"), 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, files: map[string]*os.File{}, seen: map[string]int64{}}, nil
}

func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		f.Close()
	}
}

// header is the part of every record the store checks.
type header struct {
	RunID     string `json:"run_id"`
	Room      string `json:"room"`
	ModelTime string `json:"model_time"`
	Kind      string `json:"kind"`
	SensorID  string `json:"sensor_id"`
	Seq       int64  `json:"seq"`
}

// Append validates one record and appends it to its run's stream. It returns
// false with no error for a duplicate observation (QoS 1 redelivery).
func (s *Store) Append(stream string, record []byte) (bool, error) {
	if !validStream(stream) {
		return false, fmt.Errorf("unknown stream %q", stream)
	}
	var h header
	if err := json.Unmarshal(record, &h); err != nil {
		return false, fmt.Errorf("not JSON: %w", err)
	}
	if !idPattern.MatchString(h.RunID) {
		return false, fmt.Errorf("run_id %q: %w", h.RunID, ErrBadID)
	}
	if h.Room == "" {
		return false, errors.New("room missing")
	}
	if stream != "decision" || h.ModelTime != "" {
		// Decisions made in the REST fallback have no model time (IF-8).
		if _, err := time.Parse(time.RFC3339, h.ModelTime); err != nil {
			return false, fmt.Errorf("model_time %q: %w", h.ModelTime, err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if stream == "obs" {
		key := h.RunID + "/" + h.SensorID
		if h.Seq <= s.seen[key] {
			return false, nil
		}
		s.seen[key] = h.Seq
	}
	f, err := s.file(h.RunID, stream)
	if err != nil {
		return false, err
	}
	line := append(bytes.TrimSpace(record), '\n')
	if _, err := f.Write(line); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) file(run, stream string) (*os.File, error) {
	key := run + "/" + stream
	if f, ok := s.files[key]; ok {
		return f, nil
	}
	if err := os.MkdirAll(filepath.Join(s.dir, run), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, run, stream+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	s.files[key] = f
	return f, nil
}

func validStream(stream string) bool {
	for _, v := range Streams {
		if v == stream {
			return true
		}
	}
	return false
}

// Query selects records for GET /history.
type Query struct {
	Run  string
	Room string // empty = all rooms
	Kind string // co2, temp, occupancy, truth, true_occupancy, decision or event
	From string // model time, inclusive; empty = open
	To   string // model time, exclusive; empty = open
}

// streamFor maps a history kind to its stream and, for observations, the
// observation kind to keep.
func streamFor(kind string) (stream, obsKind string, err error) {
	switch kind {
	case "co2", "temp", "occupancy":
		return "obs", kind, nil
	case "truth", "true_occupancy":
		return "truth", "", nil
	case "decision", "event":
		return kind, "", nil
	}
	return "", "", fmt.Errorf("unknown kind %q", kind)
}

// History writes the matching records to w as JSONL, in the order stored,
// which is model-time order per room.
func (s *Store) History(w io.Writer, q Query) error {
	if !idPattern.MatchString(q.Run) {
		return fmt.Errorf("run %q: %w", q.Run, ErrBadID)
	}
	stream, obsKind, err := streamFor(q.Kind)
	if err != nil {
		return err
	}
	var from, to time.Time
	if q.From != "" {
		if from, err = time.Parse(time.RFC3339, q.From); err != nil {
			return fmt.Errorf("from: %w", err)
		}
	}
	if q.To != "" {
		if to, err = time.Parse(time.RFC3339, q.To); err != nil {
			return fmt.Errorf("to: %w", err)
		}
	}

	f, err := os.Open(filepath.Join(s.dir, q.Run, stream+".jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil // nothing recorded yet is an empty answer, not an error
	}
	if err != nil {
		return err
	}
	defer f.Close()

	bw := bufio.NewWriter(w)
	defer bw.Flush()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var h header
		if json.Unmarshal(sc.Bytes(), &h) != nil {
			continue
		}
		if q.Room != "" && h.Room != q.Room {
			continue
		}
		if obsKind != "" && h.Kind != obsKind {
			continue
		}
		if !from.IsZero() || !to.IsZero() {
			t, err := time.Parse(time.RFC3339, h.ModelTime)
			if err != nil || (!from.IsZero() && t.Before(from)) || (!to.IsZero() && !t.Before(to)) {
				continue
			}
		}
		bw.Write(sc.Bytes())
		bw.WriteByte('\n')
	}
	return sc.Err()
}

// Runs lists the recorded run ids, oldest first.
func (s *Store) Runs() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	type run struct {
		id  string
		mod time.Time
	}
	var runs []run
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "models" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		runs = append(runs, run{e.Name(), info.ModTime()})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].mod.Before(runs[j].mod) })
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.id
	}
	return ids, nil
}

// Export writes a run's JSONL files as a tar archive, for offline analysis.
func (s *Store) Export(w io.Writer, run string) error {
	if !idPattern.MatchString(run) {
		return fmt.Errorf("run %q: %w", run, ErrBadID)
	}
	s.mu.Lock() // no half-written lines in the archive
	defer s.mu.Unlock()
	tw := tar.NewWriter(w)
	for _, stream := range Streams {
		path := filepath.Join(s.dir, run, stream+".jsonl")
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: run + "/" + stream + ".jsonl", Mode: 0o644, Size: int64(len(b)), ModTime: time.Now()}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	return tw.Close()
}

// PutModel stores a model; it must be valid JSON.
func (s *Store) PutModel(name string, body []byte) error {
	if !idPattern.MatchString(name) {
		return fmt.Errorf("model %q: %w", name, ErrBadID)
	}
	if !json.Valid(body) {
		return errors.New("model is not valid JSON")
	}
	path := filepath.Join(s.dir, "models", name+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path) // readers never see a half-written model
}

// Model returns a stored model. name "latest" means the newest one.
func (s *Store) Model(name string) (string, []byte, error) {
	if name == "latest" {
		entries, err := os.ReadDir(filepath.Join(s.dir, "models"))
		if err != nil {
			return "", nil, err
		}
		var newest time.Time
		name = ""
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			if info.ModTime().After(newest) {
				newest, name = info.ModTime(), e.Name()[:len(e.Name())-len(".json")]
			}
		}
		if name == "" {
			return "", nil, os.ErrNotExist
		}
	}
	if !idPattern.MatchString(name) {
		return "", nil, fmt.Errorf("model %q: %w", name, ErrBadID)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "models", name+".json"))
	return name, b, err
}
