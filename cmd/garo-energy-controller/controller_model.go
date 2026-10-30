package main

import (
	"fmt"
	"math"
	"time"
)

// Model controller (opt-in, control_strategy "model").
//
// It works with GARO's load-balancing band instead of steering pilots
// directly (measured October 2026, see TODO.md). With S_p the CENTRAL100
// current on phase p:
//
//   - GARO cuts the cars on a phase when S_p ≥ DLM + ~1 A.
//   - GARO raises only when every phase, including phases without a car,
//     has DLM − S_p ≥ ~4 A, one step per update, until the room is gone.
//   - In between it holds, so DLM changes inside the band do nothing.
//   - Large DLM drops overcut (GARO acts again on stale readings).
//
// So the model:
//
//   - raises in one write, to the DLM at which GARO's own ramp stops at the
//     wanted car current;
//   - lowers 1 A below the busiest phase per write, waiting for GARO to
//     settle between steps;
//   - otherwise keeps DLM about modelHoldRoomA above the busiest phase:
//     below GARO's raise threshold, with room for household bursts;
//   - never writes inside the band, and stops chasing when GARO keeps
//     cutting for a cycling household load.
//
// The power setpoint comes from hourly pacing (EffectiveTargetPowerW), which
// already integrates the energy error, so there is no inner PI term.
//
// Scope: exactly one charging car (1- or 3-phase). Other configurations use
// the incremental controller (see modelDecision).

const (
	// garoRaiseRoomA: GARO raises only when every phase has this much room.
	// Measured: no raise at 3.6–3.8 A, raise at 4.1 A.
	garoRaiseRoomA = 4.0

	// modelRaiseLandingRoomA: GARO's ramp stops with about this much room
	// on the car's phases (just under garoRaiseRoomA, 1 A steps).
	modelRaiseLandingRoomA = 3.0

	// modelHoldRoomA: hold DLM this far above the busiest phase. Below the
	// raise threshold, and about 3.5 A of room for bursts before GARO cuts.
	modelHoldRoomA = 2.5

	// modelSettle: after a DLM write, GARO and the car need 1–2 minutes.
	modelSettle = 90 * time.Second

	// modelOscillationSettle: slower reductions while GARO is cutting for a
	// cycling household load.
	modelOscillationSettle = 3 * time.Minute

	// modelOscillationWindow / modelOscillationCuts: this many GARO cuts
	// (pilot drops without a model write) within the window count as a
	// cycling household load.
	modelOscillationWindow = 5 * time.Minute
	modelOscillationCuts   = 2

	// modelRecentPilotWindow: raises are judged against the highest pilot
	// in this window.
	modelRecentPilotWindow = 2 * time.Minute

	// modelHouseholdAverageSeconds: EMA time constant for household power.
	modelHouseholdAverageSeconds = 120.0

	// modelStepDeadbandA: act only when the wanted pilot differs this much
	// from the current one. GARO moves in 1–2 A steps and can overshoot or
	// overcut by one, so a 1 A deadband would hunt.
	modelStepDeadbandA = 1.5

	// modelCarFollowA: the car counts as following its pilot when it draws
	// at least pilot − this.
	modelCarFollowA = 1.5

	// modelResetGap: a longer gap between samples restarts the model.
	modelResetGap = 5 * time.Minute
)

type dlmModel struct {
	initialised   bool
	last          time.Time
	householdAvgW float64
	lastPilotA    int
	lastLower     time.Time // last model write that lowered DLM to make GARO cut
	pilotAtLower  int
	garoCuts      []time.Time
	recentPilots  []timedPilot
}

type timedPilot struct {
	at     time.Time
	pilotA int
}

func (m *dlmModel) reset() { *m = dlmModel{} }

type modelInput struct {
	Now   time.Time
	GridW float64

	SitePhaseA [3]float64 // CENTRAL100
	CarPhaseA  [3]float64 // CENTRAL101
	CarPhases  [3]bool    // phases the charging car draws on

	PilotA    int
	MaxPilotA int // 0 = unknown

	TargetW    float64 // EffectiveTargetPowerW
	DLMA       int
	SinceWrite time.Duration

	MinA int
	MaxA int

	WattsPerPhaseAmp float64 // OnePhaseWattsPerAmp
}

type modelOutput struct {
	DLMA          int // equal to input DLMA when holding
	WantPilotA    float64
	HouseholdAvgW float64
	BusiestA      float64
	Oscillating   bool
	Reason        string
}

