package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
)

type ControlTuningConfig struct {
	DownDeadbandW float64 `json:"down_deadband_w"`
	UrgentDownW   float64 `json:"urgent_down_w"`

	OnePhaseUpGateW              float64 `json:"one_phase_up_gate_w"`
	OnePhaseTwoAmpThresholdW     float64 `json:"one_phase_two_amp_threshold_w"`
	OnePhaseCalculatedThresholdW float64 `json:"one_phase_calculated_threshold_w"`
	OnePhaseMaxCalculatedStepA   int     `json:"one_phase_max_calculated_step_a"`
	OnePhaseWattsPerAmp          float64 `json:"one_phase_watts_per_amp"`

	MultiPhaseUpGateW              float64 `json:"multi_phase_up_gate_w"`
	MultiPhaseTwoAmpThresholdW     float64 `json:"multi_phase_two_amp_threshold_w"`
	MultiPhaseCalculatedThresholdW float64 `json:"multi_phase_calculated_threshold_w"`
	MultiPhaseMaxCalculatedStepA   int     `json:"multi_phase_max_calculated_step_a"`
	MultiPhaseWattsPerAmp          float64 `json:"multi_phase_watts_per_amp"`

	CalculatedReserveW float64 `json:"calculated_reserve_w"`

	NormalDwellSeconds int `json:"normal_dwell_seconds"`
	MidUpDwellSeconds  int `json:"mid_up_dwell_seconds"`
	NearUpDwellSeconds int `json:"near_up_dwell_seconds"`

	UnusedDLMHeadroomA float64 `json:"unused_dlm_headroom_a"`
}

