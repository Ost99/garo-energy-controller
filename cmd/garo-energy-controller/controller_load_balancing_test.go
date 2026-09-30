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
		ChargerLoadBalancingValid: true,
		AllChargersLoadBalanced:   false,
	}
	if !hardStopRequired(s) {
		t.Fatal("hard stop should remain armed when charger load balancing is disabled")
	}
}
