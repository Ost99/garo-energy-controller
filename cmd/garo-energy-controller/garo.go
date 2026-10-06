package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	garoBaseURL = "http://127.0.0.1:8080/servlet/rest/chargebox"

	// GARO firmware reports meter currents in tenths of an ampere.
	garoMeterCurrentUnitsPerAmp = 10.0

	// Conservative fallback estimate. CENTRAL100 measures total site
	// consumption, not net grid import.
	fallbackVoltage = 230.0

	// GARO packs the SW2 1-3 current-selector switches into bits 5-7 of
	// dipSwitchSettings. The current table is the one printed by GARO for
	// GLB/GLBDC chargers. The mapping is intentionally decoded from the bit
	// field instead of hard-coding charger serial numbers.
	garoSW2CurrentMask  = 0xE0
	garoSW2CurrentShift = 5

	garoStaleAfter              = 60 * time.Second
	garoFastPollInterval        = 5 * time.Second
	garoDeepIdlePollInterval    = 30 * time.Second
	garoDeepIdleLBPollInterval  = 60 * time.Second
	garoCapabilityRetryInterval = 30 * time.Second
)

type GaroClient struct {
	baseURL string
	client  *http.Client
	mu      sync.Mutex

	// Charger limits are static hardware/configuration capabilities. Discover
	// them at most once per process startup and reuse them for every fast poll.
	capabilitiesLoaded      bool
	capabilities            GaroCapabilities
	capabilitiesErr         error
	capabilitiesNextAttempt time.Time
}

type GaroChargerCapability struct {
	SerialNumber int `json:"serial_number"`
	MaxPilotA    int `json:"max_pilot_a"`
}

type GaroCapabilities struct {
	Chargers map[int]GaroChargerCapability
}

func (c GaroCapabilities) MaxPilotA(serialNumber int) int {
	if serialNumber == 0 || c.Chargers == nil {
		return 0
	}
	return c.Chargers[serialNumber].MaxPilotA
}

func cloneGaroCapabilities(c GaroCapabilities) GaroCapabilities {
	out := GaroCapabilities{
		Chargers: make(map[int]GaroChargerCapability, len(c.Chargers)),
	}
	for serial, capability := range c.Chargers {
		out.Chargers[serial] = capability
	}
	return out
}

func minPositive(values ...int) int {
	result := 0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if result == 0 || value < result {
			result = value
		}
	}
	return result
}

// decodeGaroSW2MaxPilotA extracts the EVSE's static pilot ceiling from the
// packed dipSwitchSettings value returned for GARO group members.
//
// GARO's SW2 1-3 current table is:
//
//	code 000 = 29 A
//	code 001 =  6 A
//	code 010 = 10 A
//	code 011 = 13 A
//	code 100 = 16 A
//	code 101 = 20 A
//	code 110 = 25 A
//	code 111 = 32 A
//
// The bit placement is also verified on this firmware by observed values:
// 7689 -> 000 -> 29 A, 7881 -> 110 -> 25 A, 7913/7929 -> 111 -> 32 A.
// Other DIP switches may change other bits in dipSwitchSettings; masking 0xE0
// keeps those settings independent of the pilot-current decoder.
func decodeGaroSW2MaxPilotA(dipSwitchSettings int) int {
	if dipSwitchSettings <= 0 {
		return 0
	}

	code := (dipSwitchSettings & garoSW2CurrentMask) >> garoSW2CurrentShift
	switch code {
	case 0:
		return 29
	case 1:
		return 6
	case 2:
		return 10
	case 3:
		return 13
	case 4:
		return 16
	case 5:
		return 20
	case 6:
		return 25
	case 7:
		return 32
	default:
		return 0
	}
}

type garoCapabilityCharger struct {
	SerialNumber        int `json:"serialNumber"`
	MaxChargeCurrent    int `json:"maxChargeCurrent"`
	FactoryChargeLimit  int `json:"factoryChargeLimit"`
	SwitchChargeLimit   int `json:"switchChargeLimit"`
	FactoryCurrentLimit int `json:"factoryCurrentLimit"`
	SwitchCurrentLimit  int `json:"switchCurrentLimit"`
	MaxCurrent          int `json:"maxCurrent"`
	MaxCurrentLimit     int `json:"maxCurrentLimit"`
	DipSwitchSettings   int `json:"dipSwitchSettings"`
}

