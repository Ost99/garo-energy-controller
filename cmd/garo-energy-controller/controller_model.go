package main

import (
	"math"
	"time"
)

// Model-based DLM control.
//
// Plant model (observed on GARO LB100):
//
//	car current per phase ≈ DLM × (1 − k) − worst-phase household current
//
// clamped to [6 A, pilot ceiling], where k is GARO's reserve fraction
// (≈ 0.11) and household current is CENTRAL100 − CENTRAL101 per phase.
//
// The controller is a cascade:
//
//   - Outer loop (unchanged): hourly energy pacing produces the power
//     setpoint (EffectiveTargetPowerW). This is the integral action on
//     energy.
//   - Inner loop (this file): feed-forward from averaged household power
//     gives the car current the setpoint allows, a PI term on averaged grid
//     power corrects model error, and the plant model is inverted to a DLM
//     value that GARO turns into that car current.
//
// There is no derivative term. The inputs are 30 s samples of a signal that
// cycles with household loads; a D term would amplify exactly the swings
// the averaging is there to ignore. The household feed-forward provides the
// anticipation a D term would otherwise be used for.
//
// All inputs are averaged (EMA). A household load that cycles faster than
// the averaging window shifts the average DLM a little instead of moving it
// on every cycle; GARO still swings the car with the load on its own, which
// no single DLM value can prevent.

const (
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

	// CanIncrease is false when increases are not allowed (final minutes
	// of the hour) or would have no effect (active pilots saturated).
	CanIncrease bool

	MinA int
	MaxA int
}

type modelOutput struct {
	GridAvgW           float64
	HouseholdAvgW      float64
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
	worstHouseholdA := 0.0
	for i := 0; i < 3; i++ {
		carW += in.CarPhaseA[i] * t.OnePhaseWattsPerAmp
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
		alpha := 1 - math.Exp(-dt/float64(t.ModelAverageSeconds))
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

	if dt > 0 && !(errorA > 0 && pinnedHigh) && !(errorA < 0 && pinnedLow) {
		m.integralA += t.ModelKp * errorA * dt / float64(t.ModelIntegralSeconds)
		limit := t.ModelIntegralLimitA
		m.integralA = math.Max(-limit, math.Min(limit, m.integralA))
		carSetpointA = feedForwardA + proportionalA + m.integralA
	}

	desired = toDLM(math.Max(carSetpointA, minA))
	desired = math.Max(minA, math.Min(maxA, desired))

	current := float64(in.CurrentDLMA)
	target := in.CurrentDLMA
	if desired >= current+t.ModelHysteresisA ||
		desired <= current-t.ModelHysteresisA {
		target = int(math.Round(desired))
	}
	if !in.CanIncrease && target > in.CurrentDLMA {
		target = in.CurrentDLMA
	}
	target = clampInt(target, in.MinA, in.MaxA)

	return modelOutput{
		GridAvgW:           m.gridAvgW,
		HouseholdAvgW:      householdAvgW,
		WorstHouseholdAvgA: m.worstHouseholdAvgA,
		FeedForwardA:       feedForwardA,
		ProportionalA:      proportionalA,
		IntegralA:          m.integralA,
		CarSetpointA:       carSetpointA,
		DesiredDLMA:        desired,
		TargetDLMA:         target,
	}
}
