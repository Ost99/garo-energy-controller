package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	Enabled                bool    `json:"enabled"`
	Mode                   string  `json:"mode"`
	HourlyLimitKWh         float64 `json:"hourly_limit_kwh"`
	TibberHomeID           string  `json:"tibber_home_id"`
	SafeCurrentA           int     `json:"safe_current_a"`
	MinimumCurrentA        int     `json:"minimum_current_a"`
	MaximumCurrentA        int     `json:"maximum_current_a"`
	ManualCurrentA         int     `json:"manual_current_a"`
	TibberTimeoutSeconds   int     `json:"tibber_timeout_seconds"`
	ControlIntervalSeconds int     `json:"control_interval_seconds"`
	IdleTimeoutSeconds     int     `json:"idle_timeout_seconds"`
}

func defaultConfig() Config {
	return Config{
		Enabled:                true,
		Mode:                   "safe",
		TibberHomeID:           "",
		HourlyLimitKWh:         9.9,
		SafeCurrentA:           8,
		MinimumCurrentA:        6,
		MaximumCurrentA:        50,
		ManualCurrentA:         8,
		TibberTimeoutSeconds:   30,
		ControlIntervalSeconds: 30,
		IdleTimeoutSeconds:     120,
	}
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}

	// Reject trailing JSON/data.
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return cfg, fmt.Errorf("parse config: multiple JSON values")
		}
		return cfg, fmt.Errorf("parse config: trailing data: %w", err)
	}

	if err := validateConfig(cfg); err != nil {
		return cfg, err
	}

	return cfg, nil
}

func validateConfig(cfg Config) error {
	switch cfg.Mode {
	case "automatic", "safe", "manual":
	default:
		return fmt.Errorf("invalid mode %q", cfg.Mode)
	}

	if cfg.HourlyLimitKWh <= 0 {
		return fmt.Errorf("hourly_limit_kwh must be > 0")
	}

	if cfg.MinimumCurrentA < 6 {
		return fmt.Errorf("minimum_current_a must be >= 6")
	}

	if cfg.MaximumCurrentA < cfg.MinimumCurrentA {
		return fmt.Errorf("maximum_current_a must be >= minimum_current_a")
	}

	if cfg.SafeCurrentA < cfg.MinimumCurrentA ||
		cfg.SafeCurrentA > cfg.MaximumCurrentA {
		return fmt.Errorf("safe_current_a outside configured range")
	}

	if cfg.ManualCurrentA < cfg.MinimumCurrentA ||
		cfg.ManualCurrentA > cfg.MaximumCurrentA {
		return fmt.Errorf("manual_current_a outside configured range")
	}

	if cfg.TibberTimeoutSeconds < 5 {
		return fmt.Errorf("tibber_timeout_seconds must be >= 5")
	}

	if cfg.ControlIntervalSeconds < 5 {
		return fmt.Errorf("control_interval_seconds must be >= 5")
	}

	if cfg.IdleTimeoutSeconds < 30 {
		return fmt.Errorf("idle_timeout_seconds must be >= 30")
	}

	return nil
}

type ConfigStore struct {
	mu   sync.RWMutex
	path string
	cfg  Config
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

// Set validates and stores the new configuration.
// The SD card is only written if the configuration actually changed.
func (s *ConfigStore) Set(cfg Config) (bool, error) {
	if err := validateConfig(cfg); err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if cfg == s.cfg {
		return false, nil
	}

	if err := saveConfigAtomic(s.path, cfg); err != nil {
		return false, err
	}

	s.cfg = cfg
	return true, nil
}

func saveConfigAtomic(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	data = append(data, '\n')

	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}

	tmpName := tmp.Name()
	removeTemp := true

	defer func() {
		if removeTemp {
			os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}

	if _, err := tmp.Write(data); err != nil {
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

	removeTemp = false
	return nil
}
