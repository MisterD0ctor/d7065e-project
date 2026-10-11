// Package msg defines the MQTT messages of IF-4 to IF-7, and the record the
// physical model leaves in BuildSim for the evaluation (IF-5). Every message
// carries the run id and the model time it describes.
package msg

import (
	"fmt"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

// Observation is one sensor reading (IF-4), on obs/<level>/<room>/<kind>.
type Observation struct {
	RunID     string  `json:"run_id"`
	SensorID  string  `json:"sensor_id"`
	Room      string  `json:"room"`
	Kind      string  `json:"kind"`
	Value     float64 `json:"value"`
	Unit      string  `json:"unit"`
	ModelTime string  `json:"model_time"`
	// Seq is the reading's model time in Unix seconds: unique per sensor,
	// increasing, and unchanged across a gateway restart, so consumers can
	// deduplicate on (sensor_id, seq).
	Seq int64 `json:"seq"`
}

// Truth is one room's true state (IF-5). The physical model writes it into
// BuildSim (Record); the truth recorder copies it to truth/<level>/<room>,
// which only storage may read, and fills in the run id.
type Truth struct {
	RunID     string  `json:"run_id"`
	Room      string  `json:"room"`
	ModelTime string  `json:"model_time"`
	CO2       float64 `json:"co2_ppm"`
	Temp      float64 `json:"temp_c"`
	Occupancy int     `json:"occupancy"` // everyone in the room, guards included
	Guards    int     `json:"guards"`    // of whom night guards on patrol
	Airflow   float64 `json:"airflow_ls"`
	Setpoint  float64 `json:"setpoint_c"`
	// Power in the step that ended at ModelTime (W), for the energy proxy.
	FanW      float64 `json:"fan_w"`
	AHUHeatW  float64 `json:"ahu_heat_w"`
	RadiatorW float64 `json:"radiator_w"`
}

// Decision is the controller's record for one room and period (IF-6), on
// decision/<level>/<room>.
type Decision struct {
	RunID              string             `json:"run_id"`
	Room               string             `json:"room"`
	ModelTime          string             `json:"model_time"`
	Policy             string             `json:"policy"`
	Mode               string             `json:"mode"`
	Fault              string             `json:"fault,omitempty"` // what is wrong with the sensor behind a degraded mode
	Observed           map[string]float64 `json:"observed"`
	PredictedOccupancy *float64           `json:"predicted_occupancy,omitempty"`
	LeadMin            float64            `json:"lead_min,omitempty"`
	AirflowTarget      *float64           `json:"airflow_target_ls,omitempty"`
	SetpointTarget     *float64           `json:"setpoint_target_c,omitempty"`
	CmdID              string             `json:"cmd_id"`
	Status             int                `json:"status"`
	AppliedTarget      float64            `json:"applied_target"`
	Clamped            bool               `json:"clamped"`
	Reason             string             `json:"reason"`
}

// Event is something an actuator did on its own (IF-7), on
// event/<level>/<room>.
type Event struct {
	RunID     string `json:"run_id"`
	Room      string `json:"room"`
	ModelTime string `json:"model_time"`
	Actuator  string `json:"actuator"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail"`
}

// Record is what the physical model keeps in BuildSim besides the sensor
// values: the full state of every simulated room at each of the last few
// model minutes, as the value of the text sensor RecordSensor. It is the
// evaluation's view of the building. The control system never reads it: it
// doesn't talk to BuildSim at all.
type Record struct {
	Minutes []RecordMinute `json:"minutes"` // oldest first
}

type RecordMinute struct {
	ModelTime string  `json:"model_time"`
	Rooms     []Truth `json:"rooms"`
}

// The BuildSim equipment the physical model registers for its record.
const (
	RecordEquipment = "physics-state"
	RecordSensor    = "physics-state-record"
	RecordMinutes   = 5 // minutes of history, so a reader that polls misses none
)

// ModelTime is the building's time (IF-1): the model minute the physical
// model has last written to BuildSim, and how fast the model clock runs. The
// physical model writes it as the value of every clock device's sensor, after
// all the other values of that minute; the clock device publishes it on
// TimeTopic, on every change and once a real second otherwise, the way a
// building network distributes time. A device that sees a new minute there
// finds that minute's values in BuildSim.
type ModelTime struct {
	ModelTime string  `json:"model_time"`
	Factor    float64 `json:"factor"`
}

const TimeTopic = "time/model"

// State is what an actuator has reached (IF-9), on
// state/<level>/<room>/<kind>: on every change, and once a model minute. The
// actuator also writes it to BuildSim, where the physical model reads it;
// this copy is for the dashboard, which doesn't read BuildSim.
type State struct {
	RunID     string  `json:"run_id"`
	Room      string  `json:"room"`
	ModelTime string  `json:"model_time"`
	Actuator  string  `json:"actuator"` // damper or heating
	Value     float64 `json:"value"`
	Unit      string  `json:"unit"`
}

func ObsTopic(k rooms.Key, kind string) string {
	return fmt.Sprintf("obs/%s/%s/%s", k.Level, k.Name, kind)
}
func TruthTopic(k rooms.Key) string    { return fmt.Sprintf("truth/%s/%s", k.Level, k.Name) }
func DecisionTopic(k rooms.Key) string { return fmt.Sprintf("decision/%s/%s", k.Level, k.Name) }
func EventTopic(k rooms.Key) string    { return fmt.Sprintf("event/%s/%s", k.Level, k.Name) }
func StateTopic(k rooms.Key, kind string) string {
	return fmt.Sprintf("state/%s/%s/%s", k.Level, k.Name, kind)
}

// FormatTime is the model-time format on every interface.
func FormatTime(t time.Time) string { return t.Format(time.RFC3339) }
