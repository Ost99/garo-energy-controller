package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGaroState int

const (
	garoUnreachable fakeGaroState = iota // SerialService not listening yet
	garoStarting                         // zeroed lbconfig, slaves=null
	garoReadyState                       // normal operation
)

// fakeGaro models the GARO endpoints the settings sync uses, including the
// start-up sequence observed on the Pi.
type fakeGaro struct {
	mu sync.Mutex

	state   fakeGaroState
	fuse100 int
	fuse101 int

	// ignoreFuse101 makes GARO answer 200 to a CENTRAL101 write but keep the
	// old value, to exercise verification and retry.
	ignoreFuse101 bool

	fuse100Posts int
	fuse101Posts int
	otherPosts   int
}

func (f *fakeGaro) set(fn func(f *fakeGaro)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeGaro) counts() (fuse100Posts, fuse101Posts, otherPosts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fuse100Posts, f.fuse101Posts, f.otherPosts
}

func (f *fakeGaro) values() (fuse100, fuse101 int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fuse100, f.fuse101
}

func (f *fakeGaro) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.state == garoUnreachable {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/lbconfig/false":
		if f.state == garoStarting {
			fmt.Fprint(w, `{"loadBalancingFuse":0,"loadBalancingPower":0,"loadBalancingFuse101":0,`+
				`"loadBalancingPower101":0,"masterPhase":0,"masterLoadBalanced":false,"slaves":null}`)
			return
		}
		fmt.Fprintf(w, `{"loadBalancingFuse":%d,"loadBalancingPower":0,"loadBalancingFuse101":%d,`+
			`"loadBalancingPower101":0,"masterPhase":16,"masterLoadBalanced":true,`+
			`"slaves":[{"serialNumber":771240},{"serialNumber":771285}]}`,
			f.fuse100, f.fuse101)

	case r.Method == http.MethodPost && r.URL.Path == "/lbconfig":
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, ok := body["slaves"]; ok {
			f.otherPosts++ // a slaves write would be a Derby write: never expected
		}
		if v := rawInt(body["loadBalancingFuse"]); v != nil && *v != f.fuse100 {
			f.fuse100 = *v
			f.fuse100Posts++
		}
		if v := rawInt(body["loadBalancingFuse101"]); v != nil && *v != f.fuse101 {
			f.fuse101Posts++
			if !f.ignoreFuse101 {
				f.fuse101 = *v
			}
		}

	case r.Method == http.MethodGet && r.URL.Path == "/status":
		fmt.Fprint(w, `{"serialNumber":771285,"factoryCurrentLimit":32,"switchCurrentLimit":32,`+
			`"mainCharger":{"serialNumber":771285,"dipSwitchSettings":7929}}`)

	case r.Method == http.MethodGet && r.URL.Path == "/slaves/false":
		fmt.Fprint(w, `[{"serialNumber":771240,"dipSwitchSettings":7913},`+
			`{"serialNumber":771285,"dipSwitchSettings":7929}]`)

	default:
		f.otherPosts++
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func newSettingsSyncTest(t *testing.T, fake *fakeGaro) (*garoSettingsSync, *GaroClient, *GaroCache, Config) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	cfg := defaultConfig()
	cfg.Enabled = true
	cfg.Mode = "automatic"
	cfg.SafeCurrentA = 8
	cfg.LoadBalancingFuse101A = 32

	return newGaroSettingsSync(), NewGaroClient(srv.URL), NewGaroCache(), cfg
}

func TestGaroSettingsBootSequence(t *testing.T) {
	// GARO persisted an old CENTRAL101 value and the last DLM value.
	fake := &fakeGaro{state: garoUnreachable, fuse100: 20, fuse101: 31}
	ss, garo, cache, cfg := newSettingsSyncTest(t, fake)
	now := time.Now()

	// SerialService not listening yet.
	if err := ss.Sync(now, cfg, garo, cache); err == nil {
		t.Fatal("Sync succeeded while GARO unreachable")
	}

	// Listening, but still in the 70 s charge-card wait.
	fake.set(func(f *fakeGaro) { f.state = garoStarting })
	if err := ss.Sync(now.Add(30*time.Second), cfg, garo, cache); !errors.Is(err, errGaroStarting) {
		t.Fatalf("Sync during start-up = %v, want errGaroStarting", err)
	}
	if p100, p101, other := fake.counts(); p100+p101+other != 0 {
		t.Fatalf("writes before GARO ready: fuse100=%d fuse101=%d other=%d", p100, p101, other)
	}
	if !strings.Contains(ss.Status(), "not applied yet") {
		t.Fatalf("status while starting = %q", ss.Status())
	}

	// Ready: apply startup mode (CENTRAL100 = safe) and CENTRAL101.
	fake.set(func(f *fakeGaro) { f.state = garoReadyState })
	if err := ss.Sync(now.Add(60*time.Second), cfg, garo, cache); err != nil {
		t.Fatalf("Sync when ready: %v", err)
	}
	if f100, f101 := fake.values(); f100 != 8 || f101 != 32 {
		t.Fatalf("GARO after sync: CENTRAL100=%d CENTRAL101=%d, want 8 and 32", f100, f101)
	}
	if _, _, other := fake.counts(); other != 0 {
		t.Fatalf("unexpected requests or slaves writes: %d", other)
	}
	if ss.Status() != "" {
		t.Fatalf("status after sync = %q, want empty", ss.Status())
	}
	if got := cache.Snapshot(time.Now()).LoadBalancingFuse101; got != 32 {
		t.Fatalf("cache CENTRAL101 = %d, want 32 (verified read)", got)
	}
}

