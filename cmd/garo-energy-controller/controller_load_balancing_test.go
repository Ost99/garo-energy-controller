package main

import (
	"strings"
	"testing"
)

func TestChargerLoadBalancingDiagnosticsAllBalanced(t *testing.T) {
	valid, all, errText := chargerLoadBalancingDiagnostics([]GaroPilotLevel{
		{SerialNumber: 100001, LoadBalanced: true, LoadBalancedKnown: true},
		{SerialNumber: 100002, LoadBalanced: true, LoadBalancedKnown: true},
	}, true, false)
	if !valid || !all || errText != "" {
		t.Fatalf("got valid=%v all=%v error=%q", valid, all, errText)
	}
}

func TestChargerLoadBalancingDiagnosticsDetectsDisabledMaster(t *testing.T) {
	valid, all, errText := chargerLoadBalancingDiagnostics([]GaroPilotLevel{
		{SerialNumber: 100001, LoadBalanced: false, LoadBalancedKnown: true},
		{SerialNumber: 100002, LoadBalanced: true, LoadBalancedKnown: true},
	}, true, false)
	if !valid || all {
		t.Fatalf("got valid=%v all=%v, want valid=true all=false", valid, all)
	}
	if !strings.Contains(errText, "100001") || !strings.Contains(errText, "not load balanced") {
		t.Fatalf("unexpected error %q", errText)
	}
}

func TestChargerLoadBalancingDiagnosticsUnknownIsNotFalse(t *testing.T) {
	valid, all, errText := chargerLoadBalancingDiagnostics([]GaroPilotLevel{
		{SerialNumber: 100001, LoadBalancedKnown: false},
		{SerialNumber: 100002, LoadBalanced: true, LoadBalancedKnown: true},
	}, true, false)
	if valid || all || errText != "" {
		t.Fatalf("got valid=%v all=%v error=%q, want unknown without false config error", valid, all, errText)
	}
}

func TestHardStopRequirementIndependentOfLoadBalancing(t *testing.T) {
	s := ControllerSnapshot{
		Charging:                  true,
		EnergyValid:               true,
		RemainingEnergyKWh:        0.20,
		GridPowerW:                7200,
		SecondsRemaining:          600,
		ChargerLoadBalancingValid: true,
		AllChargersLoadBalanced:   false,
	}
	if !hardStopRequired(s) {
		t.Fatal("hard stop should remain armed when charger load balancing is disabled")
	}
}

func TestHardStopNotRequiredWhenHourEndsFirst(t *testing.T) {
	// Observed 2026-10-05 23:59: ~0.4 kWh left at ~10 kW is ~140 s to the
	// limit, but only ~70 s of the hour remained. The budget resets first.
	s := ControllerSnapshot{
		Charging:           true,
		EnergyValid:        true,
		RemainingEnergyKWh: 0.40,
		GridPowerW:         10100,
		SecondsRemaining:   70,
	}
	if hardStopRequired(s) {
		t.Fatal("hard stop issued for a breach that would only occur after the hour ends")
	}
}

func TestHardStopRequiredWhenLimitBeforeHourEnd(t *testing.T) {
	s := ControllerSnapshot{
		Charging:           true,
		EnergyValid:        true,
		RemainingEnergyKWh: 0.40,
		GridPowerW:         10100,
		SecondsRemaining:   150,
	}
	if !hardStopRequired(s) {
		t.Fatal("hard stop not issued although the limit is reached before the hour ends")
	}
}

func TestHardStopNotRequiredWhenHourEndsUnderLimit(t *testing.T) {
	// 0.4 kWh left at 10.1 kW with 120 s left: 0.337 kWh more this hour,
	// ending ~63 Wh under the limit. Stopping only costs charging.
	s := ControllerSnapshot{
		Charging:           true,
		EnergyValid:        true,
		RemainingEnergyKWh: 0.40,
		GridPowerW:         10100,
		SecondsRemaining:   120,
	}
	if hardStopRequired(s) {
		t.Fatal("hard stop issued although the hour is projected to end under the limit")
	}
}

func TestHardStopNotRequiredWhenOnPaceAtEndOfHour(t *testing.T) {
	// Observed 2026-10-09 23:58: on pace for 9.5 kWh, so remaining energy
	// and the energy still to be drawn this hour are about equal.
	s := ControllerSnapshot{
		Charging:           true,
		EnergyValid:        true,
		RemainingEnergyKWh: 9.5 * 120 / 3600,
		GridPowerW:         9400,
		SecondsRemaining:   120,
	}
	if hardStopRequired(s) {
		t.Fatal("hard stop issued for an hour paced exactly to the limit")
	}
}

