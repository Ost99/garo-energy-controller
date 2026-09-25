package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	chargingThresholdA = 2.0

	// Conservative fallback estimate. CENTRAL100 measures total site
	// consumption, not net grid import.
	fallbackVoltage = 230.0

	garoStaleAfter       = 60 * time.Second
	garoFastPollInterval = 5 * time.Second
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
	g.mu.Lock()
	defer g.mu.Unlock()

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

type GaroPilotLevel struct {
	SerialNumber int `json:"serial_number"`
	PilotA       int `json:"pilot_a"`
}

type GaroFastInfo struct {
	PilotLevels []GaroPilotLevel
	ChargeMode  string
}

func (g *GaroClient) GetFastInfo() (GaroFastInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	type pilotResponse struct {
		SerialNumber int    `json:"serialNumber"`
		PilotLevel   int    `json:"pilotLevel"`
		Mode         string `json:"mode"`
	}

	var info GaroFastInfo
	levels := make(map[int]int)

	resp, err := g.client.Get(g.baseURL + "/status")
	if err != nil {
		return info, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return info, fmt.Errorf(
			"GET status: HTTP %d: %s",
			resp.StatusCode,
			string(body),
		)
	}

	var master pilotResponse
	if err := json.NewDecoder(resp.Body).Decode(&master); err != nil {
		resp.Body.Close()
		return info, err
	}
	resp.Body.Close()

	if master.SerialNumber != 0 {
		levels[master.SerialNumber] = master.PilotLevel
	}
	info.ChargeMode = master.Mode

	resp, err = g.client.Get(g.baseURL + "/slaves/false")
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return info, fmt.Errorf(
			"GET slaves/false: HTTP %d: %s",
			resp.StatusCode,
			string(body),
		)
	}

	var slaves []pilotResponse
	if err := json.NewDecoder(resp.Body).Decode(&slaves); err != nil {
		return info, err
	}

	for _, slave := range slaves {
		if slave.SerialNumber != 0 {
			levels[slave.SerialNumber] = slave.PilotLevel
		}
	}

	info.PilotLevels = make([]GaroPilotLevel, 0, len(levels))
	for serial, pilot := range levels {
		info.PilotLevels = append(info.PilotLevels, GaroPilotLevel{
			SerialNumber: serial,
			PilotA:       pilot,
		})
	}

	sort.Slice(info.PilotLevels, func(i, j int) bool {
		return info.PilotLevels[i].SerialNumber > info.PilotLevels[j].SerialNumber
	})

	return info, nil
}

type GaroCache struct {
	mu sync.RWMutex

	central100      GaroMeterInfo
	central100Valid bool
	central100At    time.Time
	central100Error string

	central101      GaroMeterInfo
	central101Valid bool
	central101At    time.Time
	central101Error string

	loadBalancingFuse    int
	loadBalancingFuse101 int
	lbValid              bool
	lbAt                 time.Time
	lbError              string

	pilotLevels []GaroPilotLevel
	pilotValid  bool
	pilotAt     time.Time
	pilotError  string

	chargeMode      string
	chargeModeValid bool
	chargeModeAt    time.Time
	chargeModeError string
}

type GaroMeterRefreshResult struct {
	Central100OK bool
	Central101OK bool
}

type GaroCacheSnapshot struct {
	Central100           GaroMeterInfo
	Central100Valid      bool
	Central100AgeSeconds int64
	Central100Stale      bool
	Central100Error      string

	Central101           GaroMeterInfo
	Central101Valid      bool
	Central101AgeSeconds int64
	Central101Stale      bool
	Central101Error      string

	LoadBalancingFuse    int
	LoadBalancingFuse101 int
	LBValid              bool
	LBAgeSeconds         int64
	LBStale              bool
	LBError              string

	PilotLevels     []GaroPilotLevel
	PilotValid      bool
	PilotAgeSeconds int64
	PilotStale      bool
	PilotError      string

	ChargeMode           string
	ChargeModeValid      bool
	ChargeModeAgeSeconds int64
	ChargeModeStale      bool
	ChargeModeError      string

	Online bool
}

func NewGaroCache() *GaroCache {
	return &GaroCache{}
}

func sourceAge(now, updated time.Time) (int64, bool) {
	if updated.IsZero() {
		return 0, false
	}

	age := now.Sub(updated)
	if age < 0 {
		age = 0
	}

	return int64(age.Seconds()), age > garoStaleAfter
}

