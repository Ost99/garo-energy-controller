package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sync"
	"time"
)

const (
	chargingThresholdA = 2.0

	// Conservative fallback estimate. CENTRAL100 measures total site
	// consumption, not net grid import.
	fallbackVoltage = 230.0
)

type GaroMeterInfo struct {
	Phase1Current float64 `json:"phase1Current"`
	Phase2Current float64 `json:"phase2Current"`
	Phase3Current float64 `json:"phase3Current"`
}

func (m GaroMeterInfo) CurrentsA() (float64, float64, float64) {
	// This GARO firmware reports meter currents in 0.1 A.
	return m.Phase1Current / 10.0,
		m.Phase2Current / 10.0,
		m.Phase3Current / 10.0
}

func (m GaroMeterInfo) MaxCurrentA() float64 {
	a, b, c := m.CurrentsA()

	return math.Max(
		math.Abs(a),
		math.Max(math.Abs(b), math.Abs(c)),
	)
}

func (m GaroMeterInfo) ConservativePowerW() float64 {
	a, b, c := m.CurrentsA()

	return fallbackVoltage *
		(math.Abs(a) + math.Abs(b) + math.Abs(c))
}

func (g *GaroClient) GetMeterInfo(name string) (GaroMeterInfo, error) {
	var meter GaroMeterInfo

	resp, err := g.client.Get(
		g.baseURL + "/meterinfo/" + name,
	)
	if err != nil {
		return meter, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)

		return meter, fmt.Errorf(
			"GET meterinfo/%s: HTTP %d: %s",
			name,
			resp.StatusCode,
			string(body),
		)
	}

	if err := json.NewDecoder(resp.Body).Decode(&meter); err != nil {
		return meter, err
	}

	return meter, nil
}

func (g *GaroClient) GetLoadBalancingFuse() (int, error) {
	cfg, err := g.GetLBConfig()
	if err != nil {
		return 0, err
	}

	value := rawInt(cfg["loadBalancingFuse"])
	if value == nil {
		return 0, fmt.Errorf(
			"loadBalancingFuse missing from GARO configuration",
		)
	}

	return *value, nil
}

type ControllerSnapshot struct {
	State  string `json:"state"`
	Source string `json:"source"`

	Charging    bool   `json:"charging"`
	EnergyValid bool   `json:"energy_valid"`
	PhaseMode   string `json:"phase_mode,omitempty"`

	HourEnergyKWh      float64 `json:"hour_energy_kwh"`
	RemainingEnergyKWh float64 `json:"remaining_energy_kwh"`

	GridPowerW           float64 `json:"grid_power_w"`
	AllowedAveragePowerW float64 `json:"allowed_average_power_w"`
	PowerHeadroomW       float64 `json:"power_headroom_w"`

	SecondsRemaining int    `json:"seconds_remaining"`
	ReferenceTime    string `json:"reference_time,omitempty"`

	DLMCurrentA  int     `json:"dlm_current_a"`
	DLMHeadroomA float64 `json:"dlm_headroom_a"`

	Central100Phase1A float64 `json:"central100_phase1_a"`
	Central100Phase2A float64 `json:"central100_phase2_a"`
	Central100Phase3A float64 `json:"central100_phase3_a"`

	Central101Phase1A float64 `json:"central101_phase1_a"`
	Central101Phase2A float64 `json:"central101_phase2_a"`
	Central101Phase3A float64 `json:"central101_phase3_a"`

	LastAdjustmentAgeSeconds int64 `json:"last_adjustment_age_seconds"`

	Decision string `json:"decision,omitempty"`
	LastRun  string `json:"last_run,omitempty"`
	Error    string `json:"error,omitempty"`
}

type EnergyController struct {
	garo      *GaroClient
	tibber    *TibberClient
	getConfig func() Config

	snapshotMu sync.RWMutex
	snapshot   ControllerSnapshot

	idleSince   time.Time
	safeApplied bool

	lastAdjustment      time.Time
	lastAdjustmentDelta int

	fallbackValid         bool
	fallbackHour          time.Time
	fallbackEnergyKWh     float64
	fallbackLast          time.Time
	fallbackReferenceTime time.Time
}

func NewEnergyController(
	garo *GaroClient,
	tibber *TibberClient,
	getConfig func() Config,
) *EnergyController {
	return &EnergyController{
		garo:      garo,
		tibber:    tibber,
		getConfig: getConfig,
	}
}

func (c *EnergyController) Snapshot() ControllerSnapshot {
	c.snapshotMu.RLock()
	defer c.snapshotMu.RUnlock()

	return c.snapshot
}