func (c garoCapabilityCharger) maxPilotA() int {
	return minPositive(
		c.MaxChargeCurrent,
		c.FactoryChargeLimit,
		c.SwitchChargeLimit,
		c.FactoryCurrentLimit,
		c.SwitchCurrentLimit,
		c.MaxCurrent,
		c.MaxCurrentLimit,
		decodeGaroSW2MaxPilotA(c.DipSwitchSettings),
	)
}

func addGaroChargerCapability(
	chargers map[int]GaroChargerCapability,
	serialNumber int,
	maxPilotA int,
) {
	if serialNumber == 0 || maxPilotA <= 0 {
		return
	}

	if existing, ok := chargers[serialNumber]; ok && existing.MaxPilotA > 0 {
		// When multiple GARO endpoints describe the same EVSE, the lowest
		// positive static limit is the safe effective pilot ceiling.
		maxPilotA = minPositive(existing.MaxPilotA, maxPilotA)
	}

	chargers[serialNumber] = GaroChargerCapability{
		SerialNumber: serialNumber,
		MaxPilotA:    maxPilotA,
	}
}

func rawIntByNames(values map[string]json.RawMessage, names ...string) int {
	for _, name := range names {
		if value := rawInt(values[name]); value != nil && *value > 0 {
			return *value
		}
	}
	return 0
}

func (g *GaroClient) getJSONBody(path string) ([]byte, error) {
	resp, err := g.client.Get(g.baseURL + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf(
			"GET %s: HTTP %d: %s",
			strings.TrimPrefix(path, "/"),
			resp.StatusCode,
			string(body),
		)
	}
	return body, nil
}

func (g *GaroClient) discoverCapabilitiesLocked() (GaroCapabilities, error) {
	caps := GaroCapabilities{
		Chargers: make(map[int]GaroChargerCapability),
	}
	problems := make([]string, 0, 3)

	// /status gives the master serial plus its explicit factory/switch limit.
	// mainCharger also carries the packed DIP value, which is useful on firmware
	// variants where one of the explicit fields is absent.
	if body, err := g.getJSONBody("/status"); err != nil {
		problems = append(problems, "status: "+err.Error())
	} else {
		var status struct {
			SerialNumber        int                   `json:"serialNumber"`
			FactoryCurrentLimit int                   `json:"factoryCurrentLimit"`
			SwitchCurrentLimit  int                   `json:"switchCurrentLimit"`
			MainCharger         garoCapabilityCharger `json:"mainCharger"`
		}
		if err := json.Unmarshal(body, &status); err != nil {
			problems = append(problems, "status decode: "+err.Error())
		} else {
			mainSerial := status.SerialNumber
			if status.MainCharger.SerialNumber != 0 {
				mainSerial = status.MainCharger.SerialNumber
			}
			addGaroChargerCapability(
				caps.Chargers,
				mainSerial,
				minPositive(
					status.FactoryCurrentLimit,
					status.SwitchCurrentLimit,
					status.MainCharger.maxPilotA(),
				),
			)
		}
	}

	// /slaves/false is actually the group-member list on this firmware: it
	// includes both master and slave EVSEs. Decode SW2 per serial so every
	// charger gets its own immutable pilot ceiling.
	if body, err := g.getJSONBody("/slaves/false"); err != nil {
		problems = append(problems, "slaves: "+err.Error())
	} else {
		var members []garoCapabilityCharger
		if err := json.Unmarshal(body, &members); err != nil {
			problems = append(problems, "slaves decode: "+err.Error())
		} else {
			for _, member := range members {
				addGaroChargerCapability(
					caps.Chargers,
					member.SerialNumber,
					member.maxPilotA(),
				)
			}
		}
	}

	if len(caps.Chargers) == 0 {
		problems = append(problems, "no charger pilot limits discovered")
	}
	if len(problems) > 0 {
		return caps, fmt.Errorf("GARO capability discovery: %s", strings.Join(problems, "; "))
	}
	return caps, nil
}

func mergeGaroCapabilities(dst, src GaroCapabilities) GaroCapabilities {
	if dst.Chargers == nil {
		dst.Chargers = make(map[int]GaroChargerCapability)
	}
	for serial, capability := range src.Chargers {
		if capability.MaxPilotA > 0 {
			dst.Chargers[serial] = capability
		}
	}
	return dst
}

