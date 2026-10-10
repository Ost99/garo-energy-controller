package main

import (
	"testing"
	"time"
)

// simulateSteadyHour feeds a constant grid power through the pacing
// diagnostics for a whole hour and returns the largest headroom seen.
func simulateSteadyHour(t *testing.T, cfg Config, gridW float64, fromSecond int) (maxHeadroomW float64, atSecond int) {
	t.Helper()
	start := time.Date(2026, 10, 9, 23, 0, 0, 0, time.Local)
	maxHeadroomW = -1e9
	for sec := fromSecond; sec < 3600; sec += 30 {
		hourEnergy := gridW / 1000.0 * float64(sec-fromSecond) / 3600.0
		var s ControllerSnapshot
		populateEnergyDiagnostics(&s, cfg, "test", hourEnergy, gridW,
			start.Add(time.Duration(sec)*time.Second), true)
		if s.PowerHeadroomW > maxHeadroomW {
			maxHeadroomW, atSecond = s.PowerHeadroomW, sec
		}
	}
	return maxHeadroomW, atSecond
}

func TestSteadyStateBelowTargetDoesNotRampLate(t *testing.T) {
	cfg := defaultConfig()
	gate := cfg.ControlTuning.MultiPhaseUpGateW
	// Steady states a little under 9.5 kW where +1 A (~720 W) would overshoot
	// the target. Accumulated deficit must not open the upward gate.
	for _, gridW := range []float64{9000, 9100, 9200, 9300, 9400} {
		h, at := simulateSteadyHour(t, cfg, gridW, 0)
		if h >= gate {
			t.Errorf("steady %.0f W: headroom %.0f W at %d s reaches up gate %.0f W",
				gridW, h, at, gate)
		}
	}
}

func TestCatchUpStillAllowedEarlyInHour(t *testing.T) {
	// Charging starts at minute 15 with an empty hour: the early catch-up
	// from commit 2997b0b must still open the upward gate.
	cfg := defaultConfig()
	var s ControllerSnapshot
	start := time.Date(2026, 10, 9, 23, 15, 0, 0, time.Local)
	populateEnergyDiagnostics(&s, cfg, "test", 0.3, 3000, start, true)
	// Raw correction is clamped to 2500 W, then faded by 45/60.
	if s.PacingCorrectionW < 1800 {
		t.Fatalf("early catch-up faded: correction %.0f W", s.PacingCorrectionW)
	}
}

func TestOverPaceCorrectionNotFaded(t *testing.T) {
	cfg := defaultConfig()
	var s ControllerSnapshot
	ref := time.Date(2026, 10, 9, 23, 55, 0, 0, time.Local)
	// 55 min in, expected 8.708 kWh; 9.0 kWh used is over pace.
	populateEnergyDiagnostics(&s, cfg, "test", 9.0, 9500, ref, true)
	want := (cfg.HourlyLimitKWh*3300/3600 - 9.0) * 3600000.0 /
		float64(cfg.ControlTuning.PacingHorizonSeconds)
	if s.PacingCorrectionW > want+1 || s.PacingCorrectionW < want-1 {
		t.Fatalf("over-pace correction %.0f W, want %.0f W", s.PacingCorrectionW, want)
	}
}
