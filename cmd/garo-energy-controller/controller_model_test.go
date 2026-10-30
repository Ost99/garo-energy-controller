package main

import (
	"math"
	"testing"
	"time"
)

// garoSim is a single-car GARO plant built from the October 2026
// measurements (TODO.md). Its parameters are deliberately varied around the
// values the model assumes, so the tests check robustness, not
// self-consistency.
type garoSim struct {
	raiseRoomA    float64 // raise when every phase has this much room
	cutOverA      float64 // cut when a phase is this far over DLM
	stepUpA       int     // pilot step per raise
	updateSeconds float64 // GARO update interval; it acts on the previous update's reading

	carPhases [3]bool
	carMaxA   float64 // car draws min(pilot, carMaxA) − carBelowA
	carBelowA float64
	carTau    float64

	dlm      int
	pilot    int
	carA     float64
	measured [3]float64 // site currents as GARO saw them at its last update
	sinceUpd float64
}

func (g *garoSim) site(household [3]float64) [3]float64 {
	var s [3]float64
	for p := 0; p < 3; p++ {
		s[p] = household[p]
		if g.carPhases[p] {
			s[p] += g.carA
		}
	}
	return s
}

func (g *garoSim) step(dt float64, household [3]float64) {
	g.sinceUpd += dt
	if g.sinceUpd >= g.updateSeconds {
		g.sinceUpd = 0
		over, room := -100.0, 100.0
		for p := 0; p < 3; p++ {
			over = math.Max(over, g.measured[p]-float64(g.dlm))
			room = math.Min(room, float64(g.dlm)-g.measured[p])
		}
		switch {
		case over >= g.cutOverA && over > 4:
			g.pilot = 6
		case over >= g.cutOverA:
			g.pilot = max(6, g.pilot-1)
		case room >= g.raiseRoomA:
			g.pilot = min(32, g.pilot+g.stepUpA)
		}
		g.measured = g.site(household) // stale until the next update
	}
	draw := math.Max(0, math.Min(float64(g.pilot), g.carMaxA)-g.carBelowA)
	g.carA += (draw - g.carA) * (1 - math.Exp(-dt/g.carTau))
}

type simScenario struct {
	carPhases   [3]bool
	carMaxA     float64
	household   func(sec float64) [3]float64
	startSecond float64 // car plugged in this far into the hour
}

type simResult struct {
	hourEnergyKWh float64
	writes        int
	writeReasons  []string
	peakGridW     float64 // after the first 10 minutes of charging
	minGridW      float64
	oscillating   bool
}

type plantVariant struct {
	name       string
	raiseRoomA float64
	cutOverA   float64
	stepUpA    int
	updateSec  float64
}

var plantVariants = []plantVariant{
	{"nominal", 4.0, 1.0, 1, 10},
	{"eager raise", 3.6, 1.0, 2, 10},
	{"lazy raise", 4.3, 1.0, 1, 15},
	{"tight cut", 4.0, 0.6, 1, 5},
	{"loose cut", 4.0, 1.5, 2, 15},
}

