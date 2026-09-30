// Package clock reads the model clock owned by occupancysim (IF-1).
package clock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	url  string
	http *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		url:  strings.TrimRight(baseURL, "/") + "/api/state",
		http: &http.Client{Timeout: 500 * time.Millisecond},
	}
}

func (c *Client) Now(ctx context.Context) (Reading, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
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
			Clock struct {
				Time    string  `json:"time"`
				Factor  float64 `json:"factor"`
				Running bool    `json:"running"`
				Weekend bool    `json:"weekend"`
			} `json:"clock"`
		} `json:"sim"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return Reading{}, err
	}
	st := state.Sim.Clock
	t, err := time.Parse(time.RFC3339, st.Time)
	if err != nil {
		return Reading{}, fmt.Errorf("clock: %w", err)
	}
	return Reading{Time: t, Factor: st.Factor, Running: st.Running, Weekend: st.Weekend}, nil
}

// Capacities returns occupancysim's design capacity per room, keyed
// "<level>/<room>". occupancysim assigns the room roles (office, lecture,
// fika), so its capacities are the ones to size the ventilation by.
func (c *Client) Capacities(ctx context.Context) (map[string]int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
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
