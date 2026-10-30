package main

import (
	"math"
	"testing"
	"time"
)

// garoPlant simulates GARO LB100 + one three-phase car, using the behaviour
// observed in the 2026-10-10 logs: GARO limits the worst phase to
// DLM × (1 − reserve), drops the pilot immediately, raises it 1 A per
// pilotStepSeconds, and the car follows the pilot with a first-order lag.
type garoPlant struct {
	reserve          float64
	pilotStepSeconds float64
	carTau           float64
	carMaxA          float64

	pilotA      float64
	carA        float64
	sinceRaiseS float64
}

func (p *garoPlant) step(dt float64, dlm int, household [3]float64) {
	worst := math.Max(household[0], math.Max(household[1], household[2]))
	allowed := math.Floor(float64(dlm)*(1-p.reserve) - worst)
	allowed = math.Max(6, math.Min(p.carMaxA, allowed))

	p.sinceRaiseS += dt
	switch {
	case allowed < p.pilotA:
		p.pilotA = allowed
	case allowed > p.pilotA && p.sinceRaiseS >= p.pilotStepSeconds:
		p.pilotA++
		p.sinceRaiseS = 0
	}
	p.carA += (p.pilotA - p.carA) * (1 - math.Exp(-dt/p.carTau))
}

type simResult struct {
	hourEnergyKWh float64
	dlmWrites     int
	minDLM        int
	maxDLM        int
	maxIntegralA  float64
	maxGridW      float64 // after the first 5 minutes (initial ramp-up)
	carAt5MinA    float64
}

// simulateModelHour runs the model controller against the plant for one
// hour. householdAt returns per-phase household current at second t.
func simulateModelHour(t *testing.T, plantReserve, plantStepS float64, householdAt func(sec float64) [3]float64) simResult {
	t.Helper()
	cfg := defaultConfig()
	tuning := cfg.ControlTuning
	const v = 240.0
	const dt = 5.0

	plant := &garoPlant{reserve: plantReserve, pilotStepSeconds: plantStepS, carTau: 10, carMaxA: 16, pilotA: 6, carA: 6}
	var model dlmModel
	dlm := 16
	lastChange := -1e9
	res := simResult{minDLM: dlm, maxDLM: dlm}
	start := time.Date(2026, 10, 10, 23, 0, 0, 0, time.Local)

	energyKWh := 0.0
	for sec := 0.0; sec < 3600; sec += dt {
		hh := householdAt(sec)
		plant.step(dt, dlm, hh)

		gridW := 0.0
		var site, car [3]float64
		for i := 0; i < 3; i++ {
			car[i] = plant.carA
			site[i] = hh[i] + plant.carA
			gridW += site[i] * v
		}
		energyKWh += gridW * dt / 3600000.0
		if sec >= 300 && gridW > res.maxGridW {
			res.maxGridW = gridW
		}
		if sec == 300 {
			res.carAt5MinA = plant.carA
		}

		if math.Mod(sec, 30) != 0 {
			continue
		}
		now := start.Add(time.Duration(sec) * time.Second)
		var s ControllerSnapshot
		populateEnergyDiagnostics(&s, cfg, "sim", energyKWh, gridW, now, true)
		out := model.step(modelInput{
			Now: now, GridW: gridW, SitePhaseA: site, CarPhaseA: car,
			TargetW: s.EffectiveTargetPowerW, CurrentDLMA: dlm,
			CanIncrease: true,
			MinA:        cfg.MinimumCurrentA, MaxA: cfg.MaximumCurrentA,
		}, tuning)
		if out.IntegralA > res.maxIntegralA {
			res.maxIntegralA = out.IntegralA
		}
		if out.TargetDLMA != dlm && sec-lastChange >= float64(tuning.NormalDwellSeconds) {
			dlm = out.TargetDLMA
			lastChange = sec
			res.dlmWrites++
			if dlm < res.minDLM {
				res.minDLM = dlm
			}
			if dlm > res.maxDLM {
				res.maxDLM = dlm
			}
		}
	}
	res.hourEnergyKWh = energyKWh
	return res
}

// The controller assumes model_reserve_fraction 0.11. The plant uses a
// range of reserves and pilot ramp rates around the observed values, so
// the tests check robustness to model error, not self-consistency.
var plantVariants = []struct{ reserve, pilotStepS float64 }{
	{0.06, 10}, {0.06, 25}, {0.11, 10}, {0.11, 25}, {0.16, 10}, {0.16, 25},
}