// simulateModelHour runs one hour from 23:00 with the controller ticking
// every 30 s, using the real pacing (populateEnergyDiagnostics) and the
// model's DLM decisions.
func simulateModelHour(t *testing.T, sc simScenario, v plantVariant) simResult {
	t.Helper()
	cfg := defaultConfig()
	cfg.ControlTuning.ControlStrategy = "model"
	const volts = 230.0 // the controller assumes 240 W per phase amp
	const dt = 5.0

	g := &garoSim{
		raiseRoomA: v.raiseRoomA, cutOverA: v.cutOverA, stepUpA: v.stepUpA,
		updateSeconds: v.updateSec,
		carPhases:     sc.carPhases, carMaxA: sc.carMaxA, carBelowA: 0.3, carTau: 10,
		dlm: 16, pilot: 6,
	}
	var m dlmModel
	start := time.Date(2026, 10, 30, 23, 0, 0, 0, time.Local)
	lastWrite := start.Add(-time.Hour)
	res := simResult{minGridW: math.Inf(1)}

	energy := 0.0
	for sec := 0.0; sec < 3600; sec += dt {
		hh := sc.household(sec)
		charging := sec >= sc.startSecond
		if charging {
			g.step(dt, hh)
		} else {
			g.measured = hh
		}
		site := g.site(hh)
		if !charging {
			site = hh
		}
		gridW := (site[0] + site[1] + site[2]) * volts
		energy += gridW * dt / 3600000.0
		if charging && sec >= sc.startSecond+600 {
			res.peakGridW = math.Max(res.peakGridW, gridW)
			res.minGridW = math.Min(res.minGridW, gridW)
		}

		if !charging || math.Mod(sec, 30) != 0 {
			continue
		}
		now := start.Add(time.Duration(sec) * time.Second)
		var s ControllerSnapshot
		populateEnergyDiagnostics(&s, cfg, "sim", energy, gridW, now, true)

		var car [3]float64
		for p := 0; p < 3; p++ {
			if g.carPhases[p] {
				car[p] = g.carA
			}
		}
		out := m.step(modelInput{
			Now: now, GridW: gridW, SitePhaseA: site, CarPhaseA: car,
			CarPhases: g.carPhases, PilotA: g.pilot, MaxPilotA: 32,
			TargetW: s.EffectiveTargetPowerW, DLMA: g.dlm,
			SinceWrite: now.Sub(lastWrite),
			MinA:       cfg.MinimumCurrentA, MaxA: cfg.MaximumCurrentA,
			WattsPerPhaseAmp: cfg.ControlTuning.OnePhaseWattsPerAmp,
		})
		res.oscillating = res.oscillating || out.Oscillating
		if out.DLMA != g.dlm {
			g.dlm = out.DLMA
			lastWrite = now
			res.writes++
			res.writeReasons = append(res.writeReasons, out.Reason)
		}
	}
	res.hourEnergyKWh = energy
	return res
}

var (
	threePhase = [3]bool{true, true, true}
	onePhaseL2 = [3]bool{false, true, false}
)

func steadyHousehold(h [3]float64) func(float64) [3]float64 {
	return func(float64) [3]float64 { return h }
}

func checkHour(t *testing.T, name string, r simResult, minKWh float64, maxWrites int) {
	t.Helper()
	limit := defaultConfig().HourlyLimitKWh
	t.Logf("%s: %.2f kWh, %d writes, grid %.0f..%.0f W, oscillating %v",
		name, r.hourEnergyKWh, r.writes, r.minGridW, r.peakGridW, r.oscillating)
	if r.hourEnergyKWh > limit {
		t.Errorf("%s: %.3f kWh over the %.1f kWh limit", name, r.hourEnergyKWh, limit)
	}
	if r.hourEnergyKWh < minKWh {
		t.Errorf("%s: %.3f kWh, want at least %.2f", name, r.hourEnergyKWh, minKWh)
	}
	if r.writes > maxWrites {
		t.Errorf("%s: %d DLM writes, want at most %d: %v", name, r.writes, maxWrites, r.writeReasons)
	}
}

func TestModelThreePhaseSteadyHousehold(t *testing.T) {
	for _, v := range plantVariants {
		r := simulateModelHour(t, simScenario{
			carPhases: threePhase, carMaxA: 16,
			household: steadyHousehold([3]float64{4.5, 1, 2}),
		}, v)
		checkHour(t, "3-phase steady/"+v.name, r, 8.8, 12)
		if swing := r.peakGridW - r.minGridW; swing > 2500 {
			t.Errorf("3-phase steady/%s: grid swings %.0f W in steady state", v.name, swing)
		}
	}
}

func TestModelOnePhaseCarFreePhaseBlocksRaise(t *testing.T) {
	// 1-phase car on L2 while L1 (no car) carries 10 A of household load:
	// GARO cannot raise past L1, and the model must not keep writing.
	for _, v := range plantVariants {
		r := simulateModelHour(t, simScenario{
			carPhases: onePhaseL2, carMaxA: 28,
			household: steadyHousehold([3]float64{10, 1, 2}),
		}, v)
		checkHour(t, "1-phase blocked/"+v.name, r, 0, 6)
	}
}

func TestModelOnePhaseSelfLimitedCar(t *testing.T) {
	// The car stops at 28 A whatever the pilot: no writes chasing it.
	for _, v := range plantVariants {
		r := simulateModelHour(t, simScenario{
			carPhases: onePhaseL2, carMaxA: 28,
			household: steadyHousehold([3]float64{2, 1, 2}),
		}, v)
		checkHour(t, "1-phase self-limited/"+v.name, r, 0, 6)
	}
}

