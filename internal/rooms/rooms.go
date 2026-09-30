// Package rooms parses room keys and derives the device ids of IF-3 and IF-9.
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

// slug is used inside device ids, which may not contain "/".
func (k Key) slug() string { return k.Level + "-" + k.Name }

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

// Sensor kinds (IF-3).
const (
	CO2       = "co2"
	Temp      = "temp"
	Occupancy = "occupancy"
)

// Actuator kinds (IF-9).
const (
	Damper  = "damper"
	Heating = "heating"
)

type SensorSpec struct {
	EquipmentPrefix string
	EquipmentType   string
	SensorSuffix    string
	SensorType      string
	Unit            string
}

var Sensors = map[string]SensorSpec{
	CO2:       {"co2", "co2_sensor", "co2", "co2", "ppm"},
	Temp:      {"temp", "temperature_sensor", "temp", "temperature", "°C"},
	Occupancy: {"occ", "occupancy_counter", "occ", "occupancy", "persons"},
}

type ActuatorSpec struct {
	EquipmentPrefix string
	EquipmentType   string
	ActuatorSuffix  string
	ActuatorType    string
}

var Actuators = map[string]ActuatorSpec{
	Damper:  {"vent", "ventilation_fan", "airflow", "airflow"},
	Heating: {"heat", "radiator", "setpoint", "setpoint"},
}

func EquipmentID(prefix string, k Key) string { return prefix + "-" + k.slug() }

func SensorID(kind string, k Key) string { return k.slug() + "-" + Sensors[kind].SensorSuffix }

func ActuatorID(kind string, k Key) string { return k.slug() + "-" + Actuators[kind].ActuatorSuffix }
