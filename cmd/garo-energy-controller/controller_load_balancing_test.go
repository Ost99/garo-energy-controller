package main

import (
	"strings"
	"testing"
)

func TestChargerLoadBalancingDiagnosticsAllBalanced(t *testing.T) {
	valid, all, errText := chargerLoadBalancingDiagnostics([]GaroPilotLevel{
		{SerialNumber: 771285, LoadBalanced: true, LoadBalancedKnown: true},
		{SerialNumber: 771240, LoadBalanced: true, LoadBalancedKnown: true},
	}, true, false)
	if !valid || !all || errText != "" {
		t.Fatalf("got valid=%v all=%v error=%q", valid, all, errText)
	}
}

func TestChargerLoadBalancingDiagnosticsDetectsDisabledMaster(t *testing.T) {
	valid, all, errText := chargerLoadBalancingDiagnostics([]GaroPilotLevel{
		{SerialNumber: 771285, LoadBalanced: false, LoadBalancedKnown: true},
		{SerialNumber: 771240, LoadBalanced: true, LoadBalancedKnown: true},
	}, true, false)
	if !valid || all {
		t.Fatalf("got valid=%v all=%v, want valid=true all=false", valid, all)
	}
	if !strings.Contains(errText, "771285") || !strings.Contains(errText, "not load balanced") {
		t.Fatalf("unexpected error %q", errText)
	}
}

func TestChargerLoadBalancingDiagnosticsUnknownIsNotFalse(t *testing.T) {
	valid, all, errText := chargerLoadBalancingDiagnostics([]GaroPilotLevel{
		{SerialNumber: 771285, LoadBalancedKnown: false},
		{SerialNumber: 771240, LoadBalanced: true, LoadBalancedKnown: true},
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

func TestHardStopEndOfHourMargin(t *testing.T) {
	// Limit reached ~143 s from now, hour ends in 120 s: within the 30 s
	// margin for measurement error, so stop.
	s := ControllerSnapshot{
		Charging:           true,
		EnergyValid:        true,
		RemainingEnergyKWh: 0.40,
		GridPowerW:         10100,
		SecondsRemaining:   120,
	}
	if !hardStopRequired(s) {
		t.Fatal("hard stop not issued within the end-of-hour margin")
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