func (m *dlmModel) step(in modelInput) modelOutput {
	carW := 0.0
	nPhases := 0
	carMaxA := 0.0
	busiestA := 0.0
	busiestCarA := 0.0
	busiestFreeA := 0.0
	for p := 0; p < 3; p++ {
		carW += in.CarPhaseA[p] * in.WattsPerPhaseAmp
		busiestA = math.Max(busiestA, in.SitePhaseA[p])
		if in.CarPhases[p] {
			nPhases++
			carMaxA = math.Max(carMaxA, in.CarPhaseA[p])
			busiestCarA = math.Max(busiestCarA, in.SitePhaseA[p])
		} else {
			busiestFreeA = math.Max(busiestFreeA, in.SitePhaseA[p])
		}
	}
	householdW := in.GridW - carW

	if m.initialised {
		dt := in.Now.Sub(m.last).Seconds()
		if dt <= 0 || dt > modelResetGap.Seconds() {
			m.reset()
		} else {
			alpha := 1 - math.Exp(-dt/modelHouseholdAverageSeconds)
			m.householdAvgW += alpha * (householdW - m.householdAvgW)

			// A pilot drop the model did not ask for is a GARO cut. The model
			// lowers 1 A at a time, so a larger drop is GARO's own as well.
			drop := m.lastPilotA - in.PilotA
			if drop >= 3 || (drop > 0 && in.Now.Sub(m.lastLower) > modelSettle) {
				m.garoCuts = append(m.garoCuts, in.Now)
			}
		}
	}
	if !m.initialised {
		m.initialised = true
		m.householdAvgW = householdW
	}
	m.last = in.Now
	m.lastPilotA = in.PilotA

	kept := m.garoCuts[:0]
	for _, t := range m.garoCuts {
		if in.Now.Sub(t) <= modelOscillationWindow {
			kept = append(kept, t)
		}
	}
	m.garoCuts = kept
	oscillating := len(m.garoCuts) >= modelOscillationCuts

	// Highest recent pilot: a sample taken just after GARO cut the car for a
	// household burst must not look like room to raise.
	m.recentPilots = append(m.recentPilots, timedPilot{in.Now, in.PilotA})
	recentMaxPilot := in.PilotA
	keptPilots := m.recentPilots[:0]
	for _, rp := range m.recentPilots {
		if in.Now.Sub(rp.at) <= modelRecentPilotWindow {
			keptPilots = append(keptPilots, rp)
			recentMaxPilot = max(recentMaxPilot, rp.pilotA)
		}
	}
	m.recentPilots = keptPilots

	maxPilot := float64(in.MaxA)
	if in.MaxPilotA > 0 {
		maxPilot = float64(in.MaxPilotA)
	}
	want := (in.TargetW - m.householdAvgW) /
		(float64(nPhases) * in.WattsPerPhaseAmp)
	want = math.Max(float64(in.MinA), math.Min(maxPilot, want))

	out := modelOutput{
		DLMA:          in.DLMA,
		WantPilotA:    want,
		HouseholdAvgW: m.householdAvgW,
		BusiestA:      busiestA,
		Oscillating:   oscillating,
	}
	hold := func(reason string) modelOutput {
		out.Reason = reason
		return out
	}
	write := func(dlm int, reason string) modelOutput {
		dlm = clampInt(dlm, in.MinA, in.MaxA)
		if dlm == in.DLMA {
			return hold(reason + "; already at DLM")
		}
		out.DLMA = dlm
		out.Reason = reason
		return out
	}

	pilot := float64(in.PilotA)
	// A car that draws less than its pilot is limited by itself; lowering the
	// pilot above what it draws changes nothing.
	effectivePilot := math.Min(pilot, carMaxA+modelCarFollowA)
	settle := modelSettle
	if oscillating {
		settle = modelOscillationSettle
	}
	if in.SinceWrite < settle {
		return hold(fmt.Sprintf("settling after DLM change (%ds/%ds)",
			int(in.SinceWrite.Seconds()), int(settle.Seconds())))
	}

	switch {
	case want >= float64(recentMaxPilot)+modelStepDeadbandA:
		if oscillating {
			return hold("GARO is cutting for a cycling household load; not chasing")
		}
		if in.MaxPilotA > 0 && in.PilotA >= in.MaxPilotA {
			return hold("pilot at charger maximum")
		}
		if carMaxA < pilot-modelCarFollowA {
			return hold(fmt.Sprintf("car draws %.1f A below its %d A pilot", pilot-carMaxA, in.PilotA))
		}
		riseA := math.Floor(want) - pilot
		dlm := math.Max(busiestCarA+riseA+modelRaiseLandingRoomA, busiestFreeA+garoRaiseRoomA)
		target := int(math.Ceil(dlm))
		if expected := float64(target) - modelRaiseLandingRoomA - busiestCarA; expected > riseA+1 {
			return hold(fmt.Sprintf(
				"raise blocked: household on a phase without the car (%.1f A) would let GARO raise %.0f A instead of %.0f A",
				busiestFreeA, expected, riseA))
		}
		if target <= in.DLMA {
			return hold("waiting for GARO to raise")
		}
		return write(target, fmt.Sprintf("raise pilot %d -> ~%.0f A", in.PilotA, math.Floor(want)))

	case want <= effectivePilot-modelStepDeadbandA:
		if in.PilotA <= in.MinA {
			return hold("at the 6 A floor; the hard stop handles the rest")
		}
		// GARO cuts about 1 A per update once a phase is ~1 A over DLM. If
		// the previous step did not trigger a cut, go one further.
		dlm := min(int(math.Floor(busiestA))-1, in.DLMA-1)
		m.lastLower = in.Now
		m.pilotAtLower = in.PilotA
		return write(dlm,
			fmt.Sprintf("lower pilot %d A by one step (wanted %.1f A)", in.PilotA, want))

	default:
		if oscillating {
			return hold("GARO is cutting for a cycling household load; holding DLM")
		}
		position := int(math.Ceil(busiestA + modelHoldRoomA))
		minRoom := float64(in.DLMA) - busiestA
		switch {
		case !m.lastLower.IsZero() && in.PilotA < m.pilotAtLower && in.DLMA < position-1:
			// After a cut the model asked for, DLM sits at the cut edge;
			// move it up so the next household increase does not trigger
			// another. After a cut GARO made by itself, DLM is left alone.
			m.lastLower = time.Time{}
			return write(position, "move DLM up into the hold position (burst room)")
		case in.DLMA > position+1 && minRoom >= garoRaiseRoomA:
			return write(position, "move DLM down to the hold position (stop an unwanted GARO raise)")
		default:
			return hold("on target")
		}
	}
}
