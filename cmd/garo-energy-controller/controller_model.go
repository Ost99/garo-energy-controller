package main

import (
	"math"
	"time"
)

// Model-based DLM control. Opt-in (control_strategy "model") until tested
// on the real installation.
//
// Plant model, from the 2026-10-10 logs:
//
//	car current per phase ≈ DLM × (1 − k) − worst-phase household current
//
// clamped to [6 A, pilot ceiling], where k is GARO's reserve fraction and
// household current is CENTRAL100 − CENTRAL101 per phase.
//
// Unverified assumptions (check these first if it misbehaves):
//   - CENTRAL100 L1/L2/L3 line up with CENTRAL101 L1/L2/L3.
//   - The reserve is proportional to DLM (observed smaller at low DLM; the
//     exact shape is unknown). k ≈ 0.11 was fitted to one sample at 22 A.
//   - A one-phase car is limited by the worst household phase, not only its
//     own phase. Not observed; no data for one-phase charging.
//
// Structure (cascade):
//   - Outer loop (unchanged): hourly energy pacing produces the power
//     setpoint (EffectiveTargetPowerW), i.e. integral action on energy.
//   - Inner loop (this file): feed-forward from averaged household power
//     gives the car current the setpoint allows, a PI term on averaged grid
//     power corrects model error, and the plant model is inverted to the
//     DLM value that GARO turns into that car current. One write per change.
//
// No derivative term: inputs are 30 s samples of a signal that cycles with
// household loads, and a D term would amplify exactly those swings. The
// household feed-forward provides the anticipation a D term would add.
//
// A household load cycling faster than the averaging window moves the
// average DLM slightly instead of on every cycle. GARO still swings the car
// with the load on its own; no single DLM value can prevent that.

const (
	// modelAverageSeconds is the EMA time constant for all inputs.
	modelAverageSeconds = 180.0

	// modelHysteresisA: DLM is only written when the desired value is at
	// least this far from the current one. Fewer writes to GARO's SD card,
	// and no dithering between adjacent amps.
	modelHysteresisA = 1.0

	// modelCarFollowBandA: the car counts as held below the model when its
	// highest phase current is more than this below the car current the
	// model predicts for the current DLM.
	modelCarFollowBandA = 1.5

	// modelIntegralLimitA bounds the integral term.
	modelIntegralLimitA = 6.0

	// modelResetGap: a gap between samples longer than this (manual hold,
	// GARO outage) restarts the averages from the next sample.
	modelResetGap = 5 * time.Minute
)

type dlmModel struct {
	initialised bool
	last        time.Time

	gridAvgW           float64
	carAvgW            float64
	worstHouseholdAvgA float64

	integralA float64
}

type modelInput struct {
	Now time.Time

	GridW      float64
	SitePhaseA [3]float64 // CENTRAL100
	CarPhaseA  [3]float64 // CENTRAL101

	TargetW     float64 // EffectiveTargetPowerW
	CurrentDLMA int
	OnePhase    bool

	// CanIncrease is false when increases would have no effect (active
	// pilots saturated).
	CanIncrease bool

	MinA int
	MaxA int
}

type modelOutput struct {
	GridAvgW           float64
	WorstHouseholdAvgA float64

	FeedForwardA  float64
	ProportionalA float64
	IntegralA     float64
	CarSetpointA  float64

	DesiredDLMA float64 // continuous, before hysteresis
	TargetDLMA  int     // after hysteresis and limits
}

func (m *dlmModel) reset() { *m = dlmModel{} }