func (g *GaroClient) getCapabilitiesLocked() (GaroCapabilities, error) {
	if g.capabilitiesLoaded {
		return cloneGaroCapabilities(g.capabilities), g.capabilitiesErr
	}

	now := time.Now()
	if !g.capabilitiesNextAttempt.IsZero() && now.Before(g.capabilitiesNextAttempt) {
		return cloneGaroCapabilities(g.capabilities), g.capabilitiesErr
	}

	discovered, err := g.discoverCapabilitiesLocked()
	g.capabilities = mergeGaroCapabilities(g.capabilities, discovered)
	g.capabilitiesErr = err

	if err == nil && len(g.capabilities.Chargers) > 0 {
		g.capabilitiesLoaded = true
		g.capabilitiesNextAttempt = time.Time{}
	} else {
		// A transient SerialService/startup failure must not permanently cache
		// zero/unknown capabilities. Retry later, but not on every 5 s fast poll.
		g.capabilitiesNextAttempt = now.Add(garoCapabilityRetryInterval)
	}

	return cloneGaroCapabilities(g.capabilities), g.capabilitiesErr
}

func (g *GaroClient) GetCapabilities() (GaroCapabilities, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.getCapabilitiesLocked()
}

func NewGaroClient(baseURL string) *GaroClient {
	return &GaroClient{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (g *GaroClient) getLBConfig() (map[string]json.RawMessage, error) {
	resp, err := g.client.Get(g.baseURL + "/lbconfig/false")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(
			"GET lbconfig: HTTP %d: %s",
			resp.StatusCode,
			string(body),
		)
	}

	var cfg map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// errGaroStarting reports SerialService's start-up state. While it waits for
// the charge card (about 70 s after it starts listening), /lbconfig/false
// answers with every value zeroed and slaves=null. Those values are not real
// configuration and must never be used or written back.
var errGaroStarting = errors.New(
	"GARO is still starting (load-balancing configuration not loaded yet)",
)

// garoLBConfigStarting reports whether an lbconfig read is the start-up
// placeholder rather than GARO's real configuration. On this firmware the
// group-member list always contains at least the master charger once
// SerialService is ready.
func garoLBConfigStarting(cfg map[string]json.RawMessage) bool {
	raw, ok := cfg["slaves"]
	if !ok {
		return true
	}
	var slaves []json.RawMessage
	if err := json.Unmarshal(raw, &slaves); err != nil {
		return true
	}
	return len(slaves) == 0
}

func (g *GaroClient) GetLBConfig() (map[string]json.RawMessage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.getLBConfig()
}

func (g *GaroClient) postLBConfig(cfg map[string]json.RawMessage) error {
	payload, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(
		http.MethodPost,
		g.baseURL+"/lbconfig",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf(
			"POST lbconfig: HTTP %d: %s",
			resp.StatusCode,
			string(body),
		)
	}

	return nil
}

func (g *GaroClient) setLoadBalancingCurrent(
	field string,
	currentA int,
	persistent bool,
) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch field {
	case "loadBalancingFuse", "loadBalancingFuse101":
	default:
		return fmt.Errorf("unsupported load-balancing current field %q", field)
	}
	if currentA <= 0 {
		return fmt.Errorf("%s current must be positive, got %d A", field, currentA)
	}

	// Always fetch the complete current GARO configuration first. This also
	// preserves fields added by GARO firmware versions that this client does not
	// know about.
	cfg, err := g.getLBConfig()
	if err != nil {
		return err
	}

	// Never write back GARO's start-up placeholder: the POST would carry its
	// zeroed values (including the other fuse field) into the live config.
	if garoLBConfigStarting(cfg) {
		return errGaroStarting
	}

	// Avoid a POST (and for CENTRAL101, an unnecessary Derby write) when GARO
	// already has the requested setting.
	if existing := rawInt(cfg[field]); existing != nil && *existing == currentA {
		return nil
	}

	if !persistent {
		// CENTRAL100 is the dynamic DLM actuator. GARO accepts top-level
		// load-balancing updates without the slaves array; omitting it avoids
		// persistent Derby client-box writes for every control adjustment.
		delete(cfg, "slaves")
	}
	cfg[field] = json.RawMessage(strconv.Itoa(currentA))

	return g.postLBConfig(cfg)
}

func (g *GaroClient) SetLoadBalancingFuse(currentA int) error {
	return g.setLoadBalancingCurrent("loadBalancingFuse", currentA, false)
}

func (g *GaroClient) SetLoadBalancingFuse101(currentA int) error {
	// Do not round-trip GARO's live slaves array when changing CENTRAL101.
	// Those objects contain persistent charger configuration (including
	// loadBalanced and phase/rotation state), and writing them back can alter
	// charger configuration. Until the vendor persistence request is fully
	// understood, CENTRAL101 uses the same top-level-only safe write as
	// CENTRAL100. This changes the live value without rewriting charger rows.
	return g.setLoadBalancingCurrent("loadBalancingFuse101", currentA, false)
}

func (g *GaroClient) SetChargeMode(mode string) error {
	switch mode {
	case "ALWAYS_ON", "ALWAYS_OFF", "SCHEMA":
	default:
		return fmt.Errorf("unsupported GARO charge mode %q", mode)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	req, err := http.NewRequest(
		http.MethodPost,
		g.baseURL+"/mode/"+mode,
		nil,
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf(
			"POST mode/%s: HTTP %d: %s",
			mode,
			resp.StatusCode,
			string(body),
		)
	}

	return nil
}

func rawInt(v json.RawMessage) *int {
	if len(v) == 0 {
		return nil
	}

	var n int
	if err := json.Unmarshal(v, &n); err != nil {
		return nil
	}
	return &n
}

type GaroMeterInfo struct {
	Phase1Current float64 `json:"phase1Current"`
	Phase2Current float64 `json:"phase2Current"`
	Phase3Current float64 `json:"phase3Current"`
}

func (m GaroMeterInfo) CurrentsA() (float64, float64, float64) {
	return m.Phase1Current / garoMeterCurrentUnitsPerAmp,
		m.Phase2Current / garoMeterCurrentUnitsPerAmp,
		m.Phase3Current / garoMeterCurrentUnitsPerAmp
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
	return fallbackVoltage * (math.Abs(a) + math.Abs(b) + math.Abs(c))
}

func (g *GaroClient) GetMeterInfo(name string) (GaroMeterInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var meter GaroMeterInfo
	resp, err := g.client.Get(g.baseURL + "/meterinfo/" + name)
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
	SerialNumber      int    `json:"serial_number"`
	PilotA            int    `json:"pilot_a"`
	MaxPilotA         int    `json:"max_pilot_a,omitempty"`
	Connector         string `json:"connector,omitempty"`
	Charging          bool   `json:"charging"`
	Saturated         bool   `json:"saturated"`
	LoadBalanced      bool   `json:"load_balanced"`
	LoadBalancedKnown bool   `json:"load_balanced_known"`
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
		Connector    string `json:"connector"`
		LoadBalanced *bool  `json:"loadBalanced"`
	}
	type pilotState struct {
		PilotA            int
		Connector         string
		LoadBalanced      bool
		LoadBalancedKnown bool
	}

	// Capability discovery is best-effort for telemetry. A transient GARO API
	// failure leaves limits unknown and schedules a later retry; live polling
	// still continues with the last-known capability values.
	capabilities, _ := g.getCapabilitiesLocked()

	var info GaroFastInfo
	levels := make(map[int]pilotState)

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
		state := pilotState{
			PilotA:    master.PilotLevel,
			Connector: master.Connector,
		}
		if master.LoadBalanced != nil {
			state.LoadBalanced = *master.LoadBalanced
			state.LoadBalancedKnown = true
		}
		levels[master.SerialNumber] = state
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
			state := pilotState{
				PilotA:    slave.PilotLevel,
				Connector: slave.Connector,
			}
			if slave.LoadBalanced != nil {
				state.LoadBalanced = *slave.LoadBalanced
				state.LoadBalancedKnown = true
			}
			levels[slave.SerialNumber] = state
		}
	}

	info.PilotLevels = make([]GaroPilotLevel, 0, len(levels))
	for serial, state := range levels {
		connector := strings.ToUpper(strings.TrimSpace(state.Connector))
		maxPilotA := capabilities.MaxPilotA(serial)
		info.PilotLevels = append(info.PilotLevels, GaroPilotLevel{
			SerialNumber:      serial,
			PilotA:            state.PilotA,
			MaxPilotA:         maxPilotA,
			Connector:         connector,
			Charging:          connector == "CHARGING",
			Saturated:         maxPilotA > 0 && state.PilotA >= maxPilotA,
			LoadBalanced:      state.LoadBalanced,
			LoadBalancedKnown: state.LoadBalancedKnown,
		})
	}

	sort.Slice(info.PilotLevels, func(i, j int) bool {
		return info.PilotLevels[i].SerialNumber > info.PilotLevels[j].SerialNumber
	})

	return info, nil
}

