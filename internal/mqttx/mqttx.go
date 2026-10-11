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
//
// Nothing is ever retained on the broker: a retained message outlives the run
// that sent it, and a device that starts in the next run would take it for
// current. (A retained model time did exactly that to the actuators.) What a
// late subscriber needs at once is repeated by its publisher instead.
func (cl *Client) Publish(topic string, v any) {
	if !cl.Up() {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("mqtt: marshal for %s: %v", topic, err)
		return
	}
	cl.c.Publish(topic, qos, false, b)
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
