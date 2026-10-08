// Package buildsim is a thin client for the parts of the BuildSim REST API
// our services use. Values and states are strings on the wire; callers parse
// and format them.
package buildsim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	base string
	http *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		// Per-call deadline; Floor overrides it through its context.
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// StatusError is returned for any non-2xx response.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("buildsim: %d %s", e.Code, e.Body) }

// IsNotFound reports whether err is a 404 from BuildSim.
func IsNotFound(err error) bool {
	se, ok := err.(*StatusError)
	return ok && se.Code == http.StatusNotFound
}

type Room struct {
	ID   int     `json:"id"`
	Name string  `json:"name"`
	Area float64 `json:"area"` // plan units²; 1 unit = 0.5 m
	Type string  `json:"type"`
}

type Floor struct {
	Rooms []Room `json:"rooms"`
}

type Sensor struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Type      string    `json:"type"`
	Value     string    `json:"value,omitempty"`
	DataType  string    `json:"data_type"`
	Unit      string    `json:"unit,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

type Actuator struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Type  string `json:"type"`
	State string `json:"state"`
}

type Equipment struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Type      string     `json:"type"`
	Category  string     `json:"category"`
	Level     string     `json:"level"`
	Room      string     `json:"room"`
	Status    string     `json:"status"`
	Sensors   []Sensor   `json:"sensors"`
	Actuators []Actuator `json:"actuators"`
}

// Floor reads a floor plan. It is large (rooms plus the walkable graph), so
// it gets more time than the other calls: under load, 2 s wasn't enough.
func (c *Client) Floor(ctx context.Context, level string) (Floor, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var f Floor
	err := c.do(ctx, http.MethodGet, "/api/building/floors/"+url.PathEscape(level), nil, &f)
	return f, err
}

// Equipment lists every unit on a level, with sensors and actuators inline.
func (c *Client) Equipment(ctx context.Context, level string) ([]Equipment, error) {
	var eq []Equipment
	err := c.do(ctx, http.MethodGet, "/api/equipment?level="+url.QueryEscape(level), nil, &eq)
	return eq, err
}

// BulkCreate registers equipment. BuildSim skips ids that already exist, so
// calling it again after a restart is safe.
func (c *Client) BulkCreate(ctx context.Context, eq []Equipment) error {
	return c.do(ctx, http.MethodPost, "/api/equipment/bulk", eq, nil)
}

// RoomLayer is one heatmap in the viewer: a value per room, mapped onto
// Minimum..Maximum and coloured by interpolating along Palette.
type RoomLayer struct {
	ID      string             `json:"id"`
	Label   string             `json:"label"`
	Unit    string             `json:"unit,omitempty"`
	Source  string             `json:"source,omitempty"`
	Minimum float64            `json:"minimum"`
	Maximum float64            `json:"maximum"`
	Opacity float64            `json:"opacity"`
	Palette []string           `json:"palette"`
	Values  map[string]float64 `json:"values"` // "<level>/<room>" → value
}

// Alert is a short, current message in the viewer. Severity is info, warning
// or critical.
type Alert struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Message  string `json:"message,omitempty"`
	Level    string `json:"level,omitempty"`
	Room     string `json:"room,omitempty"`
}

// PutRoomLayers replaces every room layer in the viewer. BuildSim keeps one
// collection, so only one service may call this.
func (c *Client) PutRoomLayers(ctx context.Context, layers []RoomLayer) error {
	if layers == nil {
		layers = []RoomLayer{}
	}
	return c.do(ctx, http.MethodPut, "/api/room-layers", layers, nil)
}

// PutAlerts replaces every alert in the viewer (at most 100).
func (c *Client) PutAlerts(ctx context.Context, alerts []Alert) error {
	if alerts == nil {
		alerts = []Alert{}
	}
	return c.do(ctx, http.MethodPut, "/api/alerts", alerts, nil)
}

// DeleteEquipment removes a unit and its sensors and actuators.
func (c *Client) DeleteEquipment(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/equipment/"+url.PathEscape(id), nil, nil)
}

// SensorValue reads one sensor.
func (c *Client) SensorValue(ctx context.Context, sensorID string) (Sensor, error) {
	var s Sensor
	err := c.do(ctx, http.MethodGet, "/api/sensors/"+url.PathEscape(sensorID), nil, &s)
	return s, err
}

func (c *Client) SetSensorValue(ctx context.Context, sensorID, value string) error {
	body := map[string]string{"data_type": "text", "value": value}
	return c.do(ctx, http.MethodPut, "/api/sensors/"+url.PathEscape(sensorID)+"/value", body, nil)
}

func (c *Client) SetActuatorState(ctx context.Context, actuatorID, state string) error {
	body := map[string]string{"state": state}
	return c.do(ctx, http.MethodPut, "/api/actuators/"+url.PathEscape(actuatorID)+"/state", body, nil)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
