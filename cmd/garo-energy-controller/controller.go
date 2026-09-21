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

	controlDownDeadbandW = 300.0
	controlUpDeadbandW   = 1000.0

	// Conservative fallback estimate.
	// CENTRAL100 measures total site consumption, not grid import.
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

	Charging bool `json:"charging"`

	Central100Phase1A float64 `json:"central100_phase1_a"`
	Central100Phase2A float64 `json:"central100_phase2_a"`
	Central100Phase3A float64 `json:"central100_phase3_a"`

	Central101Phase1A float64 `json:"central101_phase1_a"`
	Central101Phase2A float64 `json:"central101_phase2_a"`
	Central101Phase3A float64 `json:"central101_phase3_a"`

	EnergyValid bool `json:"energy_valid"`

	HourEnergyKWh      float64 `json:"hour_energy_kwh"`
	RemainingEnergyKWh float64 `json:"remaining_energy_kwh"`

	GridPowerW           float64 `json:"grid_power_w"`
	AllowedAveragePowerW float64 `json:"allowed_average_power_w"`

	SecondsRemaining int `json:"seconds_remaining"`

	DLMCurrentA int `json:"dlm_current_a"`

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

	fallbackValid     bool
	fallbackHour      time.Time
	fallbackEnergyKWh float64
	fallbackLast      time.Time
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

func (c *EnergyController) getEnergySource(
	now time.Time,
	cfg Config,
	central100 GaroMeterInfo,
) (
	source string,
	energyKWh float64,
	powerW float64,
	valid bool,
) {

	t := c.tibber.Snapshot()

	tibberFresh :=
		t.Connected &&
			t.LastUpdate != "" &&
			t.AgeSeconds <= int64(cfg.TibberTimeoutSeconds)

	currentHour := hourStart(now)

	if tibberFresh {
		// Tibber power is grid import. Do not treat export as
		// negative consumption for the hourly import controller.
		powerW = math.Max(t.PowerW, 0)

		// Seed/update the conservative fallback from the latest
		// trustworthy Tibber hourly value.
		c.fallbackValid = true
		c.fallbackHour = currentHour
		c.fallbackEnergyKWh =
			t.AccumulatedConsumptionLastHourKWh
		c.fallbackLast = now

		return "tibber",
			t.AccumulatedConsumptionLastHourKWh,
			powerW,
			true
	}

	if !c.fallbackValid {
		return "none", 0, 0, false
	}

	// If this controller stopped running for too long, integrating
	// CENTRAL100 from the previous point would produce an unknown gap.
	maxGap :=
		time.Duration(cfg.ControlIntervalSeconds*2+10) *
			time.Second

	if now.Sub(c.fallbackLast) > maxGap {
		c.fallbackValid = false
		return "none", 0, 0, false
	}

	powerW = central100.ConservativePowerW()

	if !c.fallbackHour.Equal(currentHour) {
		// The controller stayed alive across an hour boundary while
		// Tibber was unavailable. Start a new conservative accumulator.
		//
		// CENTRAL100 measures total consumption, so this will generally
		// overestimate grid import when solar is producing.
		c.fallbackHour = currentHour
		c.fallbackEnergyKWh = 0
		c.fallbackLast = currentHour
	}

	elapsed := now.Sub(c.fallbackLast)

	if elapsed > 0 {
		c.fallbackEnergyKWh +=
			powerW *
				elapsed.Hours() /
				1000.0
	}

	c.fallbackLast = now

	return "central100",
		c.fallbackEnergyKWh,
		powerW,
		true
}

