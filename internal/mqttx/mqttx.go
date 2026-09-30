// Package mqttx wraps the Paho client with the settings every service uses:
// QoS 1, no retained messages, automatic reconnect with the subscriptions
// restored.
package mqttx

import (
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/MisterD0ctor/d7065e-project/internal/env"
)

const qos = 1

type Client struct {
	c    mqtt.Client
	up   atomic.Bool
	mu   sync.Mutex
	subs map[string]mqtt.MessageHandler
}

// Connect reads MQTT_URL, MQTT_USER and MQTT_PASS and connects in the
// background: a broker that is down at start doesn't stop the service.
func Connect(clientID string) *Client {
	cl := &Client{subs: map[string]mqtt.MessageHandler{}}
	opts := mqtt.NewClientOptions().
		AddBroker(env.String("MQTT_URL", "tcp://mosquitto:1883")).
		SetClientID(clientID).
		SetUsername(env.String("MQTT_USER", "")).
		SetPassword(env.String("MQTT_PASS", "")).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetMaxReconnectInterval(5 * time.Second).
		SetCleanSession(true).
		SetOnConnectHandler(func(c mqtt.Client) {
			cl.up.Store(true)
			log.Printf("mqtt: connected")
			cl.mu.Lock()
			defer cl.mu.Unlock()
			for filter, h := range cl.subs {
				if t := c.Subscribe(filter, qos, h); t.Wait() && t.Error() != nil {
					log.Printf("mqtt: subscribe %s: %v", filter, t.Error())
				}
			}
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			cl.up.Store(false)
			log.Printf("mqtt: connection lost: %v", err)
		})
	cl.c = mqtt.NewClient(opts)
	cl.c.Connect() // retries in the background
	return cl
}

// Up reports whether the broker connection is currently established.
func (cl *Client) Up() bool { return cl.up.Load() }

// Publish sends v as JSON. It doesn't wait for the broker: a lost message is
// a gap in the record, not a reason to stall the control loop.
func (cl *Client) Publish(topic string, v any) { cl.publish(topic, v, false) }

// PublishRetained is for state a new subscriber needs at once, like the
// model time: the broker hands the last value to anyone who subscribes.
func (cl *Client) PublishRetained(topic string, v any) { cl.publish(topic, v, true) }

func (cl *Client) publish(topic string, v any, retained bool) {
	if !cl.Up() {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("mqtt: marshal for %s: %v", topic, err)
		return
	}
	cl.c.Publish(topic, qos, retained, b)
}

// Subscribe registers a handler; it is re-subscribed after every reconnect.
func (cl *Client) Subscribe(filter string, handle func(topic string, payload []byte)) {
	h := func(_ mqtt.Client, m mqtt.Message) { handle(m.Topic(), m.Payload()) }
	cl.mu.Lock()
	cl.subs[filter] = h
	cl.mu.Unlock()
	if cl.Up() {
		if t := cl.c.Subscribe(filter, qos, h); t.Wait() && t.Error() != nil {
			log.Printf("mqtt: subscribe %s: %v", filter, t.Error())
		}
	}
}

// Dedup remembers (key, seq) pairs to drop QoS 1 redeliveries.
type Dedup struct {
	mu   sync.Mutex
	last map[string]int64
}

func NewDedup() *Dedup { return &Dedup{last: map[string]int64{}} }

// Fresh reports whether seq is newer than anything seen for key, and records
// it. Readings from one sensor arrive in order, so the highest seq is enough.
func (d *Dedup) Fresh(key string, seq int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if seq <= d.last[key] {
		return false
	}
	d.last[key] = seq
	return true
}

// Silence detects that a stream which normally arrives at a steady rate has
// stopped. The threshold follows the observed arrival interval, because one
// model minute is 1 s real at factor 60 but 0.1 s at factor 600: a fixed
// threshold in real time would be wrong at one of them. It must stay well
// below the actuators' command TTL, or a broker outage expires commands
// before the fallback starts (found by test T-24).
type Silence struct {
	mu       sync.Mutex
	last     time.Time
	interval time.Duration // smoothed real time between arrivals
}

const (
	silenceFactor = 2.5
	minSilence    = 1500 * time.Millisecond
)

// Arrived records one message at real time now.
func (s *Silence) Arrived(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.last.IsZero() {
		gap := now.Sub(s.last)
		if s.interval == 0 {
			s.interval = gap
		} else {
			s.interval = (4*s.interval + gap) / 5
		}
	}
	s.last = now
}

// Silent reports whether nothing has arrived for longer than expected.
func (s *Silence) Silent(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last.IsZero() {
		return true
	}
	limit := max(time.Duration(float64(s.interval)*silenceFactor), minSilence)
	return now.Sub(s.last) > limit
}
