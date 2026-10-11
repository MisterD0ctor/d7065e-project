package buildsim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eislab-cps/D7065E/occupancysim/internal/sim"
)

// The simulation clock in BuildSim.
//
// BuildSim has no clock, and what the simulator writes there (entities,
// occupancy) carries no simulated time: occupancy is refreshed at most every
// five real seconds, which is ten simulated minutes at factor 120. A reader
// that may talk only to BuildSim could not know what time it is in the
// building, nor which minute an occupancy snapshot belongs to.
//
// So the simulator also keeps one piece of equipment in BuildSim, the
// simulation clock, with two text sensors:
//
//	sim-clock-time     the simulated time, RFC 3339
//	sim-clock-minutes  the clock and the people in each room at each of the
//	                   last few whole minutes: the document GET /api/minutes
//	                   returns
//
// The record covers several minutes so that a reader who polls can't miss
// one, and what it reads depends only on the day's plan, not on when it
// polled.
const (
	ClockEquipmentID     = "sim-clock"
	ClockTimeSensorID    = "sim-clock-time"
	ClockMinutesSensorID = "sim-clock-minutes"

	// ClockMinutesKept is how many whole minutes the record holds. At factor
	// 120 that is five real seconds for a reader to come by.
	ClockMinutesKept = 10

	// clockInterval is how often the simulator looks whether a new minute
	// has to be written. It is well under half a simulated minute at the
	// highest factor in use (120).
	clockInterval = 100 * time.Millisecond
	// clockRefresh is how long the sensors may go unwritten while nothing
	// changes (a paused clock). It is how a BuildSim that restarted blank is
	// noticed and given its clock back.
	clockRefresh = 5 * time.Second
)

// ClockSource is the part of the simulation the clock publisher reads.
type ClockSource interface {
	Minutes(after time.Time) (sim.ClockState, []sim.MinuteOccupancy)
	Rooms() []sim.Room
}

// ClockPublisher writes the simulation clock and the per-minute occupancy
// record into BuildSim.
type ClockPublisher struct {
	client *Client
	source ClockSource

	// owned by the publishing goroutine
	registered bool
	lastMinute time.Time // newest minute written
	writtenAt  time.Time // real time of the last write
	failures   int
}

func NewClockPublisher(client *Client, source ClockSource) *ClockPublisher {
	return &ClockPublisher{client: client, source: source}
}

// Run publishes until the context ends.
func (p *ClockPublisher) Run(ctx context.Context) {
	ticker := time.NewTicker(clockInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := p.PublishOnce(ctx)
			switch {
			case err != nil:
				p.failures++
				if p.failures == 1 || p.failures%100 == 0 {
					log.Printf("simulation clock to BuildSim failed (%d times): %v", p.failures, err)
				}
			case p.failures > 0:
				log.Printf("simulation clock to BuildSim resumed after %d failures", p.failures)
				p.failures = 0
			}
		}
	}
}

// clockDocument is the value of the minutes sensor: the same JSON as
// GET /api/minutes.
type clockDocument struct {
	Clock   sim.ClockState        `json:"clock"`
	Minutes []sim.MinuteOccupancy `json:"minutes"`
}

// PublishOnce writes the clock sensors when the simulation has recorded a
// minute that is not in BuildSim yet (the next one, or another one after the
// clock was moved), or when nothing has been written for clockRefresh.
func (p *ClockPublisher) PublishOnce(ctx context.Context) error {
	_, newest := p.source.Minutes(time.Time{})
	if len(newest) == 0 {
		return nil // the simulation has not reached its first whole minute
	}
	minute := newest[0].Time
	if minute.Equal(p.lastMinute) && time.Since(p.writtenAt) < clockRefresh {
		return nil
	}
	// This read is what gets written, so the clock and the minutes in the
	// document belong together.
	clock, minutes := p.source.Minutes(minute.Add(-ClockMinutesKept * time.Minute))
	if len(minutes) == 0 {
		return nil // the clock was moved between the two reads; next time
	}
	record, err := json.Marshal(clockDocument{Clock: clock, Minutes: minutes})
	if err != nil {
		return err
	}
	if !p.registered {
		if err := p.register(ctx); err != nil {
			return err
		}
		p.registered = true
	}
	// The record first: a reader that sees the new time finds its minute.
	for _, w := range [][2]string{{ClockMinutesSensorID, string(record)}, {ClockTimeSensorID, clock.Time}} {
		if err := p.client.SetSensorValue(ctx, w[0], w[1]); err != nil {
			if errors.Is(err, errNotFound) {
				p.registered = false // BuildSim restarted blank: register again next time
			}
			return err
		}
	}
	p.lastMinute, p.writtenAt = minutes[len(minutes)-1].Time, time.Now()
	return nil
}

// register creates the clock's equipment. BuildSim wants every piece of
// equipment in a room, so it goes in the first one on the first floor;
// creating it again when it exists is not an error.
func (p *ClockPublisher) register(ctx context.Context) error {
	rooms := p.source.Rooms()
	if len(rooms) == 0 {
		return errors.New("no room to put the simulation clock in")
	}
	text := func(id, name, kind string) map[string]any {
		return map[string]any{"id": id, "name": name, "type": kind, "data_type": "text"}
	}
	return p.client.CreateEquipment(ctx, []map[string]any{{
		"id": ClockEquipmentID, "name": "Simulation clock", "type": "generic", "category": "simulation",
		"level": rooms[0].Level, "room": rooms[0].Name, "status": "running",
		"sensors": []map[string]any{
			text(ClockTimeSensorID, "Simulated time", "time"),
			text(ClockMinutesSensorID, "People per room, last minutes", "record"),
		},
		"actuators": []any{},
	}})
}

var errNotFound = errors.New("not found in BuildSim")

// CreateEquipment registers equipment with its sensors and actuators.
// Existing ids are left as they are.
func (c *Client) CreateEquipment(ctx context.Context, equipment any) error {
	return c.call(ctx, http.MethodPost, "/api/equipment/bulk", equipment)
}

// SetSensorValue writes a text sensor. It returns an error wrapping
// errNotFound when BuildSim does not know the sensor.
func (c *Client) SetSensorValue(ctx context.Context, sensorID, value string) error {
	body := map[string]string{"data_type": "text", "value": value}
	return c.call(ctx, http.MethodPut, "/api/sensors/"+url.PathEscape(sensorID)+"/value", body)
}

func (c *Client) call(ctx context.Context, method, path string, body any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s %s: %w", method, path, errNotFound)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("%s %s: BuildSim returned %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	return nil
}