func TestHardStopRequiredWhenProjectedOverLimitAtEndOfHour(t *testing.T) {
	// 0.25 kWh left at 10.1 kW with 120 s left: 0.337 kWh projected.
	s := ControllerSnapshot{
		Charging:           true,
		EnergyValid:        true,
		RemainingEnergyKWh: 0.25,
		GridPowerW:         10100,
		SecondsRemaining:   120,
	}
	if !hardStopRequired(s) {
		t.Fatal("hard stop not issued although the hour is projected over the limit")
	}
}

func TestHardStopNotRequiredOutsideLeadTime(t *testing.T) {
	// 0.8 kWh at 10 kW is ~285 s: beyond the stop latency, keep regulating.
	s := ControllerSnapshot{
		Charging:           true,
		EnergyValid:        true,
		RemainingEnergyKWh: 0.80,
		GridPowerW:         10100,
		SecondsRemaining:   600,
	}
	if hardStopRequired(s) {
		t.Fatal("hard stop issued outside the lead time")
	}
}

func TestHardStopWhenHouseholdUsesTheRestOfTheBudget(t *testing.T) {
	// 5 kWh limit, 25 min left, 1.2 kWh left: the car's 6 A floor (4.1 kW)
	// plus 2.7 kW of household would breach long before the lead time, and
	// the household alone needs 1.1 kWh of what is left. Stop now.
	s := ControllerSnapshot{
		Charging: true, EnergyValid: true,
		RemainingEnergyKWh: 1.2, GridPowerW: 6840, SecondsRemaining: 1500,
		Central100Phase1A: 10, Central100Phase2A: 10, Central100Phase3A: 9.7,
		Central101Phase1A: 6, Central101Phase2A: 6, Central101Phase3A: 6,
	}
	if !hardStopRequired(s) {
		t.Fatal("hard stop not issued although the household alone uses the rest of the budget")
	}
	// Same hour with 1.6 kWh left: household 1.1 kWh + car until the stop
	// takes effect 0.2 kWh still fits; keep charging.
	s.RemainingEnergyKWh = 1.6
	if hardStopRequired(s) {
		t.Fatal("hard stop issued although household and the car's stop latency fit")
	}
}

func TestHardStopHouseholdFromGaroMeters(t *testing.T) {
	// A car ramping up: Tibber's grid power already includes most of the
	// ramp, CENTRAL101 (and CENTRAL100) still show the car at 6 A. Grid
	// minus car would count ~6.6 kW of car as household and stop the car
	// for the rest of the hour; GARO's own meters do not.
	s := ControllerSnapshot{
		Charging: true, EnergyValid: true, Source: "tibber",
		RemainingEnergyKWh: 5.0, GridPowerW: 13000, SecondsRemaining: 2400,
		Central100Phase1A: 8, Central100Phase2A: 7, Central100Phase3A: 7.5,
		Central101Phase1A: 6, Central101Phase2A: 6, Central101Phase3A: 6,
	}
	if hardStopHouseholdRequired(s) {
		t.Fatalf("household estimate %.0f W stopped the car", garoHouseholdW(s))
	}
}

func TestHouseholdEstimateSources(t *testing.T) {
	base := ControllerSnapshot{
		GridPowerW:        6000,
		Central100Phase1A: 12, Central100Phase2A: 10, Central100Phase3A: 9,
		Central101Phase1A: 6, Central101Phase2A: 6, Central101Phase3A: 6,
	}
	carW := 18 * hardStopVoltsPerPhase
	garoW := 13 * hardStopVoltsPerPhase

	tibber := base
	tibber.Source = "tibber"
	if got := householdEstimateW(tibber); got != 6000-carW {
		t.Errorf("Tibber available: %.0f W, want Tibber minus chargers %.0f W", got, 6000-carW)
	}

	ramping := tibber
	ramping.GridPowerW = 13000 // Tibber ahead of CENTRAL101 while the car ramps
	if got := householdEstimateW(ramping); got != garoW {
		t.Errorf("Tibber ahead of GARO: %.0f W, want GARO's %.0f W", got, garoW)
	}

	fallback := base
	fallback.Source = "central100"
	if got := householdEstimateW(fallback); got != garoW {
		t.Errorf("Tibber unavailable: %.0f W, want GARO's %.0f W", got, garoW)
	}
}
