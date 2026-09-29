package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

const chargingThresholdA = 2.0

func controlRefreshProblem(refresh GaroMeterRefreshResult, s GaroCacheSnapshot) string {
	parts := make([]string, 0, 3)
	if !refresh.Central100OK {
		parts = append(parts, "CENTRAL100 refresh failed")
	}
	if !refresh.Central101OK {
		parts = append(parts, "CENTRAL101 refresh failed")
	}
	if !s.LBValid {
		parts = append(parts, "DLM configuration unavailable")
	} else if s.LBError != "" {
		parts = append(parts, "DLM configuration refresh failed")
	} else if s.LBStale {
		parts = append(parts, "DLM configuration stale")
	}
	return strings.Join(parts, ", ")
}

func mergeGaroDiagnostics(s *ControllerSnapshot, g GaroCacheSnapshot) {
	s.Central100Valid = g.Central100Valid
	s.Central100AgeSeconds = g.Central100AgeSeconds
	s.Central100Stale = g.Central100Stale
	s.Central100Error = g.Central100Error
	if g.Central100Valid {
		a, b, d := g.Central100.CurrentsA()
		s.Central100Phase1A = a
		s.Central100Phase2A = b
		s.Central100Phase3A = d
	}

	s.Central101Valid = g.Central101Valid
	s.Central101AgeSeconds = g.Central101AgeSeconds
	s.Central101Stale = g.Central101Stale
	s.Central101Error = g.Central101Error
	if g.Central101Valid {
		a, b, d := g.Central101.CurrentsA()
		s.Central101Phase1A = a
		s.Central101Phase2A = b
		s.Central101Phase3A = d
		s.Charging = g.Central101.MaxCurrentA() >= chargingThresholdA
		s.PhaseMode = phaseMode(g.Central101)
	}

	s.DLMConfigValid = g.LBValid
	s.DLMConfigAgeSeconds = g.LBAgeSeconds
	s.DLMConfigStale = g.LBStale
	s.DLMConfigError = g.LBError
	if g.LBValid {
		s.DLMCurrentA = g.LoadBalancingFuse
	}

	if g.LBValid && g.Central100Valid {
		s.DLMHeadroomA = float64(g.LoadBalancingFuse) - g.Central100.MaxCurrentA()
	}

	s.PilotValid = g.PilotValid
	s.PilotAgeSeconds = g.PilotAgeSeconds
	s.PilotStale = g.PilotStale
	s.PilotError = g.PilotError
	if g.PilotValid {
		s.PilotLevels = append([]GaroPilotLevel(nil), g.PilotLevels...)
	}
}

