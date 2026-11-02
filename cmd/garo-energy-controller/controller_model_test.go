package main

import (
	"fmt"
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
	cutOverA      float64 // cut 1 A when a phase is this far over DLM, to 6 A when 1 A further
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
	stopped  bool // hard stop: charging off
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
		case over >= g.cutOverA+1:
			g.pilot = 6 // about 2 A over: straight to the minimum
		case over >= g.cutOverA:
			g.pilot = max(6, g.pilot-1)
		case room >= g.raiseRoomA:
			g.pilot = min(32, g.pilot+g.stepUpA)
		}
		g.measured = g.site(household) // stale until the next update
	}
	draw := math.Max(0, math.Min(float64(g.pilot), g.carMaxA)-g.carBelowA)
	if g.stopped {
		draw = 0
	}
	g.carA += (draw - g.carA) * (1 - math.Exp(-dt/g.carTau))
}

type simScenario struct {
	carPhases   [3]bool
	carMaxA     float64
	household   func(sec float64) [3]float64
	startSecond float64 // car plugged in this far into the hour
	limitKWh    float64 // hourly limit; 0 = the default configuration

	// Optional: the hourly limit changes to limitAfterKWh at changeAtSecond.
	changeAtSecond float64
	limitAfterKWh  float64
	reduction      string // ModelReduction; "" = default
}

