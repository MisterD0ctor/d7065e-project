package dedup

import "testing"

func TestRedeliveriesAreDropped(t *testing.T) {
	s := New()
	if !s.Fresh("co2-0001", 100) || !s.Fresh("co2-0001", 160) {
		t.Fatal("new readings were dropped")
	}
	if s.Fresh("co2-0001", 160) || s.Fresh("co2-0001", 100) {
		t.Error("a reading delivered twice was accepted twice")
	}
	if !s.Fresh("co2-0002", 160) {
		t.Error("another sensor's reading at the same time was dropped")
	}
}

// The bug this replaced: one reading with a later time, and everything after
// it looked old. A sensor that starts in a new run can hear the previous
// run's last minute first; the run's own readings, all earlier, must still
// get through.
func TestALaterTimeDoesNotBlockEarlierOnes(t *testing.T) {
	s := New()
	const leftover = 1_790_000_000 // the previous run's last minute
	if !s.Fresh("co2-0001", leftover) {
		t.Fatal("the stray reading itself is new")
	}
	for seq := int64(1_789_500_000); seq < 1_789_500_000+60*300; seq += 60 {
		if !s.Fresh("co2-0001", seq) {
			t.Fatalf("reading at %d dropped because an unrelated later one was seen first", seq)
		}
	}
}

// Only the recent past is remembered: a redelivery repeats something recent.
func TestMemoryIsShort(t *testing.T) {
	s := New()
	for seq := int64(0); seq <= Remember; seq++ {
		s.Fresh("s", seq)
	}
	if !s.Fresh("s", 0) {
		t.Errorf("a reading from more than %d readings ago is still remembered", Remember)
	}
	if s.Fresh("s", Remember) {
		t.Error("the newest reading was forgotten")
	}
}