func (c *EnergyController) setSnapshot(s ControllerSnapshot) {
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()

	c.snapshot = s
}

func hourStart(t time.Time) time.Time {
	year, month, day := t.Date()

	return time.Date(
		year,
		month,
		day,
		t.Hour(),
		0,
		0,
		0,
		t.Location(),
	)
}

func nextHour(t time.Time) time.Time {
	return hourStart(t).Add(time.Hour)
}

func (c *EnergyController) ensureCurrent(
	current int,
	wanted int,
) error {
	if current == wanted {
		return nil
	}

	return c.garo.SetLoadBalancingFuse(wanted)
}

func tibberReferenceTime(t TibberSnapshot) (time.Time, bool) {
	if t.MeasurementTimestamp == "" {
		return time.Time{}, false
	}

	ts, err := time.Parse(time.RFC3339Nano, t.MeasurementTimestamp)
	if err != nil {
		ts, err = time.Parse(time.RFC3339, t.MeasurementTimestamp)
		if err != nil {
			return time.Time{}, false
		}
	}

	age := t.AgeSeconds
	if age < 0 {
		age = 0
	}

	return ts.Add(time.Duration(age) * time.Second), true
}

func (c *EnergyController) getEnergySource(
	now time.Time,
	cfg Config,
	central100 GaroMeterInfo,
) (
	source string,
	energyKWh float64,
	powerW float64,
	referenceTime time.Time,
	valid bool,
) {
	t := c.tibber.Snapshot()

	tibberFresh :=
		t.Connected &&
			t.LastUpdate != "" &&
			t.AgeSeconds <= int64(cfg.TibberTimeoutSeconds)

	if tibberFresh {
		powerW = math.Max(t.PowerW, 0)

		referenceTime, ok := tibberReferenceTime(t)
		if !ok {
			// The Tibber measurement timestamp is the preferred clock.
			// Falling back to the local clock keeps the controller usable if
			// Tibber ever omits or changes the timestamp field.
			referenceTime = now
		}

		c.fallbackValid = true
		c.fallbackHour = hourStart(referenceTime)
		c.fallbackEnergyKWh =
			t.AccumulatedConsumptionLastHourKWh
		c.fallbackLast = now
		c.fallbackReferenceTime = referenceTime

		return "tibber",
			t.AccumulatedConsumptionLastHourKWh,
			powerW,
			referenceTime,
			true
	}

	if !c.fallbackValid {
		return "none", 0, 0, time.Time{}, false
	}

	// If the controller stopped running for too long, the conservative
	// fallback accumulator is no longer trustworthy.
	maxGap :=
		time.Duration(cfg.ControlIntervalSeconds*2+10) *
			time.Second

	elapsed := now.Sub(c.fallbackLast)
	if elapsed < 0 || elapsed > maxGap {
		c.fallbackValid = false
		return "none", 0, 0, time.Time{}, false
	}

	powerW = central100.ConservativePowerW()
	referenceTime = c.fallbackReferenceTime.Add(elapsed)

	oldReferenceTime := c.fallbackReferenceTime
	newHour := hourStart(referenceTime)

	if !c.fallbackHour.Equal(newHour) {
		// At most one boundary can be crossed because elapsed is bounded by
		// maxGap. Only integrate the part that belongs to the new hour.
		boundary := nextHour(oldReferenceTime)
		afterBoundary := referenceTime.Sub(boundary)
		if afterBoundary < 0 {
			afterBoundary = 0
		}

		c.fallbackHour = newHour
		c.fallbackEnergyKWh =
			powerW * afterBoundary.Hours() / 1000.0
	} else if elapsed > 0 {
		c.fallbackEnergyKWh +=
			powerW * elapsed.Hours() / 1000.0
	}

	c.fallbackLast = now
	c.fallbackReferenceTime = referenceTime

	return "central100",
		c.fallbackEnergyKWh,
		powerW,
		referenceTime,
		true
}

func (c *EnergyController) Run(ctx context.Context) {
	for {
		c.tick()

		cfg := c.getConfig()

		interval :=
			time.Duration(cfg.ControlIntervalSeconds) *
				time.Second

		if interval < 5*time.Second {
			interval = 5 * time.Second
		}

		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()
			return

		case <-timer.C:
		}
	}
}

