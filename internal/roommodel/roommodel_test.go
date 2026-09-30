package roommodel

import (
	"math"
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

// T-01: a room filled at t=0 with constant flow reaches 63 % of its CO₂ rise
// after one time constant τ = V/q.
func TestCO2StepResponse(t *testing.T) {
	size := sizing.For(80)
	r := New(size)
	in := Inputs{Occupancy: 30, AirflowLs: size.Design, Setpoint: 21}

	q := size.Design / 1000
	tau := time.Duration(size.VolumeM3 / q * float64(time.Second))
	steady := sizing.OutdoorCO2 + 30*sizing.CO2PerPerson*1e6/q

	r.Step(tau, in)
	got := (r.CO2 - sizing.OutdoorCO2) / (steady - sizing.OutdoorCO2)
	if want := 1 - math.Exp(-1); math.Abs(got-want)/want > 0.05 {
		t.Fatalf("after τ=%v: %.3f of the rise, want %.3f ±5%%", tau, got, want)
	}
}

// T-02: a 3-hour clock jump in one call gives the same result as many small
// steps, and stays finite.
func TestClockJumpSubsteps(t *testing.T) {
	size := sizing.For(80)
	in := Inputs{Occupancy: 40, AirflowLs: size.Occupied, Setpoint: 21}

	big := New(size)
	big.Step(3*time.Hour, in)

	small := New(size)
	for range 180 {
		small.Step(time.Minute, in)
	}

	for _, v := range []float64{big.CO2, big.Temp} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("non-finite state: %+v", big)
		}
	}
	if math.Abs(big.CO2-small.CO2) > 1e-6 || math.Abs(big.Temp-small.Temp) > 1e-6 {
		t.Fatalf("jump %+v differs from small steps %+v", big, small)
	}
}

// More airflow must lower CO₂: the loop can only close if it does.
func TestAirflowLowersCO2(t *testing.T) {
	size := sizing.For(80)
	low, high := New(size), New(size)
	low.Step(time.Hour, Inputs{Occupancy: 30, AirflowLs: size.Occupied, Setpoint: 21})
	high.Step(time.Hour, Inputs{Occupancy: 30, AirflowLs: size.Max, Setpoint: 21})
	if high.CO2 >= low.CO2 {
		t.Fatalf("max flow gave %.0f ppm, floor flow %.0f ppm", high.CO2, low.CO2)
	}
}

// An empty room with the heating on settles close to its setpoint.
func TestHeatingHoldsSetpoint(t *testing.T) {
	size := sizing.For(80)
	r := New(size)
	r.Temp = 15
	r.Step(48*time.Hour, Inputs{AirflowLs: size.Empty, Setpoint: 21})
	if math.Abs(r.Temp-21) > 0.5 {
		t.Fatalf("settled at %.2f °C, want 21 ±0.5", r.Temp)
	}
}

// The energy proxy rises with airflow: more ventilation, more fan power and
// more heating of outdoor air.
func TestEnergyFollowsAirflow(t *testing.T) {
	size := sizing.For(80)
	low, high := New(size), New(size)
	low.Step(time.Hour, Inputs{AirflowLs: size.Empty, Setpoint: 21})
	high.Step(time.Hour, Inputs{AirflowLs: size.Max, Setpoint: 21})
	if high.FanW <= low.FanW || high.AHUHeatW <= low.AHUHeatW {
		t.Fatalf("max flow: fan %.0f W, AHU %.0f W; min flow: fan %.0f W, AHU %.0f W",
			high.FanW, high.AHUHeatW, low.FanW, low.AHUHeatW)
	}
	if low.RadiatorW <= 0 {
		t.Error("an 8 °C day at 21 °C should need some radiator power")
	}
}