type Config struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`

	HourlyLimitKWh float64 `json:"hourly_limit_kwh"`

	SafeCurrentA    int `json:"safe_current_a"`
	MinimumCurrentA int `json:"minimum_current_a"`
	MaximumCurrentA int `json:"maximum_current_a"`
	ManualCurrentA  int `json:"manual_current_a"`

	TibberTimeoutSeconds   int `json:"tibber_timeout_seconds"`
	ControlIntervalSeconds int `json:"control_interval_seconds"`
	IdleTimeoutSeconds     int `json:"idle_timeout_seconds"`

	TibberHomeID string `json:"tibber_home_id,omitempty"`

	ControlTuning ControlTuningConfig `json:"control_tuning"`
}

func defaultControlTuning() ControlTuningConfig {
	return ControlTuningConfig{
		DownDeadbandW: 300,
		UrgentDownW:   1500,

		OnePhaseUpGateW:              400,
		OnePhaseTwoAmpThresholdW:     1500,
		OnePhaseCalculatedThresholdW: 2500,
		OnePhaseMaxCalculatedStepA:   12,
		OnePhaseWattsPerAmp:          240,

		MultiPhaseUpGateW:              800,
		MultiPhaseTwoAmpThresholdW:     2500,
		MultiPhaseCalculatedThresholdW: 4000,
		MultiPhaseMaxCalculatedStepA:   4,
		MultiPhaseWattsPerAmp:          720,

		CalculatedReserveW: 1000,

		NormalDwellSeconds: 60,
		MidUpDwellSeconds:  90,
		NearUpDwellSeconds: 120,

		UnusedDLMHeadroomA: 1.0,
	}
}

func defaultConfig() Config {
	return Config{
		Enabled: true,
		Mode:    "safe",

		HourlyLimitKWh: 9.5,

		SafeCurrentA:    8,
		MinimumCurrentA: 6,
		MaximumCurrentA: 50,
		ManualCurrentA:  8,

		TibberTimeoutSeconds:   30,
		ControlIntervalSeconds: 30,
		IdleTimeoutSeconds:     120,

		ControlTuning: defaultControlTuning(),
	}
}

func (c Config) Validate() error {
	switch c.Mode {
	case "automatic", "safe", "manual":
	default:
		return fmt.Errorf("mode must be automatic, safe, or manual")
	}

	if c.HourlyLimitKWh <= 0 {
		return fmt.Errorf("hourly_limit_kwh must be greater than 0")
	}
	if c.MinimumCurrentA < 6 {
		return fmt.Errorf("minimum_current_a must be at least 6")
	}
	if c.MaximumCurrentA < c.MinimumCurrentA {
		return fmt.Errorf("maximum_current_a must be >= minimum_current_a")
	}
	if c.SafeCurrentA < c.MinimumCurrentA || c.SafeCurrentA > c.MaximumCurrentA {
		return fmt.Errorf("safe_current_a must be within the configured current range")
	}
	if c.ManualCurrentA < c.MinimumCurrentA || c.ManualCurrentA > c.MaximumCurrentA {
		return fmt.Errorf("manual_current_a must be within the configured current range")
	}
	if c.TibberTimeoutSeconds < 5 {
		return fmt.Errorf("tibber_timeout_seconds must be at least 5")
	}
	if c.ControlIntervalSeconds < 5 {
		return fmt.Errorf("control_interval_seconds must be at least 5")
	}
	if c.IdleTimeoutSeconds < 30 {
		return fmt.Errorf("idle_timeout_seconds must be at least 30")
	}

	return c.ControlTuning.Validate()
}

func (t ControlTuningConfig) Validate() error {
	if t.DownDeadbandW < 0 {
		return fmt.Errorf("control_tuning.down_deadband_w must be >= 0")
	}
	if t.UrgentDownW < t.DownDeadbandW {
		return fmt.Errorf("control_tuning.urgent_down_w must be >= down_deadband_w")
	}

	if t.OnePhaseUpGateW < 0 ||
		t.OnePhaseTwoAmpThresholdW < t.OnePhaseUpGateW ||
		t.OnePhaseCalculatedThresholdW <= t.OnePhaseTwoAmpThresholdW {
		return fmt.Errorf("one-phase thresholds must satisfy gate <= +2A threshold < calculated threshold")
	}
	if t.MultiPhaseUpGateW < 0 ||
		t.MultiPhaseTwoAmpThresholdW < t.MultiPhaseUpGateW ||
		t.MultiPhaseCalculatedThresholdW <= t.MultiPhaseTwoAmpThresholdW {
		return fmt.Errorf("multi-phase thresholds must satisfy gate <= +2A threshold < calculated threshold")
	}

	if t.OnePhaseMaxCalculatedStepA < 1 || t.OnePhaseMaxCalculatedStepA > 32 {
		return fmt.Errorf("control_tuning.one_phase_max_calculated_step_a must be 1..32")
	}
	if t.MultiPhaseMaxCalculatedStepA < 1 || t.MultiPhaseMaxCalculatedStepA > 32 {
		return fmt.Errorf("control_tuning.multi_phase_max_calculated_step_a must be 1..32")
	}
	if t.OnePhaseWattsPerAmp <= 0 || t.MultiPhaseWattsPerAmp <= 0 {
		return fmt.Errorf("control_tuning watts-per-amp values must be greater than 0")
	}
	if t.CalculatedReserveW < 0 {
		return fmt.Errorf("control_tuning.calculated_reserve_w must be >= 0")
	}
	if t.NormalDwellSeconds < 0 || t.MidUpDwellSeconds < 0 || t.NearUpDwellSeconds < 0 {
		return fmt.Errorf("control_tuning dwell values must be >= 0")
	}
	if t.UnusedDLMHeadroomA < 0 {
		return fmt.Errorf("control_tuning.unused_dlm_headroom_a must be >= 0")
	}

	return nil
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return Config{}, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

type ConfigStore struct {
	path string

	mu  sync.RWMutex
	cfg Config
}

func NewConfigStore(path string, cfg Config) *ConfigStore {
	return &ConfigStore{
		path: path,
		cfg:  cfg,
	}
}

func (s *ConfigStore) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.cfg
}

func (s *ConfigStore) Set(cfg Config) (bool, error) {
	if err := cfg.Validate(); err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if reflect.DeepEqual(s.cfg, cfg) {
		return false, nil
	}

	if err := writeConfigAtomic(s.path, cfg); err != nil {
		return false, err
	}

	s.cfg = cfg
	return true, nil
}

func writeConfigAtomic(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	mode := os.FileMode(0644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, ".garo-energy-controller-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}

	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cfg); err != nil {
		tmp.Close()
		return err
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		return err
	}

	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}
