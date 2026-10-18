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
	carAt5MinA    float64
}

// simulateModelHour runs the model controller against the plant for one
// hour. householdAt returns per-phase household current at second t.
func simulateModelHour(t *testing.T, householdAt func(sec float64) [3]float64) simResult {
	t.Helper()
	cfg := defaultConfig()
	tuning := cfg.ControlTuning
	const v = 240.0
	const dt = 5.0

	plant := &garoPlant{reserve: 0.11, pilotStepSeconds: 10, carTau: 10, carMaxA: 16, pilotA: 6, carA: 6}
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
			CanIncrease: s.SecondsRemaining >= noIncreaseFinalSeconds,
			MinA:        cfg.MinimumCurrentA, MaxA: cfg.MaximumCurrentA,
		}, tuning)
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

func TestModelCyclingSinglePhaseLoad(t *testing.T) {
	// The 2026-10-10 23:30 situation: ~3 A base household per phase plus
	// a ~11 A load on L2 cycling about once a minute.
	r := simulateModelHour(t, func(sec float64) [3]float64 {
		l2 := 3.4
		if math.Mod(sec, 70) < 35 {
			l2 += 11
		}
		return [3]float64{3.1, l2, 1.6}
	})
	t.Logf("cycling load: %.2f kWh, %d DLM writes, DLM %d..%d A",
		r.hourEnergyKWh, r.dlmWrites, r.minDLM, r.maxDLM)
	if r.hourEnergyKWh > 9.5 {
		t.Errorf("hour energy %.3f kWh over the 9.5 kWh limit", r.hourEnergyKWh)
	}
	if r.hourEnergyKWh < 8.9 {
		t.Errorf("hour energy %.3f kWh: leaves too much budget unused", r.hourEnergyKWh)
	}
	if r.dlmWrites > 15 {
		t.Errorf("%d DLM writes in one hour: still chasing the load", r.dlmWrites)
	}
}

func TestModelSteadyHouseholdRampUp(t *testing.T) {
	r := simulateModelHour(t, func(float64) [3]float64 {
		return [3]float64{3, 2, 2}
	})
	t.Logf("steady load: %.2f kWh, %d DLM writes, car %.1f A after 5 min",
		r.hourEnergyKWh, r.dlmWrites, r.carAt5MinA)
	if r.hourEnergyKWh > 9.5 || r.hourEnergyKWh < 9.2 {
		t.Errorf("hour energy %.3f kWh, want 9.2..9.5", r.hourEnergyKWh)
	}
	// Target ~9.5 kW with ~1.7 kW household: ~10.8 A car.
	if r.carAt5MinA < 9.5 {
		t.Errorf("car at %.1f A after 5 min: ramp-up too slow", r.carAt5MinA)
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