func (g *GaroClient) GetLoadBalancingFuse() (int, error) {
	cfg, err := g.GetLBConfig()
	if err != nil {
		return 0, err
	}

	value := rawInt(cfg["loadBalancingFuse"])
	if value == nil {
		return 0, fmt.Errorf("loadBalancingFuse missing from GARO configuration")
	}
	return *value, nil
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

	deepIdle bool
	fastWake chan struct{}
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
	return &GaroCache{
		fastWake: make(chan struct{}, 1),
	}
}

func (c *GaroCache) SetDeepIdle(deepIdle bool) {
	c.mu.Lock()
	changed := c.deepIdle != deepIdle
	c.deepIdle = deepIdle
	c.mu.Unlock()

	if changed {
		select {
		case c.fastWake <- struct{}{}:
		default:
		}
	}
}

func (c *GaroCache) DeepIdle() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.deepIdle
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

func (c *GaroCache) RefreshCentral101(g *GaroClient) bool {
	central101, err := g.GetMeterInfo("CENTRAL101")

	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		c.central101Error = err.Error()
		return false
	}

	c.central101 = central101
	c.central101Valid = true
	c.central101At = time.Now()
	c.central101Error = ""
	return true
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

	result.Central101OK = c.RefreshCentral101(g)

	return result
}