func populateEnergyDiagnostics(
	s *ControllerSnapshot,
	now time.Time,
	cfg Config,
	source string,
	hourEnergy float64,
	gridPower float64,
	valid bool,
) {

	s.Source = source
	s.EnergyValid = valid

	if !valid {
		return
	}

	s.HourEnergyKWh = hourEnergy
	s.GridPowerW = gridPower

	remainingEnergy :=
		cfg.HourlyLimitKWh - hourEnergy

	if remainingEnergy < 0 {
		remainingEnergy = 0
	}

	s.RemainingEnergyKWh = remainingEnergy

	secondsRemaining :=
		int(nextHour(now).Sub(now).Seconds())

	if secondsRemaining < 1 {
		secondsRemaining = 1
	}

	s.SecondsRemaining = secondsRemaining

	s.AllowedAveragePowerW =
		remainingEnergy *
			3600000.0 /
			float64(secondsRemaining)
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

func (c *EnergyController) tick() {
	now := time.Now()
	cfg := c.getConfig()

	s := ControllerSnapshot{
		LastRun: now.Format(time.RFC3339),
		State:   "monitoring",
		Source:  "none",
	}

	//
	// Always collect physical GARO status.
	//
	// This happens even in Disabled, Safe and Manual modes so the
	// diagnostics page remains useful regardless of control mode.
	//

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
		s.Decision = "could not read CENTRAL100 DLM limit"
		c.setSnapshot(s)
		return
	}

	s.DLMCurrentA = currentLimit

	c100p1, c100p2, c100p3 := central100.CurrentsA()

	s.Central100Phase1A = c100p1
	s.Central100Phase2A = c100p2
	s.Central100Phase3A = c100p3

	c101p1, c101p2, c101p3 := central101.CurrentsA()

	s.Central101Phase1A = c101p1
	s.Central101Phase2A = c101p2
	s.Central101Phase3A = c101p3

	charging :=
		central101.MaxCurrentA() >= chargingThresholdA

	s.Charging = charging

	//
	// Always resolve the energy source and calculate the hourly budget.
	//
	// This means Safe/Manual/Disabled modes still show exactly what the
	// automatic controller would currently see.
	//

	source,
		hourEnergy,
		gridPower,
		valid :=
		c.getEnergySource(
			now,
			cfg,
			central100,
		)

	populateEnergyDiagnostics(
		&s,
		now,
		cfg,
		source,
		hourEnergy,
		gridPower,
		valid,
	)

	//
	// Control-mode decisions begin here.
	//

	if !cfg.Enabled {
		s.State = "disabled"
		s.Decision = "controller disabled"

		// Do not carry idle/session state into a later automatic run.
		c.idleSince = time.Time{}
		c.safeApplied = false

		c.setSnapshot(s)
		return
	}

	if cfg.Mode != "automatic" {
		s.State = cfg.Mode
		s.Decision = "automatic controller not active"

		// Starting Automatic later should begin a fresh idle timer.
		c.idleSince = time.Time{}
		c.safeApplied = false

		c.setSnapshot(s)
		return
	}

	//
	// Automatic mode.
	//

	// Session idle handling.
	if !charging {
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
					s.DLMCurrentA = cfg.SafeCurrentA
					s.Decision =
						fmt.Sprintf(
							"session idle; restored %d A",
							cfg.SafeCurrentA,
						)

					c.safeApplied = true
				}
			} else {
				s.Decision =
					"safe current already restored"
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

	// Active charging session.
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
			s.DLMCurrentA = cfg.SafeCurrentA
		}

		c.setSnapshot(s)
		return
	}

	s.State = "automatic"

	target := currentLimit

	//
	// Normal regulation.
	//
	// Use small adjustments because GARO itself rate-limits pilot
	// changes and large downward changes have produced undesirable
	// drops to the minimum pilot during testing.
	//

	if s.RemainingEnergyKWh <= 0 {
		target = cfg.MinimumCurrentA
		s.Decision = "hourly budget exhausted"

	} else if s.GridPowerW >
		s.AllowedAveragePowerW+controlDownDeadbandW {

		target = currentLimit - 1

		s.Decision = fmt.Sprintf(
			"reduce: %.0f W > %.0f W allowed",
			s.GridPowerW,
			s.AllowedAveragePowerW,
		)

	} else if s.GridPowerW <
		s.AllowedAveragePowerW-controlUpDeadbandW {

		target = currentLimit + 1

		s.Decision = fmt.Sprintf(
			"increase: %.0f W < %.0f W allowed",
			s.GridPowerW,
			s.AllowedAveragePowerW,
		)

	} else {
		s.Decision = "within control band"
	}

	if target < cfg.MinimumCurrentA {
		target = cfg.MinimumCurrentA
	}

	if target > cfg.MaximumCurrentA {
		target = cfg.MaximumCurrentA
	}

	if target == currentLimit {
		c.setSnapshot(s)
		return
	}

	if err := c.garo.SetLoadBalancingFuse(target); err != nil {
		s.Error = err.Error()
		s.Decision += "; GARO update failed"
		c.setSnapshot(s)
		return
	}

	s.DLMCurrentA = target

	s.Decision += fmt.Sprintf(
		"; DLM %d -> %d A",
		currentLimit,
		target,
	)

	c.setSnapshot(s)
}
