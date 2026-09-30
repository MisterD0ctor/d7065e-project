package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/predict"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

// The four policies of FR-9. Everything else is identical between them.
const (
	Constant   = "constant"   // design flow, always
	Reactive   = "reactive"   // flow follows measured CO₂
	Predictive = "predictive" // reactive, plus flow for the people the profile expects
	Oracle     = "oracle"     // like predictive, but knows the true future occupancy
)

var policies = map[string]bool{Constant: true, Reactive: true, Predictive: true, Oracle: true}

// leadTime is how far ahead to ventilate: the room's CO₂ time constant at
// design flow (τ = V/q), kept between 10 and 30 minutes. A lecture room
// (τ ≈ 12 min) looks 12 minutes ahead; an office (τ ≈ 1 h) 30.
func leadTime(size sizing.Room) time.Duration {
	if size.Design <= 0 {
		return 30 * time.Minute
	}
	tau := time.Duration(size.VolumeM3 / (size.Design / 1000) * float64(time.Second))
	return min(max(tau, 10*time.Minute), 30*time.Minute)
}

// reactiveFlow rises linearly from the lowest allowed flow at ReactiveLow ppm
// to maximum at ReactiveHigh ppm.
func reactiveFlow(size sizing.Room, co2 float64) float64 {
	frac := min(max((co2-ReactiveLow)/(ReactiveHigh-ReactiveLow), 0), 1)
	return size.Empty + frac*(size.Max-size.Empty)
}

// peopleFlow is the flow the workplace rule asks for n people (AFS: 7 l/s
// per person plus 0.35 l/s·m²), capped at the room's maximum. No people, no
// demand: the actuator's floors take over.
func peopleFlow(size sizing.Room, n float64) float64 {
	if n <= 0 {
		return 0
	}
	return min(sizing.PerPersonLs*n+sizing.PerAreaLs*size.AreaM2, size.Max)
}

// airflow proposes a flow for one room. It asks for as little as the policy
// wants and leaves the floors and the CO₂ override to the actuator (D-6).
// people is the number to ventilate for: now and within the lead time.
func airflow(policy string, size sizing.Room, co2, people float64) float64 {
	switch policy {
	case Constant:
		return size.Design
	case Predictive, Oracle:
		return max(reactiveFlow(size, co2), peopleFlow(size, people))
	default:
		return reactiveFlow(size, co2)
	}
}

// forecast answers "how many people between t and t+lead?". ok is false when
// it has nothing to go on (no model yet, or no recording for the room).
type forecast interface {
	Expected(room string, t time.Time, lead time.Duration) (people float64, ok bool)
}

// profileForecast is the predictive policy's source: the newest trained
// profile in storage, reloaded periodically. It keeps the last good model if
// storage is unreachable (D-5).
type profileForecast struct {
	storage string
	http    *http.Client

	mu    sync.Mutex
	model *predict.Profile
}

// Load fetches the newest model. It reports the model's name when it
// differs from the one already loaded.
func (f *profileForecast) Load(ctx context.Context) (newName string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.storage+"/models/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("storage: status %d", resp.StatusCode)
	}
	var p predict.Profile
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.model != nil && f.model.Name == p.Name {
		return "", nil
	}
	f.model = &p
	return p.Name, nil
}

func (f *profileForecast) Expected(room string, t time.Time, lead time.Duration) (float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.model == nil {
		return 0, false
	}
	return f.model.Expected(room, t, lead), true
}

// oracleForecast replays the true occupancy recorded in an earlier run of the
// same seed and dates (D-7). It reads the recording, never the live truth:
// storage refuses to serve the live run's truth.
type oracleForecast struct {
	byRoom map[string]map[int64]int // room → model minute (Unix/60) → people
}

func loadOracle(ctx context.Context, client *http.Client, storage, run string) (*oracleForecast, error) {
	u := fmt.Sprintf("%s/history?run=%s&kind=true_occupancy", storage, url.QueryEscape(run))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("storage: run %s: status %d", run, resp.StatusCode)
	}
	o := &oracleForecast{byRoom: map[string]map[int64]int{}}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var r struct {
			Room      string `json:"room"`
			ModelTime string `json:"model_time"`
			Occupancy int    `json:"occupancy"`
		}
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, r.ModelTime)
		if err != nil {
			continue
		}
		if o.byRoom[r.Room] == nil {
			o.byRoom[r.Room] = map[int64]int{}
		}
		o.byRoom[r.Room][t.Unix()/60] = r.Occupancy
	}
	if len(o.byRoom) == 0 {
		return nil, fmt.Errorf("run %s has no recorded truth", run)
	}
	return o, sc.Err()
}

func (o *oracleForecast) Expected(room string, t time.Time, lead time.Duration) (float64, bool) {
	minutes, ok := o.byRoom[room]
	if !ok {
		return 0, false
	}
	var peak int
	for m := t.Unix() / 60; m <= t.Add(lead).Unix()/60; m++ {
		peak = max(peak, minutes[m])
	}
	return float64(peak), true
}