type ControllerSnapshot struct {
	State  string `json:"state"`
	Source string `json:"source"`

	Charging    bool   `json:"charging"`
	EnergyValid bool   `json:"energy_valid"`
	PhaseMode   string `json:"phase_mode,omitempty"`

	PilotLevels     []GaroPilotLevel `json:"pilot_levels,omitempty"`
	PilotValid      bool             `json:"pilot_valid"`
	PilotAgeSeconds int64            `json:"pilot_age_seconds,omitempty"`
	PilotStale      bool             `json:"pilot_stale"`
	PilotError      string           `json:"pilot_error,omitempty"`

	HourEnergyKWh         float64 `json:"hour_energy_kwh"`
	RemainingEnergyKWh    float64 `json:"remaining_energy_kwh"`
	ExpectedHourEnergyKWh float64 `json:"expected_hour_energy_kwh"`
	EnergyPacingErrorKWh  float64 `json:"energy_pacing_error_kwh"`

	GridPowerW            float64 `json:"grid_power_w"`
	BaseTargetPowerW      float64 `json:"base_target_power_w"`
	PacingCorrectionW     float64 `json:"pacing_correction_w"`
	PacingTargetPowerW    float64 `json:"pacing_target_power_w"`
	HardBudgetCeilingW    float64 `json:"hard_budget_ceiling_w"`
	EffectiveTargetPowerW float64 `json:"effective_target_power_w"`
	AllowedAveragePowerW  float64 `json:"allowed_average_power_w"`
	PowerHeadroomW        float64 `json:"power_headroom_w"`

	SecondsRemaining int    `json:"seconds_remaining"`
	ReferenceTime    string `json:"reference_time,omitempty"`

	DLMCurrentA         int     `json:"dlm_current_a"`
	DLMHeadroomA        float64 `json:"dlm_headroom_a"`
	DLMConfigValid      bool    `json:"dlm_config_valid"`
	DLMConfigAgeSeconds int64   `json:"dlm_config_age_seconds,omitempty"`
	DLMConfigStale      bool    `json:"dlm_config_stale"`
	DLMConfigError      string  `json:"dlm_config_error,omitempty"`

	CalculatedRequiredIncreaseA float64 `json:"calculated_required_increase_a,omitempty"`
	CalculatedDLMTargetA        int     `json:"calculated_dlm_target_a,omitempty"`

	Central100Phase1A    float64 `json:"central100_phase1_a"`
	Central100Phase2A    float64 `json:"central100_phase2_a"`
	Central100Phase3A    float64 `json:"central100_phase3_a"`
	Central100Valid      bool    `json:"central100_valid"`
	Central100AgeSeconds int64   `json:"central100_age_seconds,omitempty"`
	Central100Stale      bool    `json:"central100_stale"`
	Central100Error      string  `json:"central100_error,omitempty"`

	Central101Phase1A    float64 `json:"central101_phase1_a"`
	Central101Phase2A    float64 `json:"central101_phase2_a"`
	Central101Phase3A    float64 `json:"central101_phase3_a"`
	Central101Valid      bool    `json:"central101_valid"`
	Central101AgeSeconds int64   `json:"central101_age_seconds,omitempty"`
	Central101Stale      bool    `json:"central101_stale"`
	Central101Error      string  `json:"central101_error,omitempty"`

	LastAdjustmentAgeSeconds int64 `json:"last_adjustment_age_seconds"`

	Decision string `json:"decision,omitempty"`
	LastRun  string `json:"last_run,omitempty"`
	Error    string `json:"error,omitempty"`
}

type EnergyController struct {
	garo      *GaroClient
	garoCache *GaroCache
	tibber    *TibberClient
	getConfig func() Config

	snapshotMu sync.RWMutex
	snapshot   ControllerSnapshot

	idleSince   time.Time
	safeApplied bool

	adjustmentMu        sync.Mutex
	lastAdjustment      time.Time
	lastAdjustmentDelta int
	manualHoldUntil     time.Time

	fallbackValid         bool
	fallbackHour          time.Time
	fallbackEnergyKWh     float64
	fallbackLast          time.Time
	fallbackReferenceTime time.Time
}

func NewEnergyController(
	garo *GaroClient,
	garoCache *GaroCache,
	tibber *TibberClient,
	getConfig func() Config,
) *EnergyController {
	return &EnergyController{
		garo:      garo,
		garoCache: garoCache,
		tibber:    tibber,
		getConfig: getConfig,
	}
}

