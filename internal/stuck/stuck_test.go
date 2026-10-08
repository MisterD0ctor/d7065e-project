package stuck

import (
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/faults"
	"github.com/MisterD0ctor/d7065e-project/internal/roommodel"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

func TestFlat(t *testing.T) {
	d := New()
	for i := 1; i <= 4; i++ {
		if n, stuck := d.Flat("co2-0001", rooms.CO2, 512); stuck || n != i {
			t.Fatalf("reading %d of 512 ppm: n=%d stuck=%v", i, n, stuck)
		}
	}
	if n, stuck := d.Flat("co2-0001", rooms.CO2, 512); !stuck || n != 5 {
		t.Fatalf("fifth identical CO₂ reading: n=%d stuck=%v, want stuck", n, stuck)
	}
	// Another sensor's readings don't count towards this one's run.
	if _, stuck := d.Flat("co2-0002", rooms.CO2, 512); stuck {
		t.Error("a second sensor inherited the first one's run")
	}
	// One different reading and the sensor is trusted again.
	if n, stuck := d.Flat("co2-0001", rooms.CO2, 513); stuck || n != 1 {
		t.Errorf("after a new value: n=%d stuck=%v", n, stuck)
	}
	// Temperature repeats itself more often when healthy, so it needs longer.
	for i := 1; i <= 12; i++ {
		_, stuck := d.Flat("temp-0001", rooms.Temp, 20.7)
		if stuck != (i == 12) {
			t.Fatalf("temperature reading %d: stuck=%v", i, stuck)
		}
	}
	// The counter is never judged by repetition: an empty room reads 0 all night.
	for range 600 {
		if _, stuck := d.Flat("occ-0001", rooms.Occupancy, 0); stuck {
			t.Fatal("an occupancy counter was called stuck for repeating 0")
		}
	}
}

// reading is what a sensor publishes: the true value with the sensor's noise,
// rounded the way cmd/sensor formats it.
func reading(kind string, truth float64, rng *rand.Rand) float64 {
	v := faults.Noise(kind, truth, rng)
	digits := 0
	if kind == rooms.Temp {
		digits = 1
	}
	v, _ = strconv.ParseFloat(strconv.FormatFloat(max(v, 0), 'f', digits, 64), 64)
	return v
}

// repeatChance is the chance that k readings in a row from a healthy sensor
// are identical: the sum, over every value the sensor can show, of (the
// chance of showing it)^k.
func repeatChance(truth, sigma, step float64, k int) float64 {
	cdf := func(z float64) float64 { return 0.5 * (1 + math.Erf(z/math.Sqrt2)) }
	nearest := math.Round(truth/step) * step
	var sum float64
	for i := -400; i <= 400; i++ {
		v := nearest + float64(i)*step
		p := cdf((v+step/2-truth)/sigma) - cdf((v-step/2-truth)/sigma)
		sum += math.Pow(p, float64(k))
	}
	return sum
}

// Where FlatAfter comes from, and a check of the sum against the real noise
// code. The level never changes here, which is the worst case: a level that
// moves repeats less.
func TestFlatAfterIsTheShortestSafeRun(t *testing.T) {
	const (
		perDay  = 50 * 1440 // readings a day per kind, 50 rooms
		week    = 5 * perDay
		maxRate = 1.0 / 100 // false alarms per building-day we accept
	)
	type sensor struct {
		kind               string
		truth, sigma, step float64
		short              int // a run length common enough to count in a week
	}
	for _, s := range []sensor{
		{rooms.CO2, sizing.OutdoorCO2, co2Noise(sizing.OutdoorCO2), 1, 2},
		{rooms.Temp, 21.03, 0.15, 0.1, 3}, // σ from faults.Noise
	} {
		limit := FlatAfter[s.kind]
		if rate := repeatChance(s.truth, s.sigma, s.step, limit) * perDay; rate >= maxRate {
			t.Errorf("%s: a run of %d comes %.3f times a day in a healthy building, want under %.2f", s.kind, limit, rate, maxRate)
		}
		if rate := repeatChance(s.truth, s.sigma, s.step, limit-1) * perDay; rate < maxRate {
			t.Errorf("%s: a run of %d is already rare enough (%.4f a day); the limit could be lower", s.kind, limit-1, rate)
		}

		// One week of readings through the sensor's own noise and rounding.
		rng := rand.New(rand.NewPCG(1, 2))
		d := New()
		reached := 0 // readings that completed a run of at least s.short
		for range week {
			n, stuck := d.Flat("s", s.kind, reading(s.kind, s.truth, rng))
			if stuck {
				t.Fatalf("%s: a healthy sensor was called stuck after %d identical readings", s.kind, n)
			}
			if n >= s.short {
				reached++
			}
		}
		want := repeatChance(s.truth, s.sigma, s.step, s.short) * week
		if got := float64(reached); math.Abs(got-want) > 0.1*want {
			t.Errorf("%s: %0.f runs of %d in a week of readings, the sum says %.0f", s.kind, got, s.short, want)
		}
	}
}

// sim runs one room minute by minute with the project's own physics and
// sensor noise, and feeds the detector what the controller would get.
type sim struct {
	size  sizing.Room
	room  *roommodel.Room
	det   *Detector
	rng   *rand.Rand
	now   time.Time
	flow  float64  // what the damper is at, l/s
	stuck *float64 // the counter's frozen value; nil = healthy
	count float64  // the counter's last reading
	co2   float64  // the CO₂ sensor's last reading
}

var key = rooms.Key{Level: "level0", Name: "A109"}

func newSim(size sizing.Room, seed uint64) *sim {
	return &sim{
		size: size, room: roommodel.New(size), det: New(),
		rng: rand.New(rand.NewPCG(seed, 99)),
		now: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), flow: size.Empty,
	}
}

