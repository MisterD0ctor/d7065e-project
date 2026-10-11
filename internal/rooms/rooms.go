// Package rooms parses room keys and names the device kinds with their
// BuildSim equipment types (IF-3, IF-9).
package rooms

import (
	"fmt"
	"strings"
)

// Key identifies a room as "<level>/<room>". Room names repeat between
// floors, so the level is always part of it.
type Key struct {
	Level string
	Name  string
}

func (k Key) String() string { return k.Level + "/" + k.Name }

func Parse(s string) (Key, error) {
	level, name, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok || level == "" || name == "" {
		return Key{}, fmt.Errorf("room %q: want <level>/<room>", s)
	}
	return Key{Level: level, Name: name}, nil
}

// ParseList parses a comma-separated list, e.g. the ROOMS environment variable.
func ParseList(s string) ([]Key, error) {
	var keys []Key
	for _, part := range strings.Split(s, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		k, err := Parse(part)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no rooms given")
	}
	return keys, nil
}

// Sensor kinds (IF-3). A clock is a sensor too: what it reads is the time.
const (
	CO2       = "co2"
	Temp      = "temp"
	Occupancy = "occupancy"
	Clock     = "clock"
)

// Actuator kinds (IF-9).
const (
	Damper  = "damper"
	Heating = "heating"
)

type SensorSpec struct {
	EquipmentType string // drives the icon in the 3D viewer
	SensorType    string
	Unit          string
}

var Sensors = map[string]SensorSpec{
	CO2:       {"co2_sensor", "co2", "ppm"},
	Temp:      {"temperature_sensor", "temperature", "°C"},
	Occupancy: {"occupancy_counter", "occupancy", "persons"},
	Clock:     {"bms_controller", "model_time", ""},
}

// SensorKind finds the sensor kind behind a BuildSim equipment type. The
// physical model uses it to know what each piece of equipment measures.
func SensorKind(equipmentType string) (string, bool) {
	for kind, spec := range Sensors {
		if spec.EquipmentType == equipmentType {
			return kind, true
		}
	}
	return "", false
}

type ActuatorSpec struct {
	EquipmentType string
	ActuatorType  string
}

var Actuators = map[string]ActuatorSpec{
	Damper:  {"ventilation_fan", "airflow"},
	Heating: {"radiator", "setpoint"},
}