func TestGaroSettingsNoWriteWhenMatching(t *testing.T) {
	fake := &fakeGaro{state: garoReadyState, fuse100: 8, fuse101: 32}
	ss, garo, cache, cfg := newSettingsSyncTest(t, fake)
	now := time.Now()

	for i := 0; i < 5; i++ {
		if err := ss.Sync(now.Add(time.Duration(i)*30*time.Second), cfg, garo, cache); err != nil {
			t.Fatalf("Sync %d: %v", i, err)
		}
	}
	if p100, p101, other := fake.counts(); p100+p101+other != 0 {
		t.Fatalf("writes with matching values: fuse100=%d fuse101=%d other=%d", p100, p101, other)
	}
}

func TestGaroSettingsRetryAfterFailedVerify(t *testing.T) {
	fake := &fakeGaro{state: garoReadyState, fuse100: 8, fuse101: 31, ignoreFuse101: true}
	ss, garo, cache, cfg := newSettingsSyncTest(t, fake)
	now := time.Now()

	if err := ss.Sync(now, cfg, garo, cache); err == nil {
		t.Fatal("Sync succeeded although GARO kept the old CENTRAL101")
	}
	if !strings.Contains(ss.Status(), "31 A") {
		t.Fatalf("status = %q, want it to report GARO's value", ss.Status())
	}

	// Within the retry interval: no new write.
	_ = ss.Sync(now.Add(30*time.Second), cfg, garo, cache)
	if _, p101, _ := fake.counts(); p101 != 1 {
		t.Fatalf("CENTRAL101 writes within retry interval = %d, want 1", p101)
	}

	// After the interval: retried, and succeeds once GARO accepts it.
	fake.set(func(f *fakeGaro) { f.ignoreFuse101 = false })
	if err := ss.Sync(now.Add(garoSettingsRetryInterval+time.Second), cfg, garo, cache); err != nil {
		t.Fatalf("retry Sync: %v", err)
	}
	if _, f101 := fake.values(); f101 != 32 {
		t.Fatalf("CENTRAL101 after retry = %d, want 32", f101)
	}
}

func TestGaroSettingsSerialServiceRestart(t *testing.T) {
	fake := &fakeGaro{state: garoReadyState, fuse100: 8, fuse101: 32}
	ss, garo, cache, cfg := newSettingsSyncTest(t, fake)
	now := time.Now()

	if err := ss.Sync(now, cfg, garo, cache); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}

	// Controller has moved DLM to 20 A; SerialService restarts and comes
	// back with an older CENTRAL101.
	fake.set(func(f *fakeGaro) { f.fuse100 = 20; f.state = garoStarting })
	_ = ss.Sync(now.Add(30*time.Second), cfg, garo, cache)
	fake.set(func(f *fakeGaro) { f.fuse101 = 31; f.state = garoReadyState })

	if err := ss.Sync(now.Add(60*time.Second), cfg, garo, cache); err != nil {
		t.Fatalf("Sync after restart: %v", err)
	}
	f100, f101 := fake.values()
	if f101 != 32 {
		t.Fatalf("CENTRAL101 after restart = %d, want 32", f101)
	}
	if f100 != 20 {
		t.Fatalf("CENTRAL100 after restart = %d, want 20 (startup mode applies only once)", f100)
	}
}

func TestGaroSettingsChangedAppliesImmediately(t *testing.T) {
	fake := &fakeGaro{state: garoReadyState, fuse100: 8, fuse101: 32}
	ss, garo, cache, cfg := newSettingsSyncTest(t, fake)
	now := time.Now()

	if err := ss.Sync(now, cfg, garo, cache); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}

	cfg.LoadBalancingFuse101A = 30
	ss.MarkPending()
	if err := ss.Sync(now.Add(time.Second), cfg, garo, cache); err != nil {
		t.Fatalf("Sync after settings change: %v", err)
	}
	if _, f101 := fake.values(); f101 != 30 {
		t.Fatalf("CENTRAL101 after settings change = %d, want 30", f101)
	}
}

func TestLoadBalancingWritesRefusedWhileGaroStarting(t *testing.T) {
	fake := &fakeGaro{state: garoStarting}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	garo := NewGaroClient(srv.URL)

	if err := garo.SetLoadBalancingFuse(10); !errors.Is(err, errGaroStarting) {
		t.Fatalf("SetLoadBalancingFuse during start-up = %v, want errGaroStarting", err)
	}
	if err := garo.SetLoadBalancingFuse101(32); !errors.Is(err, errGaroStarting) {
		t.Fatalf("SetLoadBalancingFuse101 during start-up = %v, want errGaroStarting", err)
	}
	if p100, p101, other := fake.counts(); p100+p101+other != 0 {
		t.Fatalf("POSTs during start-up: fuse100=%d fuse101=%d other=%d", p100, p101, other)
	}

	cache := NewGaroCache()
	if cache.RefreshLoadBalancing(garo) {
		t.Fatal("start-up placeholder accepted as valid load-balancing data")
	}
	if s := cache.Snapshot(time.Now()); s.LBValid {
		t.Fatal("cache marked start-up placeholder valid")
	}
}