// minute advances one model minute with `people` in the room, then lets the
// policy pick the next minute's flow from the two readings. It returns the
// detector's verdict on the counter.
func (s *sim) minute(people int, policy func(s *sim) float64) (string, bool) {
	s.room.Step(time.Minute, roommodel.Inputs{Occupancy: people, AirflowLs: s.flow, Setpoint: 21})
	s.now = s.now.Add(time.Minute)
	s.count = reading(rooms.Occupancy, float64(people), s.rng)
	if s.stuck != nil {
		s.count = *s.stuck
	}
	s.co2 = reading(rooms.CO2, s.room.CO2, s.rng)
	s.det.Count(key, s.now, s.count)
	s.det.CO2(key, s.now, s.co2)
	why, stuck := s.det.Counter(key, s.size, s.now)
	s.flow = min(max(policy(s), 0), s.size.Max)
	return why, stuck
}

// The detector must hold whatever the damper does, so the tests drive it to
// both ends as well as the way the predictive policy would.
var policies = map[string]func(*sim) float64{
	"damper fully open":   func(s *sim) float64 { return s.size.Max },
	"damper at the floor": func(s *sim) float64 { return s.size.Empty },
	"flow for the count and the CO₂": func(s *sim) float64 {
		reactive := s.size.Empty + min(max((s.co2-600)/400, 0), 1)*(s.size.Max-s.size.Empty)
		people := 0.0
		if s.count >= 1 {
			people = sizing.PerPersonLs*s.count + sizing.PerAreaLs*s.size.AreaM2
		}
		return max(reactive, people)
	},
}

var (
	lectureRoom = sizing.ForCapacity(100, 50)
	office      = sizing.ForCapacity(12, 1)
)

// lectures is a lecture room's day: three lectures with breaks between.
func lectures(t time.Time) int {
	switch m := t.Hour()*60 + t.Minute(); {
	case m >= 8*60+15 && m < 10*60:
		return 40
	case m >= 10*60+15 && m < 12*60:
		return 25
	case m >= 13*60+15 && m < 15*60:
		return 45
	}
	return 0
}

// officeHours is one person in an office, out for lunch.
func officeHours(t time.Time) int {
	if h := t.Hour(); (h >= 8 && h < 12) || (h >= 13 && h < 17) {
		return 1
	}
	return 0
}

// A healthy counter is never blamed: three days, both rooms, every damper
// behaviour, ten different noise streams each.
func TestHealthyCounterIsTrusted(t *testing.T) {
	type scenario struct {
		name   string
		size   sizing.Room
		people func(time.Time) int
	}
	for _, sc := range []scenario{{"lecture room", lectureRoom, lectures}, {"office", office, officeHours}} {
		for name, policy := range policies {
			for seed := uint64(1); seed <= 10; seed++ {
				s := newSim(sc.size, seed)
				for range 3 * 1440 {
					if why, stuck := s.minute(sc.people(s.now), policy); stuck {
						t.Fatalf("%s, %s, seed %d, %s: healthy counter blamed: %s", sc.name, name, seed, s.now.Format("Mon 15:04"), why)
					}
				}
			}
		}
	}
}