func (m *dlmModel) step(in modelInput, t ControlTuningConfig) modelOutput {
	carW := 0.0
	maxCarA := 0.0
	worstHouseholdA := 0.0
	for i := 0; i < 3; i++ {
		carW += in.CarPhaseA[i] * t.OnePhaseWattsPerAmp
		maxCarA = math.Max(maxCarA, in.CarPhaseA[i])
		if h := in.SitePhaseA[i] - in.CarPhaseA[i]; h > worstHouseholdA {
			worstHouseholdA = h
		}
	}

	dt := 0.0
	if m.initialised {
		dt = in.Now.Sub(m.last).Seconds()
		if dt <= 0 || dt > modelResetGap.Seconds() {
			m.reset()
		}
	}

	if !m.initialised {
		m.initialised = true
		m.gridAvgW = in.GridW
		m.carAvgW = carW
		m.worstHouseholdAvgA = worstHouseholdA
		dt = 0
	} else {
		alpha := 1 - math.Exp(-dt/modelAverageSeconds)
		m.gridAvgW += alpha * (in.GridW - m.gridAvgW)
		m.carAvgW += alpha * (carW - m.carAvgW)
		m.worstHouseholdAvgA += alpha * (worstHouseholdA - m.worstHouseholdAvgA)
	}
	m.last = in.Now

	wattsPerAmp := t.MultiPhaseWattsPerAmp
	if in.OnePhase {
		wattsPerAmp = t.OnePhaseWattsPerAmp
	}

	householdAvgW := m.gridAvgW - m.carAvgW
	feedForwardA := (in.TargetW - householdAvgW) / wattsPerAmp
	errorA := (in.TargetW - m.gridAvgW) / wattsPerAmp
	proportionalA := t.ModelKp * errorA

	toDLM := func(carA float64) float64 {
		return (carA + m.worstHouseholdAvgA) / (1 - t.ModelReserveFraction)
	}

	minA := float64(in.MinA)
	maxA := float64(in.MaxA)

	// Conditional integration (anti-windup): only integrate when the
	// resulting output is not pinned against a limit in the direction of
	// the error.
	carSetpointA := feedForwardA + proportionalA + m.integralA
	desired := toDLM(math.Max(carSetpointA, minA))
	pinnedHigh := desired >= maxA ||
		(!in.CanIncrease && desired >= float64(in.CurrentDLMA))
	pinnedLow := carSetpointA <= minA || desired <= minA

	// GARO cuts the car below what the model expects from the current DLM
	// whenever a household load cycles on the worst phase (immediately, down
	// to the 6 A minimum), and only ramps it back slowly. Grid power is then
	// below target, but that shortfall is not model error a higher DLM would
	// fix: integrating it winds up, and the wound-up DLM lets the car surge
	// each time the load switches off. Only integrate a shortfall while the
	// car is drawing what the model predicts for the current DLM.
	predictedCarA := float64(in.CurrentDLMA)*(1-t.ModelReserveFraction) -
		m.worstHouseholdAvgA
	carHeldBelowModel := maxCarA < predictedCarA-modelCarFollowBandA

	if dt > 0 &&
		!(errorA > 0 && (pinnedHigh || carHeldBelowModel)) &&
		!(errorA < 0 && pinnedLow) {
		m.integralA += t.ModelKp * errorA * dt / float64(t.ModelIntegralSeconds)
		m.integralA = math.Max(-modelIntegralLimitA,
			math.Min(modelIntegralLimitA, m.integralA))
		carSetpointA = feedForwardA + proportionalA + m.integralA
	}

	desired = toDLM(math.Max(carSetpointA, minA))
	desired = math.Max(minA, math.Min(maxA, desired))

	current := float64(in.CurrentDLMA)
	target := in.CurrentDLMA
	if desired >= current+modelHysteresisA ||
		desired <= current-modelHysteresisA {
		target = int(math.Round(desired))
	}
	if !in.CanIncrease && target > in.CurrentDLMA {
		target = in.CurrentDLMA
	}
	target = clampInt(target, in.MinA, in.MaxA)

	return modelOutput{
		GridAvgW:           m.gridAvgW,
		WorstHouseholdAvgA: m.worstHouseholdAvgA,
		FeedForwardA:       feedForwardA,
		ProportionalA:      proportionalA,
		IntegralA:          m.integralA,
		CarSetpointA:       carSetpointA,
		DesiredDLMA:        desired,
		TargetDLMA:         target,
	}
}
