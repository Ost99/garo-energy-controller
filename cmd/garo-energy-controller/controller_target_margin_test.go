package main

import (
	"math"
	"testing"
	"time"
)

// 2026-10-23 00:57:27: an on-pace hour hard-stopped because regulation aimed
// at the absolute limit. Regulation must aim below the limit, while the
// hard-stop budget (RemainingEnergyKWh) stays tied to the absolute limit.
func TestRegulationTargetsBelowAbsoluteLimit(t *testing.T) {
	var cfg Config
	cfg.HourlyLimitKWh = 9.5
	cfg.TargetMarginWh = 75
	cfg.ControlTuning.PacingHorizonSeconds = 600
	cfg.ControlTuning.MaxPacingAdjustmentW = 2000

	ref := time.Date(2026, 10, 23, 0, 57, 23, 0, time.Local) // 157 s left
	var s ControllerSnapshot
	populateEnergyDiagnostics(&s, cfg, "tibber", 9.079, 9864, ref, true)

	if math.Abs(s.RemainingEnergyKWh-0.421) > 1e-9 {
		t.Fatalf("hard-stop budget must use the absolute limit, got %.4f kWh", s.RemainingEnergyKWh)
	}
	if math.Abs(s.RemainingTargetEnergyKWh-0.346) > 1e-9 {
		t.Fatalf("regulation budget must use the target, got %.4f kWh", s.RemainingTargetEnergyKWh)
	}
	if math.Abs(s.BaseTargetPowerW-9425) > 1e-6 {
		t.Fatalf("base target %.3f W, want 9425 W", s.BaseTargetPowerW)
	}
	if s.PowerHeadroomW >= -1000 {
		t.Fatalf("expected strong reduction signal, headroom %.0f W", s.PowerHeadroomW)
	}
}

func TestZeroTargetMarginKeepsPreviousBehaviour(t *testing.T) {
	var cfg Config
	cfg.HourlyLimitKWh = 9.5
	cfg.ControlTuning.PacingHorizonSeconds = 600
	cfg.ControlTuning.MaxPacingAdjustmentW = 2000

	ref := time.Date(2026, 10, 23, 0, 57, 23, 0, time.Local)
	var s ControllerSnapshot
	populateEnergyDiagnostics(&s, cfg, "tibber", 9.079, 9864, ref, true)

	wantCeilingW := 0.421 * 3600000.0 / 157
	if math.Abs(s.HardBudgetCeilingW-wantCeilingW) > 1e-6 {
		t.Fatalf("ceiling %.3f W, want %.3f W", s.HardBudgetCeilingW, wantCeilingW)
	}
}
