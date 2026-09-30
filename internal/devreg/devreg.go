// Package devreg is the client side of the device registry (IF-12), used by
// devices at boot and by the controller to find the devices in each room.
package devreg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/registry"
)

var (
	ErrNotInstalled = errors.New("device is not installed")
	ErrRetired      = errors.New("device is retired")
)

type Client struct {
	base string
	http *http.Client
}

func New(baseURL string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 2 * time.Second}}
}

// CheckIn tells the registry this device is up, and where to reach it
// (actuators; sensors pass ""). It returns the device's own record.
func (c *Client) CheckIn(ctx context.Context, id, endpoint string) (registry.Device, error) {
	body, _ := json.Marshal(map[string]string{"endpoint": endpoint})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+"/devices/"+url.PathEscape(id)+"/checkin", bytes.NewReader(body))
	if err != nil {
		return registry.Device{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	var dev registry.Device
	err = c.do(req, &dev)
	return dev, err
}

// WaitForCommissioning checks in until the registry knows the device and it
// is active. Until an installer has registered it, a device does nothing:
// it doesn't know its room, so it has nothing to report.
func (c *Client) WaitForCommissioning(ctx context.Context, id, endpoint string) (registry.Device, error) {
	for {
		dev, err := c.CheckIn(ctx, id, endpoint)
		if err == nil {
			return dev, nil
		}
		switch {
		case errors.Is(err, ErrNotInstalled):
			log.Printf("device %s is not commissioned yet; waiting for an installer to register it", id)
		case errors.Is(err, ErrRetired):
			log.Printf("device %s is retired; staying idle", id)
		default:
			log.Printf("registry: %v (retrying)", err)
		}
		select {
		case <-ctx.Done():
			return registry.Device{}, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// List returns the devices matching the filter.
func (c *Client) List(ctx context.Context, f registry.Filter) ([]registry.Device, error) {
	q := url.Values{}
	if f.Kind != "" {
		q.Set("kind", f.Kind)
	}
	if f.Room != "" {
		q.Set("room", f.Room)
	}
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/devices?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var list []registry.Device
	err = c.do(req, &list)
	return list, err
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(resp.Body).Decode(out)
	case http.StatusNotFound:
		return ErrNotInstalled
	case http.StatusGone:
		return ErrRetired
	}
	var e struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&e)
	return fmt.Errorf("registry: %d %s", resp.StatusCode, e.Error)
}
