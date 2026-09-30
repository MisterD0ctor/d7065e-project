// Package sizing holds the ventilation sizing and physical constants from
// docs/architecture-options.md, "Ventilation sizing".
package sizing

import "time"

const (
	MetresPerUnit = 0.5  // floor-plan units to metres (occupancysim model.md)
	CeilingHeight = 2.7  // m, assumed
	OutdoorCO2    = 420  // ppm
	CO2PerPerson  = 5e-6 // m³/s of CO₂ per seated adult (≈ 18 l/h)

	OfficeMaxM2  = 40 // occupancysim role thresholds
	LectureMinM2 = 60

	PerPersonLs     = 7.0  // l/s per person (AFS)
	PerAreaLs       = 0.35 // l/s·m² (AFS), also the occupied minimum
	UnoccupiedLsM2  = 0.10 // l/s·m² (BBR, nobody present)
	MaxOverDesign   = 1.5
	OverrideCO2     = 1100.0 // ppm (FR-6)
	DefaultSetpoint = 21.0   // °C, heating fallback
	MinSetpoint     = 16.0
	MaxSetpoint     = 24.0
)

// Room is one room's geometry and flow limits, all flows in l/s.
type Room struct {
	AreaM2   float64 `json:"area_m2"`
	VolumeM3 float64 `json:"volume_m3"`
	Capacity int     `json:"capacity"`
	Design   float64 `json:"design_ls"`
	Max      float64 `json:"max_ls"`
	Occupied float64 `json:"occupied_min_ls"` // floor during occupied hours
	Empty    float64 `json:"empty_min_ls"`    // floor outside them
}

// AreaM2 converts a floor-plan area (units²) to m².
func AreaM2(planArea float64) float64 { return planArea * MetresPerUnit * MetresPerUnit }

// Capacity estimates a room's design capacity from its area, following
// occupancysim's roles: an office holds one person, a lecture room 2 m² per
// seat. It is the fallback when occupancysim's own capacities are
// unavailable; it can't tell a fika room (4 m² per person) from a big lecture
// room, and sizes it as a lecture room, which is the safe side. Rooms between
// the office and lecture sizes get one person per 10 m².
func Capacity(areaM2 float64) int {
	switch {
	case areaM2 <= OfficeMaxM2:
		return 1
	case areaM2 >= LectureMinM2:
		return max(4, int(areaM2/2))
	default:
		return max(1, int(areaM2/10))
	}
}

// For sizes a room from its area alone, estimating the capacity.
func For(areaM2 float64) Room { return ForCapacity(areaM2, Capacity(areaM2)) }

// ForCapacity sizes a room whose design capacity is known.
func ForCapacity(areaM2 float64, people int) Room {
	design := PerPersonLs*float64(people) + PerAreaLs*areaM2
	return Room{
		AreaM2:   areaM2,
		VolumeM3: areaM2 * CeilingHeight,
		Capacity: people,
		Design:   design,
		Max:      MaxOverDesign * design,
		Occupied: PerAreaLs * areaM2,
		Empty:    UnoccupiedLsM2 * areaM2,
	}
}

// OccupiedHours reports whether t is inside the occupied schedule used for
// the flow floor: weekdays 07:00–18:00.
func OccupiedHours(t time.Time) bool {
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	h := t.Hour()
	return h >= 7 && h < 18
}

// Floor is the lowest airflow the actuator allows at model time t.
func (r Room) Floor(t time.Time) float64 {
	if OccupiedHours(t) {
		return r.Occupied
	}
	return r.Empty
}
