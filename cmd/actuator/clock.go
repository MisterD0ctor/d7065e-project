package main

import "time"

// TimeLost is how long the building's time signal may be missing before the
// device stops waiting for it. The clock device repeats the time every real
// second even when it stands still, so five missed beats mean the clock or
// the broker is gone, not that the model clock is paused.
const TimeLost = 5 * time.Second

// timekeeper is the device's own clock. The building's time signal sets it
// (IF-1). Without the signal it keeps running at the last known speed, the
// way a device's clock does between synchronisations.
//
// This matters for safety, not for accuracy: a command's TTL is counted in
// model time (D-6). If the device's time stopped when the signal did, a lost
// broker would freeze every damper at its last command for good. Running on,
// the commands expire and the device goes to its safe default (NFR-6).
type timekeeper struct {
	now    time.Time // model time at the last signal
	factor float64   // model seconds per real second, from the signal
	heard  time.Time // real time of the last signal
}

func (k *timekeeper) set(model time.Time, factor float64, real time.Time) {
	k.now, k.heard = model, real
	if factor > 0 {
		k.factor = factor
	}
}

// at returns the model time at a real time. known is false before the first
// signal. lost reports that the signal has been missing for TimeLost and the
// time is the device's own estimate.
func (k *timekeeper) at(real time.Time) (model time.Time, known, lost bool) {
	if k.now.IsZero() {
		return time.Time{}, false, false
	}
	silent := real.Sub(k.heard)
	if silent <= TimeLost || k.factor <= 0 {
		return k.now, true, false
	}
	return k.now.Add(time.Duration(float64(silent) * k.factor)), true, true
}
