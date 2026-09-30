// Package msg defines the MQTT messages of IF-4 to IF-7. Every message
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

// Truth is one room's true state (IF-5), on truth/<level>/<room>.
type Truth struct {
	RunID     string  `json:"run_id"`
	Room      string  `json:"room"`
	ModelTime string  `json:"model_time"`
	CO2       float64 `json:"co2_ppm"`
	Temp      float64 `json:"temp_c"`
	Occupancy int     `json:"occupancy"`
	Airflow   float64 `json:"airflow_ls"`
	Setpoint  float64 `json:"setpoint_c"`
}

// Decision is the controller's record for one room and period (IF-6), on
// decision/<level>/<room>.
type Decision struct {
	RunID              string             `json:"run_id"`
	Room               string             `json:"room"`
	ModelTime          string             `json:"model_time"`
	Policy             string             `json:"policy"`
	Mode               string             `json:"mode"`
	Observed           map[string]float64 `json:"observed"`
	PredictedOccupancy *float64           `json:"predicted_occupancy,omitempty"`
	AirflowTarget      float64            `json:"airflow_target_ls"`
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

func ObsTopic(k rooms.Key, kind string) string {
	return fmt.Sprintf("obs/%s/%s/%s", k.Level, k.Name, kind)
}
func TruthTopic(k rooms.Key) string    { return fmt.Sprintf("truth/%s/%s", k.Level, k.Name) }
func DecisionTopic(k rooms.Key) string { return fmt.Sprintf("decision/%s/%s", k.Level, k.Name) }
func EventTopic(k rooms.Key) string    { return fmt.Sprintf("event/%s/%s", k.Level, k.Name) }

// FormatTime is the model-time format on every interface.
func FormatTime(t time.Time) string { return t.Format(time.RFC3339) }