func TestModelCyclingLoadOnCarPhase(t *testing.T) {
	// A 2.5 kW element on L1 switching on for 20 s every 60 s: GARO cuts the
	// car on every burst. The model must recognise it and not chase.
	burst := func(sec float64) [3]float64 {
		h := [3]float64{4.5, 1, 2}
		if math.Mod(sec, 60) < 20 {
			h[0] += 11
		}
		return h
	}
	for _, v := range plantVariants {
		r := simulateModelHour(t, simScenario{carPhases: threePhase, carMaxA: 16, household: burst}, v)
		checkHour(t, "3-phase cycling load/"+v.name, r, 0, 12)
		if !r.oscillating {
			t.Errorf("3-phase cycling load/%s: oscillation not recognised", v.name)
		}
	}
}

func TestModelMidHourStart(t *testing.T) {
	for _, v := range plantVariants {
		r := simulateModelHour(t, simScenario{
			carPhases: threePhase, carMaxA: 16, startSecond: 1500,
			household: steadyHousehold([3]float64{4.5, 1, 2}),
		}, v)
		checkHour(t, "3-phase mid-hour start/"+v.name, r, 0, 12)
	}
}

func TestModelSingleCarScope(t *testing.T) {
	base := ControllerSnapshot{
		PilotValid:        true,
		PhaseMode:         "one-phase",
		Central101Phase1A: 0, Central101Phase2A: 10, Central101Phase3A: 0,
	}

	two := base
	two.PilotLevels = []GaroPilotLevel{{SerialNumber: 2, Charging: true}, {SerialNumber: 1, Charging: true}}
	if _, _, reason := modelSingleCar(two); reason == "" {
		t.Error("two charging chargers accepted")
	}

	ok := base
	ok.PilotLevels = []GaroPilotLevel{{SerialNumber: 2, Charging: true, PhaseRotation: 32}, {SerialNumber: 1}}
	if _, phases, reason := modelSingleCar(ok); reason != "" || phases != [3]bool{false, true, false} {
		t.Errorf("single 1-phase car on L2 rejected: %q %v", reason, phases)
	}

	mismatch := base
	mismatch.PilotLevels = []GaroPilotLevel{{SerialNumber: 2, Charging: true, PhaseRotation: 16}}
	if _, _, reason := modelSingleCar(mismatch); reason == "" {
		t.Error("rotation/CENTRAL101 mismatch not detected")
	}
}

func TestRotationFirstPhase(t *testing.T) {
	for phase, want := range map[int]int{16: 1, 32: 2, 48: 3, 0: 0, 64: 0} {
		if got := rotationFirstPhase(phase); got != want {
			t.Errorf("rotationFirstPhase(%d) = %d, want %d", phase, got, want)
		}
	}
}

func TestModelStrategyIsOptIn(t *testing.T) {
	if got := defaultConfig().ControlTuning.ControlStrategy; got != "incremental" {
		t.Fatalf("default control_strategy %q, want \"incremental\" until tested", got)
	}
}

func TestModelRaiseBlockedByCarFreePhase(t *testing.T) {
	// 1-phase car at 8 A on L2; L1 (no car) carries 14 A of household load.
	// GARO only raises with ~4 A room on every phase, so any DLM that allows
	// a raise lets GARO take the car far past the wanted ~10 A: hold.
	var m dlmModel
	now := time.Date(2026, 10, 30, 23, 30, 0, 0, time.Local)
	in := modelInput{
		Now: now, GridW: (14 + 9 + 2) * 240,
		SitePhaseA: [3]float64{14, 9, 2}, CarPhaseA: [3]float64{0, 8, 0},
		CarPhases: onePhaseL2, PilotA: 8, MaxPilotA: 32,
		TargetW: (14+1+2)*240 + 10.5*240, DLMA: 16, SinceWrite: time.Hour,
		MinA: 6, MaxA: 50, WattsPerPhaseAmp: 240,
	}
	out := m.step(in)
	if out.DLMA != in.DLMA {
		t.Fatalf("raised DLM to %d although GARO would overshoot: %s", out.DLMA, out.Reason)
	}
}
