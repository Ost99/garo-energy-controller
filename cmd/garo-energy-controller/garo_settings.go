package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// GARO settings sync
//
// The configuration file is the single source of truth for the GARO settings
// this service owns. They are applied when GARO is ready, never earlier:
//
//   - after the service starts: the startup mode current (applyMode) and
//     CENTRAL101,
//   - after GARO comes back from being unreachable or from its start-up
//     placeholder state (SerialService restart): CENTRAL101,
//   - after a settings save: CENTRAL101 (the mode is applied by the save
//     handler itself, as before).
//
// CENTRAL101 is compared first and only written on a difference, using the
// top-level-only lbconfig POST. Measured on the Pi, that write causes no SD
// card write, and GARO keeps the value across a clean reboot, so normally a
// boot costs no write at all. After a failure the sync retries once per
// garoSettingsRetryInterval.
//
// The control algorithm never chooses a CENTRAL101 value; this sync only ever
// writes the configured one.

const garoSettingsRetryInterval = time.Minute

type garoSettingsSync struct {
	mu sync.Mutex

	// pending is set at startup, on a not-ready -> ready transition and on
	// settings saves, and cleared once GARO verifiably matches the config.
	pending bool
	// wasReady is the readiness seen on the previous attempt.
	wasReady bool
	// startupModeApplied is set once the startup mode current was applied.
	startupModeApplied bool

	lastFailure time.Time

	// status is read by the status page without taking mu, so a slow GARO
	// call inside Sync never blocks a controller snapshot.
	status atomic.Value // string
}

func newGaroSettingsSync() *garoSettingsSync {
	s := &garoSettingsSync{pending: true}
	s.status.Store("GARO settings not applied yet: waiting for GARO")
	return s
}

// Status returns a short message while settings are not yet applied, or ""
// once GARO matches the configuration.
func (s *garoSettingsSync) Status() string {
	v, _ := s.status.Load().(string)
	return v
}

// MarkPending makes the next sync compare and apply the settings again. Used
// after a settings save; it also skips any retry wait.
func (s *garoSettingsSync) MarkPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = true
	s.lastFailure = time.Time{}
}

// Sync applies the configured GARO settings if they are pending and GARO is
// ready. It returns nil when GARO matches the configuration (or nothing is
// pending), errGaroStarting-style errors while GARO is not ready, and the
// write/verify error otherwise.
func (s *garoSettingsSync) Sync(
	now time.Time,
	cfg Config,
	garo *GaroClient,
	garoCache *GaroCache,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lbCfg, err := garoReady(garo)
	if err != nil {
		s.wasReady = false
		if s.pending {
			s.status.Store("GARO settings not applied yet: " + err.Error())
		}
		return err
	}
	if !s.wasReady {
		// First ready read after start, outage or SerialService restart.
		s.pending = true
		s.wasReady = true
	}
	if !s.pending {
		return nil
	}
	if !s.lastFailure.IsZero() && now.Sub(s.lastFailure) < garoSettingsRetryInterval {
		return fmt.Errorf("%s", s.Status())
	}

	if err := s.apply(cfg, lbCfg, garo, garoCache); err != nil {
		s.lastFailure = now
		msg := "GARO settings not applied: " + err.Error()
		s.status.Store(msg)
		log.Printf("%s; retrying in %s", msg, garoSettingsRetryInterval)
		return err
	}

	s.pending = false
	s.lastFailure = time.Time{}
	s.status.Store("")
	return nil
}

func (s *garoSettingsSync) apply(
	cfg Config,
	lbCfg map[string]json.RawMessage,
	garo *GaroClient,
	garoCache *GaroCache,
) error {
	if !s.startupModeApplied {
		if err := applyMode(cfg, garo); err != nil {
			return fmt.Errorf("startup mode: %w", err)
		}
		s.startupModeApplied = true
		log.Printf("applied startup mode %q to GARO", cfg.Mode)
	}

	// Compare against the fresh readiness read, never the cache: the cache
	// may hold a value from before a GARO restart. The startup mode only
	// changes CENTRAL100, so this read is still current for CENTRAL101.
	want := cfg.LoadBalancingFuse101A
	current := rawInt(lbCfg["loadBalancingFuse101"])
	if current != nil && *current == want {
		return nil
	}

	found := "missing"
	if current != nil {
		found = fmt.Sprintf("%d A", *current)
	}

	if err := garo.SetLoadBalancingFuse101(want); err != nil {
		return fmt.Errorf("write CENTRAL101 %d A: %w", want, err)
	}

	// Verify against GARO itself; this read also refreshes the cache.
	if !garoCache.RefreshLoadBalancing(garo) {
		return fmt.Errorf("verify CENTRAL101: %s", garoCache.Snapshot(time.Now()).LBError)
	}
	if got := garoCache.Snapshot(time.Now()).LoadBalancingFuse101; got != want {
		return fmt.Errorf("GARO reports CENTRAL101 %d A after writing %d A", got, want)
	}

	log.Printf("CENTRAL101 set to configured %d A (GARO had %s)", want, found)
	return nil
}

// garoReady reports whether SerialService has finished starting: lbconfig is
// real configuration (not the zeroed start-up placeholder) and the charger
// capabilities could be discovered. It returns the lbconfig it read.
// Capabilities are cached once discovered, so this normally costs a single
// lbconfig GET.
func garoReady(garo *GaroClient) (map[string]json.RawMessage, error) {
	lbCfg, err := garo.GetLBConfig()
	if err != nil {
		return nil, err
	}
	if garoLBConfigStarting(lbCfg) {
		return nil, errGaroStarting
	}
	if _, err := garo.GetCapabilities(); err != nil {
		return nil, err
	}
	return lbCfg, nil
}