func phaseMode(m GaroMeterInfo) string {
	a, b, d := m.CurrentsA()
	currents := []float64{
		math.Abs(a),
		math.Abs(b),
		math.Abs(d),
	}

	active := make([]float64, 0, 3)
	for _, current := range currents {
		if current >= chargingThresholdA {
			active = append(active, current)
		}
	}

	switch len(active) {
	case 0:
		return "idle"
	case 1:
		return "one-phase"
	case 2:
		return "mixed"
	}

	minA := active[0]
	maxA := active[0]
	for _, current := range active[1:] {
		minA = math.Min(minA, current)
		maxA = math.Max(maxA, current)
	}

	// A balanced 3-phase car should be close across all three phases.
	// A materially larger phase normally means a simultaneous 1-phase load.
	if maxA-minA > 3.0 {
		return "mixed"
	}

	return "three-phase"
}

func (c *EnergyController) adjustmentAge(now time.Time) time.Duration {
	if c.lastAdjustment.IsZero() {
		return 365 * 24 * time.Hour
	}

	age := now.Sub(c.lastAdjustment)
	if age < 0 {
		return 0
	}

	return age
}

func requiredUpDwell(
	headroomW float64,
	upGateW float64,
	twoAmpThresholdW float64,
	tuning ControlTuningConfig,
) time.Duration {
	switch {
	case headroomW >= twoAmpThresholdW:
		return time.Duration(tuning.NormalDwellSeconds) * time.Second
	case headroomW >= upGateW:
		return time.Duration(tuning.MidUpDwellSeconds) * time.Second
	default:
		return time.Duration(tuning.NearUpDwellSeconds) * time.Second
	}
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func calculatedUpStep(
	headroomW float64,
	wattsPerAmp float64,
	reserveW float64,
	maxStepA int,
) int {
	usableHeadroomW := headroomW - reserveW
	if usableHeadroomW <= 0 || wattsPerAmp <= 0 {
		return 1
	}

	step := int(math.Floor(usableHeadroomW / wattsPerAmp))
	return clampInt(step, 1, maxStepA)
}

func populateMeterDiagnostics(
	s *ControllerSnapshot,
	central100 GaroMeterInfo,
	central101 GaroMeterInfo,
	currentLimit int,
) {
	c100a, c100b, c100c := central100.CurrentsA()
	c101a, c101b, c101c := central101.CurrentsA()

	s.Central100Phase1A = c100a
	s.Central100Phase2A = c100b
	s.Central100Phase3A = c100c

	s.Central101Phase1A = c101a
	s.Central101Phase2A = c101b
	s.Central101Phase3A = c101c

	s.DLMCurrentA = currentLimit
	s.DLMHeadroomA =
		float64(currentLimit) - central100.MaxCurrentA()

	s.Charging = central101.MaxCurrentA() >= chargingThresholdA
	s.PhaseMode = phaseMode(central101)
}

func populateEnergyDiagnostics(
	s *ControllerSnapshot,
	cfg Config,
	source string,
	hourEnergy float64,
	gridPower float64,
	referenceTime time.Time,
	valid bool,
) {
	s.Source = source
	s.EnergyValid = valid
	s.GridPowerW = gridPower

	if !valid {
		return
	}

	s.HourEnergyKWh = hourEnergy
	s.ReferenceTime = referenceTime.Format(time.RFC3339)

	remainingEnergy :=
		cfg.HourlyLimitKWh - hourEnergy
	if remainingEnergy < 0 {
		remainingEnergy = 0
	}

	s.RemainingEnergyKWh = remainingEnergy

	secondsRemaining :=
		int(nextHour(referenceTime).Sub(referenceTime).Seconds())
	if secondsRemaining < 1 {
		secondsRemaining = 1
	}

	s.SecondsRemaining = secondsRemaining
	s.AllowedAveragePowerW =
		remainingEnergy * 3600000.0 /
			float64(secondsRemaining)

	s.PowerHeadroomW =
		s.AllowedAveragePowerW - gridPower
}

func (c *EnergyController) tick() {
	now := time.Now()
	cfg := c.getConfig()

	s := ControllerSnapshot{
		LastRun: now.Format(time.RFC3339),
		State:   "idle",
		Source:  "none",
	}

	age := c.adjustmentAge(now)
	if age < 365*24*time.Hour {
		s.LastAdjustmentAgeSeconds = int64(age.Seconds())
	}

	// Read GARO regardless of controller mode so the status page remains
	// useful in disabled, safe and manual modes.
	central100, err := c.garo.GetMeterInfo("CENTRAL100")
	if err != nil {
		s.State = "error"
		s.Error = err.Error()
		s.Decision = "could not read CENTRAL100"
		c.setSnapshot(s)
		return
	}

	central101, err := c.garo.GetMeterInfo("CENTRAL101")
	if err != nil {
		s.State = "error"
		s.Error = err.Error()
		s.Decision = "could not read CENTRAL101"
		c.setSnapshot(s)
		return
	}

	currentLimit, err := c.garo.GetLoadBalancingFuse()
	if err != nil {
		s.State = "error"
		s.Error = err.Error()
		s.Decision = "could not read DLM current limit"
		c.setSnapshot(s)
		return
	}

	populateMeterDiagnostics(
		&s,
		central100,
		central101,
		currentLimit,
	)

	source,
		hourEnergy,
		gridPower,
		referenceTime,
		valid := c.getEnergySource(
		now,
		cfg,
		central100,
	)

	populateEnergyDiagnostics(
		&s,
		cfg,
		source,
		hourEnergy,
		gridPower,
		referenceTime,
		valid,
	)

	if !cfg.Enabled {
		s.State = "disabled"
		s.Decision = "controller disabled"
		c.setSnapshot(s)
		return
	}

	if cfg.Mode != "automatic" {
		s.State = cfg.Mode
		s.Decision = "automatic controller not active"
		c.setSnapshot(s)
		return
	}

	// Session idle handling.
	if !s.Charging {
		s.State = "idle"

		if c.idleSince.IsZero() {
			c.idleSince = now
		}

		idleFor := now.Sub(c.idleSince)

		if idleFor >=
			time.Duration(cfg.IdleTimeoutSeconds)*time.Second {

			if !c.safeApplied {
				if err := c.ensureCurrent(
					currentLimit,
					cfg.SafeCurrentA,
				); err != nil {
					s.Error = err.Error()
					s.Decision =
						"failed to restore safe current"
				} else {
					if currentLimit != cfg.SafeCurrentA {
						c.lastAdjustment = now
						c.lastAdjustmentDelta =
							cfg.SafeCurrentA - currentLimit
					}

					s.DLMCurrentA = cfg.SafeCurrentA
					s.DLMHeadroomA =
						float64(cfg.SafeCurrentA) -
							central100.MaxCurrentA()
					s.Decision = fmt.Sprintf(
						"session idle; restored %d A",
						cfg.SafeCurrentA,
					)
					c.safeApplied = true
				}
			} else {
				s.Decision = "safe current already restored"
			}
		} else {
			s.Decision = fmt.Sprintf(
				"idle for %ds",
				int(idleFor.Seconds()),
			)
		}

		c.setSnapshot(s)
		return
	}

	c.idleSince = time.Time{}
	c.safeApplied = false

	if !valid {
		s.State = "fallback-safe"
		s.Decision =
			"no trustworthy hourly energy value; using safe current"

		if err := c.ensureCurrent(
			currentLimit,
			cfg.SafeCurrentA,
		); err != nil {
			s.Error = err.Error()
		} else {
			if currentLimit != cfg.SafeCurrentA {
				c.lastAdjustment = now
				c.lastAdjustmentDelta =
					cfg.SafeCurrentA - currentLimit
			}
			s.DLMCurrentA = cfg.SafeCurrentA
		}

		c.setSnapshot(s)
		return
	}

	s.State = "automatic"

	remainingEnergy := s.RemainingEnergyKWh
	allowedPower := s.AllowedAveragePowerW
	headroomW := s.PowerHeadroomW
	maxSiteCurrentA := central100.MaxCurrentA()
	adjustmentAge := c.adjustmentAge(now)

	target := currentLimit
	step := 0

	// If the hourly budget is exhausted, move directly to the configured
	// minimum. The future hard-stop/availability action remains separate.
	if remainingEnergy <= 0 {
		target = cfg.MinimumCurrentA
		s.Decision = "hourly budget exhausted"

	} else if headroomW < -cfg.ControlTuning.DownDeadbandW {
		normalDwell := time.Duration(cfg.ControlTuning.NormalDwellSeconds) * time.Second

		// Downward control is faster than upward control, but still waits
		// long enough for GARO to react before stacking another reduction.
		if adjustmentAge < normalDwell {
			s.Decision = fmt.Sprintf(
				"hold: %.0f W over target; waiting for previous DLM change (%ds/%ds)",
				-headroomW,
				int(adjustmentAge.Seconds()),
				int(normalDwell.Seconds()),
			)
		} else {
			step = -1
			if headroomW <= -cfg.ControlTuning.UrgentDownW {
				step = -2
			}

			target = currentLimit + step
			s.Decision = fmt.Sprintf(
				"reduce %d A: %.0f W > %.0f W allowed",
				-step,
				gridPower,
				allowedPower,
			)
		}

	} else {
		onePhase := s.PhaseMode == "one-phase"

		upGateW := cfg.ControlTuning.MultiPhaseUpGateW
		twoAmpThresholdW := cfg.ControlTuning.MultiPhaseTwoAmpThresholdW
		calculatedThresholdW := cfg.ControlTuning.MultiPhaseCalculatedThresholdW
		maxCalculatedStepA := cfg.ControlTuning.MultiPhaseMaxCalculatedStepA
		wattsPerAmp := cfg.ControlTuning.MultiPhaseWattsPerAmp

		if onePhase {
			upGateW = cfg.ControlTuning.OnePhaseUpGateW
			twoAmpThresholdW = cfg.ControlTuning.OnePhaseTwoAmpThresholdW
			calculatedThresholdW = cfg.ControlTuning.OnePhaseCalculatedThresholdW
			maxCalculatedStepA = cfg.ControlTuning.OnePhaseMaxCalculatedStepA
			wattsPerAmp = cfg.ControlTuning.OnePhaseWattsPerAmp
		}

		unusedDLMHeadroomA := float64(currentLimit) - maxSiteCurrentA
		normalDwell := time.Duration(cfg.ControlTuning.NormalDwellSeconds) * time.Second

		switch {
		case headroomW < upGateW:
			s.Decision = fmt.Sprintf(
				"within upward band: %.0f W headroom (%s gate %.0f W)",
				headroomW,
				s.PhaseMode,
				upGateW,
			)

		case headroomW > calculatedThresholdW:
			// Far below target: give GARO enough DLM ceiling for its own pilot
			// ramp instead of feeding it one amp at a time. After an upward
			// change, do not stack another calculated jump until GARO has used
			// the outstanding allocation.
			if c.lastAdjustmentDelta > 0 &&
				unusedDLMHeadroomA > cfg.ControlTuning.UnusedDLMHeadroomA {
				s.Decision = fmt.Sprintf(
					"hold calculated increase: GARO still has %.1f A unused DLM headroom",
					unusedDLMHeadroomA,
				)
			} else if adjustmentAge < normalDwell {
				s.Decision = fmt.Sprintf(
					"hold calculated increase: %.0f W headroom; waiting %ds/%ds",
					headroomW,
					int(adjustmentAge.Seconds()),
					int(normalDwell.Seconds()),
				)
			} else {
				step = calculatedUpStep(
					headroomW,
					wattsPerAmp,
					cfg.ControlTuning.CalculatedReserveW,
					maxCalculatedStepA,
				)
				target = currentLimit + step
				s.Decision = fmt.Sprintf(
					"calculated increase %d A: %.0f W headroom, %.0f W reserve, %.0f W/A (%s)",
					step,
					headroomW,
					cfg.ControlTuning.CalculatedReserveW,
					wattsPerAmp,
					s.PhaseMode,
				)
			}

		case unusedDLMHeadroomA > cfg.ControlTuning.UnusedDLMHeadroomA:
			// Nearer the target, do not increase while GARO still has unused
			// current authority from the existing DLM setting.
			s.Decision = fmt.Sprintf(
				"hold increase: GARO still has %.1f A unused DLM headroom",
				unusedDLMHeadroomA,
			)

		default:
			dwell := requiredUpDwell(headroomW, upGateW, twoAmpThresholdW, cfg.ControlTuning)
			if adjustmentAge < dwell {
				s.Decision = fmt.Sprintf(
					"hold increase: %.0f W headroom; waiting %ds/%ds",
					headroomW,
					int(adjustmentAge.Seconds()),
					int(dwell.Seconds()),
				)
			} else {
				step = 1
				if headroomW >= twoAmpThresholdW {
					step = 2
				}

				target = currentLimit + step
				s.Decision = fmt.Sprintf(
					"increase %d A: %.0f W headroom (%s)",
					step,
					headroomW,
					s.PhaseMode,
				)
			}
		}
	}

	if target < cfg.MinimumCurrentA {
		target = cfg.MinimumCurrentA
	}

	if target > cfg.MaximumCurrentA {
		target = cfg.MaximumCurrentA
	}

	if target == currentLimit {
		if currentLimit == cfg.MaximumCurrentA && headroomW > 0 {
			s.Decision += "; at maximum DLM current"
		}

		c.setSnapshot(s)
		return
	}

	if err := c.garo.SetLoadBalancingFuse(target); err != nil {
		s.Error = err.Error()
		s.Decision += "; GARO update failed"
		c.setSnapshot(s)
		return
	}

	c.lastAdjustment = now
	c.lastAdjustmentDelta = target - currentLimit

	s.DLMCurrentA = target
	s.DLMHeadroomA =
		float64(target) - maxSiteCurrentA
	s.LastAdjustmentAgeSeconds = 0

	s.Decision += fmt.Sprintf(
		"; DLM %d -> %d A",
		currentLimit,
		target,
	)

	c.setSnapshot(s)
}
