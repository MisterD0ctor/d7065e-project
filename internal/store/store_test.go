package store

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

func obs(seq int, t, room, kind string) []byte {
	return []byte(`{"run_id":"r1","sensor_id":"` + room + `-` + kind + `","room":"` + room +
		`","kind":"` + kind + `","value":600,"model_time":"` + t + `","seq":` + strconv.Itoa(seq) + `}`)
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func lines(t *testing.T, s *Store, q Query) []string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.History(&buf, q); err != nil {
		t.Fatal(err)
	}
	out := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if out[0] == "" {
		return nil
	}
	return out
}

// A QoS 1 redelivery of the same (sensor_id, seq) is stored once.
func TestDeduplicatesObservations(t *testing.T) {
	s := open(t)
	rec := obs(1, "2026-09-14T10:00:00Z", "level0/1570", "co2")
	if ok, err := s.Append("obs", rec); !ok || err != nil {
		t.Fatalf("first append: %v %v", ok, err)
	}
	if ok, err := s.Append("obs", rec); ok || err != nil {
		t.Fatalf("duplicate was stored: %v %v", ok, err)
	}
	if n := len(lines(t, s, Query{Run: "r1", Kind: "co2"})); n != 1 {
		t.Fatalf("%d records, want 1", n)
	}
}

func TestHistoryFilters(t *testing.T) {
	s := open(t)
	for i, tm := range []string{"2026-09-14T10:00:00Z", "2026-09-14T10:01:00Z", "2026-09-14T10:02:00Z"} {
		s.Append("obs", obs(i+1, tm, "level0/1570", "co2"))
		s.Append("obs", obs(i+1, tm, "level0/1570", "temp"))
		s.Append("obs", obs(i+1, tm, "level0/A117", "co2"))
	}
	got := lines(t, s, Query{Run: "r1", Room: "level0/1570", Kind: "co2",
		From: "2026-09-14T10:01:00Z", To: "2026-09-14T10:02:00Z"})
	if len(got) != 1 || !strings.Contains(got[0], "10:01:00") {
		t.Fatalf("got %v, want only the 10:01 CO₂ reading of 1570", got)
	}
	if got := lines(t, s, Query{Run: "r1", Kind: "decision"}); got != nil {
		t.Fatalf("empty stream returned %v", got)
	}
}

func TestRejectsBadRecords(t *testing.T) {
	s := open(t)
	for name, rec := range map[string]string{
		"not json":       `{"run_id":`,
		"path in run id": `{"run_id":"../x","room":"level0/1","model_time":"2026-09-14T10:00:00Z"}`,
		"no room":        `{"run_id":"r1","model_time":"2026-09-14T10:00:00Z"}`,
		"bad time":       `{"run_id":"r1","room":"level0/1","model_time":"10:00"}`,
	} {
		if _, err := s.Append("truth", []byte(rec)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := s.Append("secrets", obs(1, "2026-09-14T10:00:00Z", "level0/1", "co2")); err == nil {
		t.Error("unknown stream accepted")
	}
}

func TestModels(t *testing.T) {
	s := open(t)
	if _, _, err := s.Model("latest"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("latest with no models: %v", err)
	}
	if err := s.PutModel("profile-1", []byte(`{"rooms":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.PutModel("bad", []byte(`{`)); err == nil {
		t.Error("invalid JSON model accepted")
	}
	name, b, err := s.Model("latest")
	if err != nil || name != "profile-1" || string(b) != `{"rooms":{}}` {
		t.Fatalf("latest = %q %s %v", name, b, err)
	}
}

func TestExport(t *testing.T) {
	s := open(t)
	s.Append("obs", obs(1, "2026-09-14T10:00:00Z", "level0/1570", "co2"))
	var buf bytes.Buffer
	if err := s.Export(&buf, "r1"); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	h, err := tr.Next()
	if err != nil || h.Name != "r1/obs.jsonl" {
		t.Fatalf("first entry %v %v", h, err)
	}
	b, _ := io.ReadAll(tr)
	if !strings.Contains(string(b), `"seq":1`) {
		t.Fatalf("archive content %s", b)
	}
}
