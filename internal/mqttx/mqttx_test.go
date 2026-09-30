package mqttx

import (
	"testing"
	"time"
)

func TestSilenceFollowsArrivalRate(t *testing.T) {
	var s Silence
	t0 := time.Unix(0, 0)
	if !s.Silent(t0) {
		t.Fatal("nothing ever arrived: should be silent")
	}
	for i := range 10 { // one message per second, as at factor 60
		s.Arrived(t0.Add(time.Duration(i) * time.Second))
	}
	last := t0.Add(9 * time.Second)
	if s.Silent(last.Add(2 * time.Second)) {
		t.Error("2 s after a 1 s stream: not silent yet")
	}
	if !s.Silent(last.Add(3 * time.Second)) {
		t.Error("3 s after a 1 s stream: should be silent")
	}
}

func TestDedup(t *testing.T) {
	d := NewDedup()
	if !d.Fresh("s", 1) || d.Fresh("s", 1) || d.Fresh("s", 0) || !d.Fresh("s", 2) {
		t.Error("dedup accepted a repeat or refused a new seq")
	}
}
