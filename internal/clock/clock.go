// Package clock reads what occupancysim owns (IF-1): the model clock, the
// room capacities, and who was in each room at every whole model minute.
package clock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Reading struct {
	Time    time.Time
	Factor  float64 // model seconds per real second
	Running bool
	Weekend bool
}

type Client struct {
	base string
	http *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{Timeout: 500 * time.Millisecond},
	}
}

func (c *Client) Now(ctx context.Context) (Reading, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/state", nil)
	if err != nil {
		return Reading{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Reading{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Reading{}, fmt.Errorf("clock: status %d", resp.StatusCode)
	}
	var state struct {
		Sim struct {
			Clock clockJSON `json:"clock"`
		} `json:"sim"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return Reading{}, err
	}
	return state.Sim.Clock.reading()
}

// clockJSON is occupancysim's clock as it appears in its responses.
type clockJSON struct {
	Time    string  `json:"time"`
	Factor  float64 `json:"factor"`
	Running bool    `json:"running"`
	Weekend bool    `json:"weekend"`
}

func (c clockJSON) reading() (Reading, error) {
	t, err := time.Parse(time.RFC3339, c.Time)
	if err != nil {
		return Reading{}, fmt.Errorf("clock: %w", err)
	}
	return Reading{Time: t, Factor: c.Factor, Running: c.Running, Weekend: c.Weekend}, nil
}

// RoomCount is how many people are in a room. Guards are counted in
// Occupants and again in Guards: a guard on patrol breathes like anyone else,
// but a room with only a guard in it is not in use.
type RoomCount struct {
	Occupants int `json:"occupants"`
	Guards    int `json:"guards"`
}

// Minute is who was in each room at one whole model minute, keyed
// "<level>/<room>". Rooms with nobody in them are left out.
type Minute struct {
	Time  time.Time            `json:"time"`
	Rooms map[string]RoomCount `json:"rooms"`
}

// Minutes returns the clock and the whole-minute occupancy occupancysim
// recorded after the given model time, oldest first. A zero time returns only
// the newest minute, which is where a reader starts. Each entry is the state
// exactly on its minute and depends only on the day's plan, so every run of
// the same date sees the same sequence (NFR-7), unlike BuildSim's occupancy,
// which carries no time and is refreshed on a wall-clock schedule.
func (c *Client) Minutes(ctx context.Context, after time.Time) (Reading, []Minute, error) {
	u := c.base + "/api/minutes"
	if !after.IsZero() {
		u += "?after=" + url.QueryEscape(after.Format(time.RFC3339))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Reading{}, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Reading{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Reading{}, nil, fmt.Errorf("occupancysim minutes: status %d", resp.StatusCode)
	}
	var body struct {
		Clock   clockJSON `json:"clock"`
		Minutes []Minute  `json:"minutes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Reading{}, nil, err
	}
	r, err := body.Clock.reading()
	return r, body.Minutes, err
}

// Capacities returns occupancysim's design capacity per room, keyed
// "<level>/<room>". occupancysim assigns the room roles (office, lecture,
// fika), so its capacities are the ones to size the ventilation by.
func (c *Client) Capacities(ctx context.Context) (map[string]int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/state", nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("occupancysim: status %d", resp.StatusCode)
	}
	var state struct {
		Sim struct {
			Rooms []struct {
				Name     string `json:"name"`
				Level    string `json:"level"`
				Capacity int    `json:"capacity"`
			} `json:"rooms"`
		} `json:"sim"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range state.Sim.Rooms {
		out[r.Level+"/"+r.Name] = r.Capacity
	}
	return out, nil
}