func (c *GaroCache) Snapshot(now time.Time) GaroCacheSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	s := GaroCacheSnapshot{
		Central100:           c.central100,
		Central100Valid:      c.central100Valid,
		Central100Error:      c.central100Error,
		Central101:           c.central101,
		Central101Valid:      c.central101Valid,
		Central101Error:      c.central101Error,
		LoadBalancingFuse:    c.loadBalancingFuse,
		LoadBalancingFuse101: c.loadBalancingFuse101,
		LBValid:              c.lbValid,
		LBError:              c.lbError,
		PilotValid:           c.pilotValid,
		PilotError:           c.pilotError,
		ChargeMode:           c.chargeMode,
		ChargeModeValid:      c.chargeModeValid,
		ChargeModeError:      c.chargeModeError,
	}

	s.PilotLevels = append([]GaroPilotLevel(nil), c.pilotLevels...)

	if c.central100Valid {
		s.Central100AgeSeconds, s.Central100Stale = sourceAge(now, c.central100At)
	}
	if c.central101Valid {
		s.Central101AgeSeconds, s.Central101Stale = sourceAge(now, c.central101At)
	}
	if c.lbValid {
		s.LBAgeSeconds, s.LBStale = sourceAge(now, c.lbAt)
	}
	if c.pilotValid {
		s.PilotAgeSeconds, s.PilotStale = sourceAge(now, c.pilotAt)
	}
	if c.chargeModeValid {
		s.ChargeModeAgeSeconds, s.ChargeModeStale = sourceAge(now, c.chargeModeAt)
	}

	s.Online =
		(c.central100Valid && !s.Central100Stale) ||
			(c.central101Valid && !s.Central101Stale) ||
			(c.lbValid && !s.LBStale) ||
			(c.pilotValid && !s.PilotStale) ||
			(c.chargeModeValid && !s.ChargeModeStale)

	return s
}

func (c *GaroCache) RefreshMeters(g *GaroClient) GaroMeterRefreshResult {
	now := time.Now()
	result := GaroMeterRefreshResult{}

	central100, err := g.GetMeterInfo("CENTRAL100")
	c.mu.Lock()
	if err != nil {
		c.central100Error = err.Error()
	} else {
		c.central100 = central100
		c.central100Valid = true
		c.central100At = now
		c.central100Error = ""
		result.Central100OK = true
	}
	c.mu.Unlock()

	central101, err := g.GetMeterInfo("CENTRAL101")
	c.mu.Lock()
	if err != nil {
		c.central101Error = err.Error()
	} else {
		c.central101 = central101
		c.central101Valid = true
		c.central101At = time.Now()
		c.central101Error = ""
		result.Central101OK = true
	}
	c.mu.Unlock()

	return result
}

func (c *GaroCache) RefreshFast(g *GaroClient) {
	fastInfo, err := g.GetFastInfo()
	c.mu.Lock()
	if err != nil {
		c.pilotError = err.Error()
		c.chargeModeError = err.Error()
	} else {
		now := time.Now()
		c.pilotLevels = append(c.pilotLevels[:0], fastInfo.PilotLevels...)
		c.pilotValid = true
		c.pilotAt = now
		c.pilotError = ""

		c.chargeMode = fastInfo.ChargeMode
		c.chargeModeValid = fastInfo.ChargeMode != ""
		c.chargeModeAt = now
		c.chargeModeError = ""
	}
	c.mu.Unlock()

	lbCfg, err := g.GetLBConfig()
	if err != nil {
		c.mu.Lock()
		c.lbError = err.Error()
		c.mu.Unlock()
		return
	}

	fuse100 := rawInt(lbCfg["loadBalancingFuse"])
	fuse101 := rawInt(lbCfg["loadBalancingFuse101"])
	if fuse100 == nil || fuse101 == nil {
		c.mu.Lock()
		c.lbError = "load-balancing fuse values missing from GARO configuration"
		c.mu.Unlock()
		return
	}

	c.mu.Lock()
	c.loadBalancingFuse = *fuse100
	c.loadBalancingFuse101 = *fuse101
	c.lbValid = true
	c.lbAt = time.Now()
	c.lbError = ""
	c.mu.Unlock()
}

func (c *GaroCache) RunFast(ctx context.Context, g *GaroClient) {
	ticker := time.NewTicker(garoFastPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.RefreshFast(g)
		}
	}
}

func (c *GaroCache) NoteLoadBalancingFuse(currentA int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.loadBalancingFuse = currentA
	if c.lbValid {
		c.lbAt = time.Now()
		c.lbError = ""
	}
}

func (c *GaroCache) NoteLoadBalancingFuse101(currentA int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.loadBalancingFuse101 = currentA
	if c.lbValid {
		c.lbAt = time.Now()
		c.lbError = ""
	}
}

func (c *GaroCache) NoteChargeMode(mode string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.chargeMode = mode
	c.chargeModeValid = mode != ""
	c.chargeModeAt = time.Now()
	c.chargeModeError = ""
}

func garoErrorSummary(s GaroCacheSnapshot) string {
	parts := make([]string, 0, 4)
	if s.Central100Error != "" {
		parts = append(parts, "CENTRAL100: "+s.Central100Error)
	}
	if s.Central101Error != "" {
		parts = append(parts, "CENTRAL101: "+s.Central101Error)
	}
	if s.LBError != "" {
		parts = append(parts, "load balancing: "+s.LBError)
	}
	if s.PilotError != "" {
		parts = append(parts, "pilot status: "+s.PilotError)
	}
	if s.ChargeModeError != "" && s.ChargeModeError != s.PilotError {
		parts = append(parts, "charge mode: "+s.ChargeModeError)
	}
	return strings.Join(parts, "; ")
}

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