func (c *GaroCache) RefreshFastInfo(g *GaroClient) {
	fastInfo, err := g.GetFastInfo()
	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		c.pilotError = err.Error()
		c.chargeModeError = err.Error()
		return
	}

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

func (c *GaroCache) RefreshLoadBalancing(g *GaroClient) bool {
	lbCfg, err := g.GetLBConfig()
	if err != nil {
		c.mu.Lock()
		c.lbError = err.Error()
		c.mu.Unlock()
		return false
	}

	if garoLBConfigStarting(lbCfg) {
		c.mu.Lock()
		c.lbError = errGaroStarting.Error()
		c.mu.Unlock()
		return false
	}

	fuse100 := rawInt(lbCfg["loadBalancingFuse"])
	fuse101 := rawInt(lbCfg["loadBalancingFuse101"])
	if fuse100 == nil || fuse101 == nil {
		c.mu.Lock()
		c.lbError = "load-balancing fuse values missing from GARO configuration"
		c.mu.Unlock()
		return false
	}

	c.mu.Lock()
	c.loadBalancingFuse = *fuse100
	c.loadBalancingFuse101 = *fuse101
	c.lbValid = true
	c.lbAt = time.Now()
	c.lbError = ""
	c.mu.Unlock()
	return true
}

func (c *GaroCache) RefreshFast(g *GaroClient) {
	c.RefreshFastInfo(g)
	c.RefreshLoadBalancing(g)
}

func (c *GaroCache) RunFast(ctx context.Context, g *GaroClient) {
	lastLBRefresh := time.Now()

	for {
		deepIdle := c.DeepIdle()
		interval := garoFastPollInterval
		if deepIdle {
			interval = garoDeepIdlePollInterval
		}

		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()
			return

		case <-c.fastWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}

			// Leaving deep idle should restore the fast status cache immediately.
			// The control loop separately refreshes the load-balancing configuration
			// before it makes the first active DLM decision.
			if !c.DeepIdle() {
				c.RefreshFastInfo(g)
			}
			continue

		case <-timer.C:
		}

		if !c.DeepIdle() {
			c.RefreshFast(g)
			lastLBRefresh = time.Now()
			continue
		}

		// Deep idle: pilot/charge-mode information is useful for status, but it
		// does not need a five-second cadence. Refresh /status and /slaves/false
		// every 30 seconds, and the more static load-balancing configuration only
		// every 60 seconds.
		c.RefreshFastInfo(g)
		if time.Since(lastLBRefresh) >= garoDeepIdleLBPollInterval {
			c.RefreshLoadBalancing(g)
			lastLBRefresh = time.Now()
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

// applyMode applies the immediate current for startup and explicit mode changes.
// Automatic mode starts from SafeCurrentA; the energy controller then adjusts it.
func applyMode(cfg Config, garo *GaroClient) error {
	if !cfg.Enabled {
		return nil
	}

	switch cfg.Mode {
	case "safe":
		return garo.SetLoadBalancingFuse(cfg.SafeCurrentA)
	case "manual":
		return garo.SetLoadBalancingFuse(cfg.ManualCurrentA)
	case "automatic":
		return garo.SetLoadBalancingFuse(cfg.SafeCurrentA)
	default:
		return fmt.Errorf("unsupported mode %q", cfg.Mode)
	}
}