func (c *EnergyController) Snapshot() ControllerSnapshot {
	c.snapshotMu.RLock()
	defer c.snapshotMu.RUnlock()

	s := c.snapshot
	s.PilotLevels = append([]GaroPilotLevel(nil), c.snapshot.PilotLevels...)
	return s
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

	if err := c.garo.SetLoadBalancingFuse(wanted); err != nil {
		return err
	}

	c.garoCache.NoteLoadBalancingFuse(wanted)
	return nil
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
	central100Fresh bool,
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

	// CENTRAL100 fallback is only advanced from a value refreshed in this
	// control cycle. Cached/stale current remains available for display only.
	if !central100Fresh {
		return "none", 0, 0, time.Time{}, false
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

		// Once the charger has been idle long enough for the safe current to
		// be restored, only wake the control loop every 30 seconds. The deep-idle
		// tick polls CENTRAL101 only, so charging detection remains available
		// without continuously running the full metering/pacing calculation.
		if cfg.Enabled && cfg.Mode == "automatic" && c.safeApplied {
			interval = garoDeepIdlePollInterval
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

func (c *EnergyController) noteAdjustment(now time.Time, delta int) {
	c.adjustmentMu.Lock()
	defer c.adjustmentMu.Unlock()

	c.lastAdjustment = now
	c.lastAdjustmentDelta = delta
}

// NoteManualAction records an operator-initiated GARO change. Automatic DLM
// writes are held for at least one normal dwell/control interval so a manual
// safe-current or charge-mode action cannot be overwritten by the next tick.
func (c *EnergyController) NoteManualAction(delta int) {
	now := time.Now()
	cfg := c.getConfig()

	hold := time.Duration(cfg.ControlTuning.NormalDwellSeconds) * time.Second
	controlInterval := time.Duration(cfg.ControlIntervalSeconds) * time.Second
	if controlInterval < 5*time.Second {
		controlInterval = 5 * time.Second
	}
	if hold < controlInterval {
		hold = controlInterval
	}

	c.adjustmentMu.Lock()
	defer c.adjustmentMu.Unlock()

	c.lastAdjustment = now
	c.lastAdjustmentDelta = delta
	c.manualHoldUntil = now.Add(hold)
}

func (c *EnergyController) adjustmentAge(now time.Time) time.Duration {
	c.adjustmentMu.Lock()
	last := c.lastAdjustment
	c.adjustmentMu.Unlock()

	if last.IsZero() {
		return 365 * 24 * time.Hour
	}

	age := now.Sub(last)
	if age < 0 {
		return 0
	}

	return age
}

func (c *EnergyController) manualHoldRemaining(now time.Time) time.Duration {
	c.adjustmentMu.Lock()
	until := c.manualHoldUntil
	c.adjustmentMu.Unlock()

	if until.IsZero() || !now.Before(until) {
		return 0
	}
	return until.Sub(now)
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
	unusedDLMHeadroomA float64,
	maxStepA int,
) (stepA int, requiredIncreaseA float64) {
	usableHeadroomW := headroomW - reserveW
	if usableHeadroomW <= 0 || wattsPerAmp <= 0 {
		return 0, 0
	}

	// Convert the remaining power budget to the amount of additional charging
	// current that would consume it. Existing unused DLM authority already
	// contributes toward that requirement, so only add the difference.
	requiredIncreaseA = usableHeadroomW / wattsPerAmp
	if unusedDLMHeadroomA < 0 {
		unusedDLMHeadroomA = 0
	}

	additionalDLMNeededA := requiredIncreaseA - unusedDLMHeadroomA
	if additionalDLMNeededA <= 0 {
		return 0, requiredIncreaseA
	}

	stepA = int(math.Ceil(additionalDLMNeededA))
	return clampInt(stepA, 1, maxStepA), requiredIncreaseA
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

	// The hourly limit is both a nominal power target and a hard energy
	// ceiling. Pace toward the nominal target over a fixed recovery horizon
	// instead of trying to spend every unused Wh before the top of the hour.
	// This prevents the target from accelerating sharply as secondsRemaining
	// approaches zero.
	baseTargetPowerW := cfg.HourlyLimitKWh * 1000.0
	elapsedSeconds := 3600 - secondsRemaining
	if elapsedSeconds < 0 {
		elapsedSeconds = 0
	}
	if elapsedSeconds > 3600 {
		elapsedSeconds = 3600
	}

	expectedHourEnergyKWh :=
		cfg.HourlyLimitKWh * float64(elapsedSeconds) / 3600.0
	energyPacingErrorKWh := expectedHourEnergyKWh - hourEnergy

	pacingCorrectionW :=
		energyPacingErrorKWh * 3600000.0 /
			float64(cfg.ControlTuning.PacingHorizonSeconds)
	maxPacingAdjustmentW := cfg.ControlTuning.MaxPacingAdjustmentW
	if pacingCorrectionW > maxPacingAdjustmentW {
		pacingCorrectionW = maxPacingAdjustmentW
	} else if pacingCorrectionW < -maxPacingAdjustmentW {
		pacingCorrectionW = -maxPacingAdjustmentW
	}

	pacingTargetPowerW := baseTargetPowerW + pacingCorrectionW
	if pacingTargetPowerW < 0 {
		pacingTargetPowerW = 0
	}

	hardBudgetCeilingW :=
		remainingEnergy * 3600000.0 /
			float64(secondsRemaining)

	effectiveTargetPowerW := math.Min(
		pacingTargetPowerW,
		hardBudgetCeilingW,
	)
	if effectiveTargetPowerW < 0 {
		effectiveTargetPowerW = 0
	}

	s.ExpectedHourEnergyKWh = expectedHourEnergyKWh
	s.EnergyPacingErrorKWh = energyPacingErrorKWh
	s.BaseTargetPowerW = baseTargetPowerW
	s.PacingCorrectionW = pacingCorrectionW
	s.PacingTargetPowerW = pacingTargetPowerW
	s.HardBudgetCeilingW = hardBudgetCeilingW
	s.EffectiveTargetPowerW = effectiveTargetPowerW

	// Keep AllowedAveragePowerW as a compatibility field for existing UI and
	// controller logic. It now represents the effective target selected by the
	// pacing algorithm, not the raw remaining-energy/remaining-time ceiling.
	s.AllowedAveragePowerW = effectiveTargetPowerW
	s.PowerHeadroomW = effectiveTargetPowerW - gridPower
}

func clearEnergyDiagnostics(s *ControllerSnapshot) {
	s.Source = ""
	s.EnergyValid = false
	s.HourEnergyKWh = 0
	s.RemainingEnergyKWh = 0
	s.ExpectedHourEnergyKWh = 0
	s.EnergyPacingErrorKWh = 0
	s.GridPowerW = 0
	s.BaseTargetPowerW = 0
	s.PacingCorrectionW = 0
	s.PacingTargetPowerW = 0
	s.HardBudgetCeilingW = 0
	s.EffectiveTargetPowerW = 0
	s.AllowedAveragePowerW = 0
	s.PowerHeadroomW = 0
	s.SecondsRemaining = 0
	s.ReferenceTime = ""
	s.CalculatedRequiredIncreaseA = 0
	s.CalculatedDLMTargetA = 0
}

func (c *EnergyController) tick() {
	now := time.Now()
	cfg := c.getConfig()

	// Start with the previous snapshot so transient GARO failures do not erase
	// last-known-good values. Freshness flags below determine whether they may
	// be used for control.
	s := c.Snapshot()
	s.LastRun = now.Format(time.RFC3339)
	s.State = "idle"
	s.Decision = ""
	s.Error = ""
	s.CalculatedRequiredIncreaseA = 0
	s.CalculatedDLMTargetA = 0

	age := c.adjustmentAge(now)
	if age < 365*24*time.Hour {
		s.LastAdjustmentAgeSeconds = int64(age.Seconds())
	}

	// After the charger has remained idle long enough for SafeCurrentA to be
	// restored, use a lightweight monitoring path. CENTRAL101 alone is enough
	// to tell whether charging has resumed. Avoid refreshing CENTRAL100 and
	// avoid running the hourly energy/pacing calculations until then.
	if cfg.Enabled && cfg.Mode == "automatic" && c.safeApplied {
		c.garoCache.SetDeepIdle(true)
		central101Fresh := c.garoCache.RefreshCentral101(c.garo)
		garoState := c.garoCache.Snapshot(now)
		mergeGaroDiagnostics(&s, garoState)
		s.Error = garoErrorSummary(garoState)
		s.State = "idle"
		clearEnergyDiagnostics(&s)

		if !central101Fresh {
			s.Decision =
				"deep idle; CENTRAL101 refresh failed; retaining safe current"
			c.setSnapshot(s)
			return
		}

		if !s.Charging {
			s.Decision = fmt.Sprintf(
				"deep idle; safe current %d A; charging check every %ds",
				cfg.SafeCurrentA,
				int(garoDeepIdlePollInterval.Seconds()),
			)
			c.setSnapshot(s)
			return
		}

		// Charging was detected by the lightweight poll. Clear the deep-idle
		// state and immediately continue through a complete control tick so the
		// first pacing/DLM decision is made without another 30-second delay.
		c.idleSince = time.Time{}
		c.safeApplied = false
		c.garoCache.SetDeepIdle(false)

		// The deep-idle fast loop refreshes DLM configuration only once per
		// minute. Refresh it synchronously here so the first active control
		// decision never waits for the background fast loop to catch up.
		c.garoCache.RefreshLoadBalancing(c.garo)
	}

	refresh := c.garoCache.RefreshMeters(c.garo)
	garoState := c.garoCache.Snapshot(now)
	mergeGaroDiagnostics(&s, garoState)
	s.Error = garoErrorSummary(garoState)

	central100 := garoState.Central100
	currentLimit := garoState.LoadBalancingFuse

	source,
		hourEnergy,
		gridPower,
		referenceTime,
		valid := c.getEnergySource(
		now,
		cfg,
		central100,
		refresh.Central100OK,
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
		c.garoCache.SetDeepIdle(false)
		// Force a fresh idle/safe-current transition if automatic control is
		// enabled again later; another mode may have changed the DLM setting.
		c.safeApplied = false
		s.State = "disabled"
		s.Decision = "controller disabled"
		c.setSnapshot(s)
		return
	}

	if cfg.Mode != "automatic" {
		c.garoCache.SetDeepIdle(false)
		// Do not carry a deep-idle assumption across manual/other modes because
		// those modes can legitimately alter the current limit.
		c.safeApplied = false
		s.State = cfg.Mode
		s.Decision = "automatic controller not active"
		c.setSnapshot(s)
		return
	}

	if problem := controlRefreshProblem(refresh, garoState); problem != "" {
		s.State = "automatic-hold"
		s.Decision = "hold: " + problem + "; retaining last GARO values"
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
						c.noteAdjustment(now, cfg.SafeCurrentA-currentLimit)
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
					c.garoCache.SetDeepIdle(true)
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
	c.garoCache.SetDeepIdle(false)

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
				c.noteAdjustment(now, cfg.SafeCurrentA-currentLimit)
			}
			s.DLMCurrentA = cfg.SafeCurrentA
		}

		c.setSnapshot(s)
		return
	}

	s.State = "automatic"

	if remaining := c.manualHoldRemaining(now); remaining > 0 {
		s.Decision = fmt.Sprintf(
			"hold: manual action; %ds before automatic DLM adjustment",
			int(math.Ceil(remaining.Seconds())),
		)
		c.setSnapshot(s)
		return
	}

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
		if unusedDLMHeadroomA < 0 {
			unusedDLMHeadroomA = 0
		}
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
			// Far below the energy target, calculate how much additional charging
			// current the power budget can support. Existing unused DLM headroom is
			// subtracted from that requirement, but it no longer blocks an increase
			// by itself. This lets us raise the DLM ceiling while GARO is still
			// slowly ramping the pilot, without blindly stacking the full max step.
			step, requiredIncreaseA := calculatedUpStep(
				headroomW,
				wattsPerAmp,
				cfg.ControlTuning.CalculatedReserveW,
				unusedDLMHeadroomA,
				maxCalculatedStepA,
			)

			s.CalculatedRequiredIncreaseA = requiredIncreaseA

			desiredTarget := currentLimit + step
			if desiredTarget > cfg.MaximumCurrentA {
				desiredTarget = cfg.MaximumCurrentA
			}
			actualStep := desiredTarget - currentLimit
			s.CalculatedDLMTargetA = desiredTarget

			if actualStep <= 0 {
				s.CalculatedDLMTargetA = currentLimit
				s.Decision = fmt.Sprintf(
					"hold calculated increase: %.1f A DLM headroom already covers %.1f A calculated requirement",
					unusedDLMHeadroomA,
					requiredIncreaseA,
				)
			} else if adjustmentAge < normalDwell {
				s.Decision = fmt.Sprintf(
					"hold calculated target %d A: need +%d A after %.1f A existing DLM headroom; waiting %ds/%ds",
					s.CalculatedDLMTargetA,
					actualStep,
					unusedDLMHeadroomA,
					int(adjustmentAge.Seconds()),
					int(normalDwell.Seconds()),
				)
			} else {
				target = desiredTarget
				s.Decision = fmt.Sprintf(
					"calculated target %d A: %.0f W headroom -> %.1f A required, %.1f A already available, +%d A (%s)",
					target,
					headroomW,
					requiredIncreaseA,
					unusedDLMHeadroomA,
					actualStep,
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

	if err := c.ensureCurrent(currentLimit, target); err != nil {
		s.Error = err.Error()
		s.Decision += "; GARO update failed"
		c.setSnapshot(s)
		return
	}

	c.noteAdjustment(now, target-currentLimit)

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