// T-12: the counter freezes in the middle of a lecture. Nothing is wrong with
// what it says until the room empties at 10:00; then CO₂ falls below what the
// 40 people it still reports could leave, and it is found.
//
// With the damper held at the floor it is never blamed either, but not found:
// the air keeps the lecture's CO₂ for hours, so the counter stays believable.
// The system doesn't do that to a room it thinks is full; the case is here
// to show what the verdict rests on.
func TestCounterStuckDuringALecture(t *testing.T) {
	for name, policy := range policies {
		s := newSim(lectureRoom, 3)
		ends := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
		var found time.Time
		var why string
		for s.now.Before(ends.Add(2 * time.Hour)) {
			if s.now.Equal(time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)) {
				frozen := 40.0
				s.stuck = &frozen
			}
			people := 0
			if s.now.Before(ends) {
				people = lectures(s.now)
			}
			w, stuck := s.minute(people, policy)
			if stuck && found.IsZero() {
				found, why = s.now, w
			}
		}
		atFloor := name == "damper at the floor"
		switch {
		case found.IsZero() && !atFloor:
			t.Errorf("%s: a counter stuck at 40 in an empty room was never found", name)
		case !found.IsZero() && atFloor:
			t.Errorf("%s: found after all (%s): update the comment above", name, why)
		case atFloor:
		case found.Before(ends):
			t.Errorf("%s: blamed at %s, while the room was still full: %s", name, found.Format("15:04"), why)
		case found.Sub(ends) > 30*time.Minute:
			t.Errorf("%s: took %.0f min after the room emptied", name, found.Sub(ends).Minutes())
		case !strings.Contains(why, "read 40"):
			t.Errorf("%s: verdict doesn't name the stuck value: %s", name, why)
		default:
			t.Logf("%s: found %.0f min after the room emptied", name, found.Sub(ends).Minutes())
		}
	}
}

// A counter stuck at 0 overnight is found when the first lecture walks in:
// CO₂ rises, and only people can make it rise.
func TestCounterStuckAtZero(t *testing.T) {
	for name, policy := range policies {
		s := newSim(lectureRoom, 4)
		zero := 0.0
		s.stuck = &zero
		starts := time.Date(2026, 9, 21, 8, 15, 0, 0, time.UTC)
		var found time.Time
		for s.now.Before(starts.Add(time.Hour)) {
			if _, stuck := s.minute(lectures(s.now), policy); stuck && found.IsZero() {
				found = s.now
			}
		}
		switch {
		case found.IsZero():
			t.Errorf("%s: a counter stuck at 0 in a room of 40 was never found", name)
		case found.Before(starts):
			t.Errorf("%s: blamed at %s, before anybody came", name, found.Format("15:04"))
		case found.Sub(starts) > 15*time.Minute:
			t.Errorf("%s: took %.0f min", name, found.Sub(starts).Minutes())
		}
	}
}

// The same in an office: the counter keeps saying 1 after its occupant has
// gone home. A small room at a small flow forgets slowly, so this takes
// longer than in a lecture room.
func TestCounterStuckInAnOffice(t *testing.T) {
	s := newSim(office, 5)
	leaves := time.Date(2026, 9, 21, 17, 0, 0, 0, time.UTC)
	var found time.Time
	for s.now.Before(leaves.Add(4 * time.Hour)) {
		if s.now.Equal(leaves.Add(-time.Hour)) {
			one := 1.0
			s.stuck = &one
		}
		if _, stuck := s.minute(officeHours(s.now), policies["flow for the count and the CO₂"]); stuck && found.IsZero() {
			found = s.now
		}
	}
	if found.IsZero() || found.Before(leaves) {
		t.Fatalf("office counter stuck at 1: found at %v, want after 17:00", found)
	}
	t.Logf("found %.0f min after the office emptied", found.Sub(leaves).Minutes())
}

// A verdict lasts until the counter moves, and not a reading longer.
func TestTrustReturnsWhenTheCounterMoves(t *testing.T) {
	s := newSim(lectureRoom, 6)
	frozen := 40.0
	s.stuck = &frozen
	blamed := false
	for range 120 {
		_, blamed = s.minute(0, policies["damper fully open"])
	}
	if !blamed {
		t.Fatal("a counter reading 40 in a room empty for two hours was not found")
	}
	s.stuck = nil // repaired
	for i := range 30 {
		if why, stuck := s.minute(0, policies["damper fully open"]); stuck && s.count != frozen {
			t.Fatalf("minute %d after the repair, counter reading %v: still blamed: %s", i, s.count, why)
		}
	}
}

// The limit, pinned down: three people too many in a 50-seat room would add
// 17 ppm at full flow, which is inside the CO₂ sensor's noise. It is not
// found. The cost is the flow for three people.
func TestSmallOvercountInABigRoomGoesUnseen(t *testing.T) {
	s := newSim(lectureRoom, 7)
	three := 3.0
	s.stuck = &three
	for range 1440 {
		if why, stuck := s.minute(0, policies["damper fully open"]); stuck {
			t.Fatalf("found after all (%s): update the documented limit", why)
		}
	}
}

// A pause in the counter's readings starts its run again: nothing is known
// about what it would have said.
func TestGapRestartsTheRun(t *testing.T) {
	d := New()
	t0 := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	d.Count(key, t0, 40)
	for m := 1; m <= 60; m++ {
		d.CO2(key, t0.Add(time.Duration(m)*time.Minute), 420) // an empty room's air
	}
	// The counter says 40 again an hour later, having said nothing in between.
	d.Count(key, t0.Add(60*time.Minute), 40)
	if why, stuck := d.Counter(key, lectureRoom, t0.Add(60*time.Minute)); stuck {
		t.Errorf("blamed for an hour it never reported on: %s", why)
	}
}
