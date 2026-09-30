// Package roommodel is the per-room physics (FR-1): a CO₂ mass balance and a
// heat balance, both first order. Step integrates over a model-time interval
// in sub-steps of at most MaxStep, holding the inputs constant inside each.
package roommodel

import (
	"math"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

const (
	MaxStep = time.Minute // D-1: dt ≤ 1 model-minute

	OutdoorTemp   = 8.0    // °C, Luleå in autumn
	SupplyTemp    = 18.0   // °C, supply air after heat recovery
	UAPerM2       = 1.0    // W/K per m² floor, envelope loss
	HeatCapPerM2  = 100e3  // J/K per m² floor, air plus furniture and inner walls
	BodyHeat      = 100.0  // W per seated person
	RadiatorPerM2 = 60.0   // W per m² floor, radiator maximum
	RadiatorBand  = 0.5    // K below setpoint for full radiator power
	AirHeatCap    = 1200.0 // J/(m³·K), ρ·cp of air

	// Energy proxy (NFR-3). Fan power per airflow is a typical specific fan
	// power (SFP 1.5 kW per m³/s); the air-handling unit heats outdoor air
	// to SupplyTemp after heat recovery.
	SpecificFanPower = 1500.0 // W per m³/s
)

// Inputs hold for the whole interval passed to Step.
type Inputs struct {
	Occupancy int
	AirflowLs float64 // l/s, the damper's reached state
	Setpoint  float64 // °C, the heating actuator's reached state
}

type Room struct {
	Size sizing.Room
	CO2  float64 // ppm
	Temp float64 // °C

	// Power over the last Step (W), for the energy proxy.
	FanW      float64
	AHUHeatW  float64 // heating outdoor air to the supply temperature
	RadiatorW float64
}

func New(size sizing.Room) *Room {
	return &Room{Size: size, CO2: sizing.OutdoorCO2, Temp: sizing.DefaultSetpoint}
}

// Step advances the room by d of model time.
func (r *Room) Step(d time.Duration, in Inputs) {
	q := max(in.AirflowLs, 0) / 1000 // m³/s
	r.FanW = SpecificFanPower * q
	r.AHUHeatW = AirHeatCap * q * max(SupplyTemp-OutdoorTemp, 0)
	var radiatorJ, total float64
	for d > 0 {
		dt := min(d, MaxStep)
		radiatorJ += r.radiator(in.Setpoint) * dt.Seconds()
		total += dt.Seconds()
		r.stepCO2(dt.Seconds(), in)
		r.stepTemp(dt.Seconds(), in)
		d -= dt
	}
	if total > 0 {
		r.RadiatorW = radiatorJ / total
	}
}

// stepCO2 uses the exact solution of V·dC/dt = n·G·1e6 + q·(C_out − C),
// which is stable for any dt.
func (r *Room) stepCO2(dt float64, in Inputs) {
	v := r.Size.VolumeM3
	q := max(in.AirflowLs, 0) / 1000 // m³/s
	gen := float64(max(in.Occupancy, 0)) * sizing.CO2PerPerson * 1e6
	if q == 0 {
		r.CO2 += gen / v * dt
		return
	}
	steady := sizing.OutdoorCO2 + gen/q
	r.CO2 = steady + (r.CO2-steady)*math.Exp(-q*dt/v)
}

// stepTemp solves C·dT/dt = UA·(T_out − T) + ρcp·q·(T_supply − T) + P_body + P_rad
// exactly over dt, with the radiator power held at its value at the start.
func (r *Room) stepTemp(dt float64, in Inputs) {
	area := r.Size.AreaM2
	ua := UAPerM2 * area
	vent := AirHeatCap * max(in.AirflowLs, 0) / 1000
	heat := BodyHeat*float64(max(in.Occupancy, 0)) + r.radiator(in.Setpoint)
	k := ua + vent // W/K
	steady := (ua*OutdoorTemp + vent*SupplyTemp + heat) / k
	r.Temp = steady + (r.Temp-steady)*math.Exp(-k*dt/(HeatCapPerM2*area))
}

// radiator is a proportional controller at the radiator valve: full power
// RadiatorBand below the setpoint, none at or above it.
func (r *Room) radiator(setpoint float64) float64 {
	pmax := RadiatorPerM2 * r.Size.AreaM2
	frac := (setpoint - r.Temp) / RadiatorBand
	return pmax * min(max(frac, 0), 1)
}