func TestModelCyclingSinglePhaseLoad(t *testing.T) {
	// The 2026-10-10 23:30 situation: ~3 A base household per phase plus
	// a ~11 A load on L2 cycling about once a minute.
	cycling := func(sec float64) [3]float64 {
		l2 := 3.4
		if math.Mod(sec, 70) < 35 {
			l2 += 11
		}
		return [3]float64{3.1, l2, 1.6}
	}
	// Regulation is measured against the target, not the absolute limit;
	// the limit belongs to the hard stop, which this simulation excludes.
	cfg := defaultConfig()
	targetKWh := cfg.HourlyLimitKWh - cfg.TargetMarginWh/1000.0
	for _, v := range plantVariants {
		r := simulateModelHour(t, v.reserve, v.pilotStepS, cycling)
		t.Logf("reserve %.2f, pilot step %.0f s: %.2f kWh, %d DLM writes, DLM %d..%d A, max integral %.2f A, peak grid %.0f W",
			v.reserve, v.pilotStepS, r.hourEnergyKWh, r.dlmWrites, r.minDLM, r.maxDLM, r.maxIntegralA, r.maxGridW)
		if r.hourEnergyKWh > targetKWh {
			t.Errorf("reserve %.2f, step %.0f s: %.3f kWh over the %.3f kWh target",
				v.reserve, v.pilotStepS, r.hourEnergyKWh, targetKWh)
		}
		// Ending under target is preferred over swings: GARO cuts the car on
		// every load cycle, and the controller must not chase that loss.
		if r.hourEnergyKWh < targetKWh-1.5 {
			t.Errorf("reserve %.2f, step %.0f s: %.3f kWh leaves too much budget unused",
				v.reserve, v.pilotStepS, r.hourEnergyKWh)
		}
		// Before the integral fix: integral 2.8-5.6 A, peak grid 11.0-11.7 kW.
		if r.maxIntegralA > 1.0 {
			t.Errorf("reserve %.2f, step %.0f s: integral wound up to %.2f A",
				v.reserve, v.pilotStepS, r.maxIntegralA)
		}
		if r.maxGridW > 10500 {
			t.Errorf("reserve %.2f, step %.0f s: grid peaked at %.0f W",
				v.reserve, v.pilotStepS, r.maxGridW)
		}
		if r.dlmWrites > 15 {
			t.Errorf("reserve %.2f, step %.0f s: %d DLM writes in one hour",
				v.reserve, v.pilotStepS, r.dlmWrites)
		}
	}
}

func TestModelSteadyHouseholdRampUp(t *testing.T) {
	steady := func(float64) [3]float64 { return [3]float64{3, 2, 2} }
	cfg := defaultConfig()
	targetKWh := cfg.HourlyLimitKWh - cfg.TargetMarginWh/1000.0
	for _, v := range plantVariants {
		r := simulateModelHour(t, v.reserve, v.pilotStepS, steady)
		t.Logf("reserve %.2f, pilot step %.0f s: %.2f kWh, %d DLM writes, car %.1f A after 5 min",
			v.reserve, v.pilotStepS, r.hourEnergyKWh, r.dlmWrites, r.carAt5MinA)
		if r.hourEnergyKWh > targetKWh || r.hourEnergyKWh < targetKWh-0.3 {
			t.Errorf("reserve %.2f, step %.0f s: %.3f kWh, want %.3f..%.3f",
				v.reserve, v.pilotStepS, r.hourEnergyKWh, targetKWh-0.3, targetKWh)
		}
		// Target ~9.5 kW with ~1.7 kW household: ~10.8 A car.
		if r.carAt5MinA < 9.5 {
			t.Errorf("reserve %.2f, step %.0f s: car at %.1f A after 5 min",
				v.reserve, v.pilotStepS, r.carAt5MinA)
		}
		if r.dlmWrites > 15 {
			t.Errorf("reserve %.2f, step %.0f s: %d DLM writes in one hour",
				v.reserve, v.pilotStepS, r.dlmWrites)
		}
	}
}

func TestModelStrategyIsOptIn(t *testing.T) {
	if got := defaultConfig().ControlTuning.ControlStrategy; got != "incremental" {
		t.Fatalf("default control_strategy %q, want \"incremental\" until tested", got)
	}
}

func TestModelNoIncreaseWhenNotAllowed(t *testing.T) {
	var m dlmModel
	tuning := defaultConfig().ControlTuning
	now := time.Now()
	in := modelInput{
		Now: now, GridW: 3000,
		SitePhaseA: [3]float64{10, 6, 6}, CarPhaseA: [3]float64{6, 6, 6},
		TargetW: 9500, CurrentDLMA: 16, CanIncrease: false, MinA: 6, MaxA: 50,
	}
	for i := 0; i < 20; i++ {
		in.Now = now.Add(time.Duration(i*30) * time.Second)
		out := m.step(in, tuning)
		if out.TargetDLMA > 16 {
			t.Fatalf("DLM raised to %d A while increases are not allowed", out.TargetDLMA)
		}
	}
	if m.integralA != 0 {
		t.Fatalf("integral wound up to %.2f A while pinned", m.integralA)
	}
}

func TestModelRecoversInFinalMinutes(t *testing.T) {
	// 2026-10-24 20:56: the model reduced DLM 15 -> 13 on a lagging average
	// and was then held at 13 A for the rest of the hour by the final-window
	// no-increase rule while its own target rose to 18 A.
	cfg := defaultConfig()
	cfg.ControlTuning.ControlStrategy = "model"
	c := &EnergyController{}
	s := ControllerSnapshot{
		SecondsRemaining:      120,
		EffectiveTargetPowerW: 9400,
		PhaseMode:             "multi-phase",
		Central100Phase1A:     11.6, Central100Phase2A: 9.0, Central100Phase3A: 9.0,
		Central101Phase1A: 7.6, Central101Phase2A: 7.6, Central101Phase3A: 7.6,
	}
	target := c.modelDecision(&s, cfg, time.Now(), 6000, 13, 2*time.Minute)
	if target <= 13 {
		t.Fatalf("model held DLM at %d A in the final minutes: %s", target, s.Decision)
	}
}