type simResult struct {
	hourEnergyKWh float64
	writes        int
	writeReasons  []string
	garoSlams     int // GARO cuts straight to the minimum
	hardStops     int
	followSeconds float64 // after a limit change: until the car is near the wanted level
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
	if sc.limitKWh > 0 {
		cfg.HourlyLimitKWh = sc.limitKWh
	}
	if sc.reduction != "" {
		cfg.ControlTuning.ModelReduction = sc.reduction
	}

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
	res := simResult{minGridW: math.Inf(1), followSeconds: -1}

	energy := 0.0
	for sec := 0.0; sec < 3600; sec += dt {
		hh := sc.household(sec)
		charging := sec >= sc.startSecond
		if charging {
			before := g.pilot
			g.step(dt, hh)
			if g.pilot == 6 && before > 8 {
				res.garoSlams++
			}
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
		if sc.limitAfterKWh > 0 && sec >= sc.changeAtSecond {
			cfg.HourlyLimitKWh = sc.limitAfterKWh
		}
		var s ControllerSnapshot
		populateEnergyDiagnostics(&s, cfg, "sim", energy, gridW, now, true)
		s.Charging = true
		carPhaseA := [3]float64{}
		for p, on := range g.carPhases {
			if on {
				carPhaseA[p] = g.carA
			}
		}
		s.Central101Phase1A, s.Central101Phase2A, s.Central101Phase3A =
			carPhaseA[0], carPhaseA[1], carPhaseA[2]
		s.Central100Phase1A, s.Central100Phase2A, s.Central100Phase3A =
			site[0], site[1], site[2]
		s.HouseholdAvgW = m.householdAvgW
		if !g.stopped && hardStopRequired(s) {
			// The controller's hard stop: charging off for the rest of the
			// hour (the car's floor plus household can exceed a low limit).
			g.stopped = true
			res.hardStops++
		}

		var car [3]float64
		for p := 0; p < 3; p++ {
			if g.carPhases[p] {
				car[p] = g.carA
			}
		}
		out := m.step(modelInput{
			Now: now, GridW: gridW, SitePhaseA: site, CarPhaseA: car,
			CarPhases: g.carPhases, PilotA: g.pilot, MaxPilotA: 32,
			TargetW: s.EffectiveTargetPowerW, BudgetCeilingW: s.HardBudgetCeilingW, DLMA: g.dlm,
			SinceWrite: now.Sub(lastWrite),
			MinA:       cfg.MinimumCurrentA, MaxA: cfg.MaximumCurrentA,
			WattsPerPhaseAmp: cfg.ControlTuning.OnePhaseWattsPerAmp,
			Reduction:        cfg.ControlTuning.ModelReduction,
		})
		if sc.limitAfterKWh > 0 && sec >= sc.changeAtSecond && res.followSeconds < 0 &&
			sec > sc.changeAtSecond && g.carA <= out.WantPilotA+modelStepDeadbandA {
			res.followSeconds = sec - sc.changeAtSecond
		}
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
	checkHourLimit(t, name, r, defaultConfig().HourlyLimitKWh, minKWh, maxWrites)
}

func checkHourLimit(t *testing.T, name string, r simResult, limit, minKWh float64, maxWrites int) {
	t.Helper()
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
		checkHour(t, "1-phase blocked/"+v.name, r, 0, 8)
	}
}

func TestModelOnePhaseSelfLimitedCar(t *testing.T) {
	// The car stops at 28 A whatever the pilot: no writes chasing it.
	for _, v := range plantVariants {
		r := simulateModelHour(t, simScenario{
			carPhases: onePhaseL2, carMaxA: 28,
			household: steadyHousehold([3]float64{2, 1, 2}),
		}, v)
		checkHour(t, "1-phase self-limited/"+v.name, r, 0, 8)
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

func TestModelShortHouseholdDipDoesNotMoveDLMDown(t *testing.T) {
	// Seen 2026-10-30: a microwave paused for ~30 s, the model moved DLM
	// 23 -> 18 during the pause, and GARO cut the car to 6 A when the
	// microwave came back. A dip must last several ticks before DLM moves.
	var m dlmModel
	now := time.Date(2026, 10, 30, 23, 28, 0, 0, time.Local)
	base := modelInput{
		GridW: 9600, CarPhaseA: [3]float64{9, 9, 9}, CarPhases: threePhase,
		PilotA: 9, MaxPilotA: 32, TargetW: 9500, DLMA: 23, SinceWrite: time.Hour,
		MinA: 6, MaxA: 50, WattsPerPhaseAmp: 240,
	}
	step := func(sec int, l1 float64) modelOutput {
		in := base
		in.Now = now.Add(time.Duration(sec) * time.Second)
		in.SitePhaseA = [3]float64{l1, 9.6, 10.8}
		return m.step(in)
	}
	step(0, 19.6)                              // microwave on
	if out := step(30, 15.3); out.DLMA != 23 { // 30 s pause
		t.Fatalf("DLM moved to %d on a 30 s dip: %s", out.DLMA, out.Reason)
	}
	step(60, 19.6) // back on
	for sec := 90; sec <= 150; sec += 30 {
		if out := step(sec, 15.3); out.DLMA != 23 && sec < 150 {
			t.Fatalf("DLM moved to %d after %d s of a sustained dip", out.DLMA, sec-90)
		} else if sec == 150 && out.DLMA == 23 {
			t.Fatalf("DLM not moved down after a sustained dip: %s", out.Reason)
		}
	}
}

func TestModelLoadWithShortPauses(t *testing.T) {
	// Seen 2026-10-30: a 10 A load on the car's phase, on for ~4.5 minutes
	// and off for ~30 s. The pauses must not make the model move DLM around.
	pauses := func(sec float64) [3]float64 {
		h := [3]float64{4.5, 1, 2}
		if math.Mod(sec, 300) < 270 {
			h[0] += 10
		}
		return h
	}
	for _, v := range plantVariants {
		r := simulateModelHour(t, simScenario{carPhases: threePhase, carMaxA: 16, household: pauses}, v)
		checkHour(t, "3-phase load with short pauses/"+v.name, r, 0, 8)
	}
}

func TestModelLowerAvoidsGaroSlam(t *testing.T) {
	// Seen 2026-10-30: busiest phase 16.9 A, DLM 19 -> 15 put it ~2 A over
	// and GARO cut the car 12 -> 6. The first lowering step must aim for
	// just over 1 A.
	var m dlmModel
	in := modelInput{
		Now: time.Date(2026, 10, 30, 23, 34, 43, 0, time.Local), GridW: 10520,
		SitePhaseA: [3]float64{16.9, 12.5, 14.1}, CarPhaseA: [3]float64{12.1, 12, 12},
		CarPhases: threePhase, PilotA: 12, MaxPilotA: 32, TargetW: 9300,
		DLMA: 19, SinceWrite: time.Hour, MinA: 6, MaxA: 50, WattsPerPhaseAmp: 240,
	}
	out := m.step(in)
	if out.DLMA != 16 {
		t.Fatalf("lowered DLM to %d, want 16 (just over 1 A below the busiest phase): %s", out.DLMA, out.Reason)
	}
}

func TestModelCarAtMaxGetsBurstRoom(t *testing.T) {
	// 1-phase car limited to 20 A by itself, the budget covering the
	// charger's 32 A, and a 12 A element cycling on the car's phase from the
	// ramp onwards. With DLM at the hold position GARO slams the car to 6 A
	// on every burst; with room for the burst within the hour's budget it
	// does not need to.
	oven := func(sec float64) [3]float64 {
		h := [3]float64{4.3, 0.5, 2.2}
		if sec >= 120 && math.Mod(sec, 300) >= 240 { // element starts while the car is still ramping
			h[1] += 12
		}
		return h
	}
	for _, v := range plantVariants {
		r := simulateModelHour(t, simScenario{carPhases: onePhaseL2, carMaxA: 20, household: oven}, v)
		checkHour(t, "1-phase at max with cycling load/"+v.name, r, 0, 8)
		if r.garoSlams > 2 {
			t.Errorf("1-phase at max with cycling load/%s: GARO slammed the car %d times", v.name, r.garoSlams)
		}
	}
}

func TestModelOtherHourlyLimits(t *testing.T) {
	// Nothing in the model assumes a particular hourly limit or car: the
	// same scenarios must stay under 5, 7.5, 9.5 and 15 kWh. Where the
	// budget covers the car's maximum, a cycling element on the car's phase
	// must not make GARO slam the car (household room within the budget).
	element := func(sec float64) [3]float64 {
		h := [3]float64{4.3, 0.5, 2.2}
		if sec >= 120 && math.Mod(sec, 300) >= 240 {
			h[1] += 12
			h[0] += 12 // reaches the 3-phase car too
		}
		return h
	}
	cars := []struct {
		name    string
		phases  [3]bool
		carMaxA float64
	}{
		{"1-phase 20 A", onePhaseL2, 20},
		{"3-phase 16 A", threePhase, 16},
	}
	for _, limit := range []float64{5, 7.5, 9.5, 15} {
		for _, car := range cars {
			for _, v := range plantVariants {
				name := fmt.Sprintf("%.1f kWh/%s/%s", limit, car.name, v.name)
				r := simulateModelHour(t, simScenario{
					carPhases: car.phases, carMaxA: car.carMaxA, household: element, limitKWh: limit,
				}, v)
				// The car's 6 A floor plus the household can exceed a low
				// limit on its own; then the hard stop must stop the car in
				// time for the household to fit in the rest of the hour.
				phasesN := map[bool]float64{true: 3, false: 1}[car.phases[0]]
				floorKW := (6*phasesN + 7 + 12*0.2*map[bool]float64{true: 2, false: 1}[car.phases[0]]) * 230 / 1000
				if floorKW > limit && r.hardStops == 0 {
					t.Errorf("%s: floor above the limit but no hard stop", name)
				}
				// A budget hovering around the car's maximum with a cycling
				// element switches between burst room and limiting the car;
				// those writes are RAM-only 1 A steps, so allow more of them.
				checkHourLimit(t, name, r, limit, 0, 20)
				// GARO cuts are only avoidable when the budget covers the car
				// at its maximum, the household and the element together.
				phases := map[bool]float64{true: 3, false: 1}[car.phases[0]]
				burstPhases := map[bool]float64{true: 2, false: 1}[car.phases[0]]
				needKW := (car.carMaxA*phases + 7 + 12*burstPhases) * 230 / 1000
				if limit-0.1 > needKW && r.garoSlams > 2 {
					t.Errorf("%s: budget covers car and element but GARO slammed the car %d times", name, r.garoSlams)
				}
			}
		}
	}
}

func TestModelLargeReductionModes(t *testing.T) {
	// Seen 2026-11-01: the limit was lowered while a 1-phase car charged at
	// its maximum; wanted fell by ~16 A and 1 A steps every 90 s took 15
	// minutes. Now: faster staircase, and cut and land for large reductions
	// (default "auto"), each selectable.
	for _, mode := range []string{"staircase", "auto", "cut"} {
		for _, v := range plantVariants {
			name := mode + "/" + v.name
			r := simulateModelHour(t, simScenario{
				carPhases: onePhaseL2, carMaxA: 28,
				household:      steadyHousehold([3]float64{4.3, 0.5, 2.2}),
				changeAtSecond: 300, limitAfterKWh: 4.9, reduction: mode,
			}, v)
			t.Logf("%s: car near the wanted level after %.0f s, %d slams, %d writes",
				name, r.followSeconds, r.garoSlams, r.writes)
			checkHourLimit(t, name, r, 4.9, 0, 30)
			switch mode {
			case "staircase":
				if r.followSeconds < 0 || r.followSeconds > 600 {
					t.Errorf("%s: %.0f s to reach the wanted level, want ≤ 600", name, r.followSeconds)
				}
				// The plant variant with the tightest cut tolerance can turn
				// one 1 A step into a slam.
				if r.garoSlams > 1 {
					t.Errorf("%s: staircase slammed the car %d times", name, r.garoSlams)
				}
			default:
				if r.followSeconds < 0 || r.followSeconds > 240 {
					t.Errorf("%s: %.0f s to reach the wanted level, want ≤ 240", name, r.followSeconds)
				}
			}
		}
	}
}
