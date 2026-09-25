package main

import (
	"bytes"

	"context"

	"encoding/json"

	"flag"

	"fmt"

	"io"

	"log"

	"net/http"

	"os"

	"strconv"

	"sync"

	"time"
)

const (
	listenAddress = ":8090"

	garoBaseURL = "http://127.0.0.1:8080/servlet/rest/chargebox"

	garoStableImageDir    = "/var/lib/tomcat8/webapps/serialweb/images"
	garoFallbackImageDir  = "/tmp/serialwebapp/webapp/images"
	garoStableJQueryDir   = "/var/lib/tomcat8/webapps/serialweb/jquery"
	garoFallbackJQueryDir = "/tmp/serialwebapp/webapp/jquery"
)

type GaroClient struct {
	baseURL string

	client *http.Client

	mu sync.Mutex
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

func (g *GaroClient) GetLBConfig() (map[string]json.RawMessage, error) {

	g.mu.Lock()

	defer g.mu.Unlock()

	return g.getLBConfig()

}

func (g *GaroClient) setLoadBalancingCurrent(field string, currentA int) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch field {
	case "loadBalancingFuse", "loadBalancingFuse101":
	default:
		return fmt.Errorf("unsupported load-balancing current field %q", field)
	}

	// Always fetch the complete current GARO configuration first.
	cfg, err := g.getLBConfig()
	if err != nil {
		return err
	}

	// Avoid even a no-op POST when the current GARO value already matches.
	if existing := rawInt(cfg[field]); existing != nil && *existing == currentA {
		return nil
	}

	// The GARO servlet updates Derby client-box rows whenever a slaves array is
	// included in the POST. The charger accepts the same top-level load-balancing
	// configuration without that array, avoiding persistent SD-card writes for
	// ordinary DLM current adjustments.
	delete(cfg, "slaves")
	cfg[field] = json.RawMessage(strconv.Itoa(currentA))

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

func (g *GaroClient) SetLoadBalancingFuse(currentA int) error {
	return g.setLoadBalancingCurrent("loadBalancingFuse", currentA)
}

func (g *GaroClient) SetLoadBalancingFuse101(currentA int) error {
	return g.setLoadBalancingCurrent("loadBalancingFuse101", currentA)
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

type Status struct {
	GAROOnline bool `json:"garo_online"`

	LoadBalancingFuse *int `json:"load_balancing_fuse,omitempty"`

	LoadBalancingFuse101 *int `json:"load_balancing_fuse_101,omitempty"`

	ChargeMode           string `json:"charge_mode,omitempty"`
	ChargeModeValid      bool   `json:"charge_mode_valid"`
	ChargeModeAgeSeconds int64  `json:"charge_mode_age_seconds,omitempty"`
	ChargeModeStale      bool   `json:"charge_mode_stale"`
	ChargeModeError      string `json:"charge_mode_error,omitempty"`

	Enabled bool `json:"enabled"`

	Mode string `json:"mode"`

	SafeCurrentA int `json:"safe_current_a"`

	Tibber TibberSnapshot `json:"tibber"`

	Controller ControllerSnapshot `json:"controller"`

	Error string `json:"error,omitempty"`
}

type SaveResponse struct {
	OK bool `json:"ok"`

	Saved bool `json:"saved"`

	Warning string `json:"warning,omitempty"`
}

func appendWarning(response *SaveResponse, warning string) {
	if warning == "" {
		return
	}
	if response.Warning == "" {
		response.Warning = warning
		return
	}
	response.Warning += "; " + warning
}

func ensureLoadBalancingFuse101(
	cfg Config,
	garo *GaroClient,
	garoCache *GaroCache,
) error {
	state := garoCache.Snapshot(time.Now())
	if state.LBValid && state.LoadBalancingFuse101 == cfg.LoadBalancingFuse101A {
		return nil
	}

	if err := garo.SetLoadBalancingFuse101(cfg.LoadBalancingFuse101A); err != nil {
		return err
	}
	garoCache.NoteLoadBalancingFuse101(cfg.LoadBalancingFuse101A)
	return nil
}

func writeJSON(w http.ResponseWriter, value any) {

	w.Header().Set("Content-Type", "application/json")

	json.NewEncoder(w).Encode(value)

}

// applyMode applies the immediate current for startup and explicit mode changes.

//

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

		// Start automatic control from the configured safe/default current.

		return garo.SetLoadBalancingFuse(cfg.SafeCurrentA)

	default:

		return fmt.Errorf("unsupported mode %q", cfg.Mode)

	}

}

func findGaroImageDir() string {
	for _, candidate := range []string{
		garoStableImageDir,
		garoFallbackImageDir,
	} {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate
		}
	}

	return ""
}

func findGaroJQueryDir() string {
	for _, candidate := range []string{
		garoStableJQueryDir,
		garoFallbackJQueryDir,
	} {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate
		}
	}

	return ""
}

func main() {

	configPath := flag.String(

		"config",

		"/etc/garo-energy-controller/config.json",

		"configuration file",
	)

	secretsPath := flag.String(

		"secrets",

		"/etc/garo-energy-controller/secrets.json",

		"secrets file",
	)

	flag.Parse()

	cfg, err := loadConfig(*configPath)

	if err != nil {

		log.Fatalf("configuration error: %v", err)

	}

	store := NewConfigStore(*configPath, cfg)

	garo := NewGaroClient(garoBaseURL)

	secrets, err := loadSecrets(*secretsPath)

	if err != nil {

		log.Printf(

			"warning: could not load secrets from %s: %v",

			*secretsPath,

			err,
		)

	}

	log.Printf(

		"configuration loaded from %s: mode=%s safe=%dA range=%d-%dA hourly=%.2fkWh",

		*configPath,

		cfg.Mode,

		cfg.SafeCurrentA,

		cfg.MinimumCurrentA,

		cfg.MaximumCurrentA,

		cfg.HourlyLimitKWh,
	)

	// Apply the configured startup mode.

	if err := applyMode(cfg, garo); err != nil {

		log.Printf("warning: could not apply startup mode: %v", err)

	}

	garoCache := NewGaroCache()
	// Prime in-memory values before the controller starts. Browser status
	// requests read this cache and never call the GARO API directly.
	garoCache.RefreshFast(garo)
	if err := ensureLoadBalancingFuse101(cfg, garo, garoCache); err != nil {
		log.Printf("warning: could not apply configured CENTRAL101 limit: %v", err)
	}
	go garoCache.RunFast(context.Background(), garo)

	tibber := NewTibberClient(secrets.TibberToken)

	go tibber.Run(

		context.Background(),

		store.Get,
	)

	controller := NewEnergyController(

		garo,

		garoCache,

		tibber,

		store.Get,
	)

	go controller.Run(context.Background())

	if imageDir := findGaroImageDir(); imageDir != "" {
		log.Printf("serving GARO interface images from %s", imageDir)
		http.Handle(
			"/garo-assets/",
			http.StripPrefix(
				"/garo-assets/",
				http.FileServer(http.Dir(imageDir)),
			),
		)
	} else {
		log.Printf("warning: GARO interface image directory not found")
	}

	if jqueryDir := findGaroJQueryDir(); jqueryDir != "" {
		log.Printf("serving GARO jQuery Mobile icon CSS from %s", jqueryDir)
		http.Handle(
			"/garo-jquery/",
			http.StripPrefix(
				"/garo-jquery/",
				http.FileServer(http.Dir(jqueryDir)),
			),
		)
	} else {
		log.Printf("warning: GARO jQuery directory not found")
	}

	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		cfg := store.Get()
		garoState := garoCache.Snapshot(time.Now())
		controllerStatus := controller.Snapshot()
		mergeGaroDiagnostics(&controllerStatus, garoState)

		status := Status{
			GAROOnline:           garoState.Online,
			ChargeMode:           garoState.ChargeMode,
			ChargeModeValid:      garoState.ChargeModeValid,
			ChargeModeAgeSeconds: garoState.ChargeModeAgeSeconds,
			ChargeModeStale:      garoState.ChargeModeStale,
			ChargeModeError:      garoState.ChargeModeError,
			Enabled:              cfg.Enabled,
			Mode:                 cfg.Mode,
			SafeCurrentA:         cfg.SafeCurrentA,
			Tibber:               tibber.Snapshot(),
			Controller:           controllerStatus,
		}

		if garoState.LBValid {
			fuse100 := garoState.LoadBalancingFuse
			fuse101 := garoState.LoadBalancingFuse101
			status.LoadBalancingFuse = &fuse100
			status.LoadBalancingFuse101 = &fuse101
		}

		status.Error = garoErrorSummary(garoState)
		if controllerStatus.Error != "" && status.Error == "" {
			status.Error = controllerStatus.Error
		}

		writeJSON(w, status)
	})

	http.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {

		switch r.Method {

		case http.MethodGet:

			writeJSON(w, store.Get())

			return

		case http.MethodPost:

			r.Body = http.MaxBytesReader(w, r.Body, 16*1024)

			decoder := json.NewDecoder(r.Body)

			decoder.DisallowUnknownFields()

			// Decode on top of the current configuration so newly added tuning
			// fields retain their existing/default values if an older client omits them.
			newCfg := store.Get()

			if err := decoder.Decode(&newCfg); err != nil {

				http.Error(

					w,

					"invalid configuration: "+err.Error(),

					http.StatusBadRequest,
				)

				return

			}

			oldCfg := store.Get()

			saved, err := store.Set(newCfg)

			if err != nil {

				http.Error(

					w,

					"configuration not saved: "+err.Error(),

					http.StatusBadRequest,
				)

				return

			}

			response := SaveResponse{

				OK: true,

				Saved: saved,
			}

			// CENTRAL101 is a fixed charger-subfeed ceiling. The setter uses the
			// tested no-slaves lbconfig payload, avoiding Derby slave-row writes.
			if err := ensureLoadBalancingFuse101(newCfg, garo, garoCache); err != nil {
				appendWarning(
					&response,
					"Configuration saved, but CENTRAL101 update failed: "+err.Error(),
				)
			}

			// Do not reset DLM100 to SafeCurrentA when changing settings while
			// automatic control is already active. The controller will use the
			// new configuration on its next control cycle.
			shouldApplyMode := !(oldCfg.Enabled &&
				oldCfg.Mode == "automatic" &&
				newCfg.Enabled &&
				newCfg.Mode == "automatic")

			if shouldApplyMode {
				if err := applyMode(newCfg, garo); err != nil {
					appendWarning(
						&response,
						"Configuration saved, but GARO update failed: "+err.Error(),
					)
				}
			}

			writeJSON(w, response)

			return

		default:

			http.Error(

				w,

				"GET or POST required",

				http.StatusMethodNotAllowed,
			)

		}

	})

	http.HandleFunc("/api/safe", func(w http.ResponseWriter, r *http.Request) {

		if r.Method != http.MethodPost {

			http.Error(w, "POST required", http.StatusMethodNotAllowed)

			return

		}

		cfg := store.Get()

		if err := garo.SetLoadBalancingFuse(cfg.SafeCurrentA); err != nil {

			http.Error(w, err.Error(), http.StatusBadGateway)

			return

		}
		garoCache.NoteLoadBalancingFuse(cfg.SafeCurrentA)

		writeJSON(w, map[string]any{

			"ok": true,

			"load_balancing_fuse": cfg.SafeCurrentA,
		})

	})

	http.HandleFunc("/api/charge-mode", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 4*1024)
		var request struct {
			Mode string `json:"mode"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid charge mode: "+err.Error(), http.StatusBadRequest)
			return
		}

		if err := garo.SetChargeMode(request.Mode); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		garoCache.NoteChargeMode(request.Mode)

		writeJSON(w, map[string]any{
			"ok":   true,
			"mode": request.Mode,
		})
	})

	http.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/settings" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, settingsPage)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, statusPage)
	})

	log.Printf("GARO Energy Controller starting on %s", listenAddress)

	if err := http.ListenAndServe(listenAddress, nil); err != nil {

		log.Fatal(err)

	}

}

const pageCSS = `
* { box-sizing: border-box; }
html, body { margin: 0; padding: 0; }
body {
    font-family: Arial, Helvetica, sans-serif;
    background: #f7f7f7;
    color: #111;
    font-size: 14px;
}
.topbar {
    height: 46px;
    background: #e9e9e9;
    border-bottom: 1px solid #d5d5d5;
    position: relative;
    display: flex;
    align-items: center;
    justify-content: center;
    color: #444;
    font-size: 13px;
}
.garo-icon-button {
    position: absolute;
    left: 8px;
    top: 8px;
    display: block;
    width: 30px;
    height: 30px;
    padding: 0;
    overflow: hidden;
    text-indent: -9999px;
    white-space: nowrap;
    border: 1px solid #c8c8c8;
    border-radius: 16px;
    background: linear-gradient(#fff, #ededed);
    box-shadow: 0 1px 2px rgba(0,0,0,.18);
}
.garo-icon-button:hover { background: #f4f4f4; }
.garo-icon-button:after {
    content: "";
    position: absolute;
    display: block;
    width: 22px;
    height: 22px;
    left: 50%;
    top: 50%;
    margin-left: -11px;
    margin-top: -11px;
    background-color: #777;
    background-color: rgba(0,0,0,.42);
    background-position: center center;
    background-repeat: no-repeat;
    border-radius: 12px;
}
.page {
    max-width: 1200px;
    margin: 0 auto;
    padding: 16px 16px 40px;
}
.logo-wrap {
    text-align: center;
    padding: 0 0 20px;
    user-select: none;
}
#garoLogo {
    width: 110px;
    max-height: 62px;
    object-fit: contain;
    user-select: none;
    -webkit-user-drag: none;
}
.status-strip {
    background: #ececec;
    border: 1px solid #d6d6d6;
    border-radius: 5px;
    box-shadow: 0 1px 2px rgba(0,0,0,.12);
    text-align: center;
    font-weight: bold;
    font-size: 16px;
    padding: 12px 42px;
    margin-bottom: 26px;
}
.section-label {
    font-weight: bold;
    font-size: 16px;
    margin: 0 0 10px;
}
.panel {
    background: #fff;
    border: 1px solid #d5d5d5;
    border-radius: 5px;
    box-shadow: 0 1px 3px rgba(0,0,0,.12);
    margin-bottom: 16px;
    overflow: hidden;
}
.panel-title {
    background: linear-gradient(#fff, #f3f3f3);
    border-bottom: 1px solid #ddd;
    font-weight: bold;
    font-size: 16px;
    padding: 12px 16px;
}
.panel-body { padding: 14px 16px; }
.device-row {
    display: grid;
    grid-template-columns: 76px minmax(0, 1fr);
    gap: 14px;
    align-items: center;
    padding: 10px 8px;
}
.device-row + .device-row { border-top: 1px solid #ddd; }
.device-icon {
    width: 56px;
    max-height: 82px;
    object-fit: contain;
    justify-self: center;
}
.device-name {
    font-weight: bold;
    font-size: 15px;
    margin-bottom: 6px;
}
.data-line { margin: 4px 0; line-height: 1.35; }
.data-label { font-weight: bold; }
.status-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(280px, 1fr));
    column-gap: 34px;
    row-gap: 7px;
}
.status-item {
    display: grid;
    grid-template-columns: 190px minmax(0, 1fr);
    gap: 10px;
}
.status-item .label { font-weight: bold; }
.config-grid {
    display: grid;
    grid-template-columns: 280px max-content;
    gap: 10px 20px;
    align-items: center;
}
.config-grid > div { white-space: nowrap; }
input[type="number"], input[type="text"], select {
    width: 150px;
    padding: 6px 7px;
    border: 1px solid #bbb;
    border-radius: 3px;
    background: #fff;
}
input[type="checkbox"] { transform: translateY(1px); }
.actions { margin-top: 18px; }
button {
    padding: 8px 14px;
    margin: 0 8px 0 0;
    border: 1px solid #aaa;
    border-radius: 4px;
    background: linear-gradient(#fff, #e9e9e9);
    cursor: pointer;
}
button:hover { background: #eee; }
.note { color: #666; font-size: 12px; margin-top: 12px; }
.current-value { color: #666; margin-left: 8px; font-size: 12px; }
#message { color: #087b12; }
#error { color: #a00; white-space: pre-wrap; }
@media (max-width: 760px) {
    .status-grid { grid-template-columns: 1fr; }
    .status-item { grid-template-columns: 165px minmax(0, 1fr); }
    .config-grid { grid-template-columns: 1fr; gap: 4px; }
    .config-grid label { margin-top: 8px; }
}
`

const statusPage = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GARO Energy Controller</title>
<link rel="stylesheet" href="/garo-jquery/jquery.mobile.icons.min.css">
<style>` + pageCSS + `</style>
</head>
<body>
<div class="topbar">
    <a href="/settings" class="garo-icon-button ui-icon-gear"
       data-role="button" data-icon="gear" data-mini="true" data-iconpos="notext"
       aria-label="Settings">Settings</a>
    <span>GARO Energy Controller</span>
</div>

<div class="page">
    <div class="logo-wrap">
        <img id="garoLogo" src="/garo-assets/garologo.png" alt="GARO">
    </div>

    <div id="topStatus" class="status-strip">Loading...</div>

    <div class="section-label">Controller</div>
    <section class="panel">
        <div class="device-row">
            <img class="device-icon" src="/garo-assets/single.png" alt="Wallbox">
            <div>
                <div class="device-name">Energy controller</div>
                <div class="data-line"><span class="data-label">Mode:</span> <span id="controllerStatus">...</span></div>
                <div class="data-line"><span class="data-label">Charging availability:</span> <span id="chargeAvailability">...</span></div>
                <div class="data-line"><span class="data-label">Charging:</span> <span id="charging">...</span></div>
                <div class="data-line"><span class="data-label">Phase mode:</span> <span id="phaseMode">...</span></div>
                <div class="data-line"><span class="data-label">Pilot currents:</span> <span id="pilotCurrents">...</span></div>
                <div class="data-line"><span class="data-label">Calculated DLM target:</span> <span id="calculatedDlmTarget">...</span></div>
                <div class="data-line"><span class="data-label">Decision:</span> <span id="decision">...</span></div>
            </div>
        </div>
    </section>

    <section class="panel">
        <div class="panel-title">Loadbalancingmeter</div>
        <div class="panel-body">
            <div class="device-row">
                <img class="device-icon" src="/garo-assets/dlm.png" alt="Load balancing meter">
                <div>
                    <div class="device-name">Loadbalancingmeter 100</div>
                    <div class="data-line"><span class="data-label">Configured limit:</span> <span id="fuse100">...</span></div>
                    <div class="data-line"><span class="data-label">Phase current:</span> <span id="central100Current">...</span></div>
                    <div class="data-line"><span class="data-label">Unused DLM headroom:</span> <span id="dlmHeadroom">...</span></div>
                </div>
            </div>
            <div class="device-row">
                <img class="device-icon" src="/garo-assets/dlm.png" alt="Load balancing meter">
                <div>
                    <div class="device-name">Loadbalancingmeter 101</div>
                    <div class="data-line"><span class="data-label">Configured limit:</span> <span id="fuse101">...</span></div>
                    <div class="data-line"><span class="data-label">Phase current:</span> <span id="central101Current">...</span></div>
                </div>
            </div>
        </div>
    </section>

    <section class="panel">
        <div class="panel-title">Grid energy</div>
        <div class="panel-body status-grid">
            <div class="status-item"><span class="label">GARO</span><span id="garo">...</span></div>
            <div class="status-item"><span class="label">Tibber</span><span id="tibber">...</span></div>
            <div class="status-item"><span class="label">Tibber home</span><span id="tibberHome">...</span></div>
            <div class="status-item"><span class="label">Control source</span><span id="controlSource">...</span></div>
            <div class="status-item"><span class="label">Grid import now</span><span id="tibberPower">...</span></div>
            <div class="status-item"><span class="label">Grid export now</span><span id="tibberProduction">...</span></div>
            <div class="status-item"><span class="label">Imported this hour</span><span id="tibberHour">...</span></div>
            <div class="status-item"><span class="label">Controller hour energy</span><span id="hourEnergy">...</span></div>
            <div class="status-item"><span class="label">Expected energy by now</span><span id="expectedHourEnergy">...</span></div>
            <div class="status-item"><span class="label">Energy pacing error</span><span id="pacingError">...</span></div>
            <div class="status-item"><span class="label">Remaining allowance</span><span id="remainingEnergy">...</span></div>
            <div class="status-item"><span class="label">Base target</span><span id="baseTargetPower">...</span></div>
            <div class="status-item"><span class="label">Pacing correction</span><span id="pacingCorrection">...</span></div>
            <div class="status-item"><span class="label">Pacing target</span><span id="pacingTarget">...</span></div>
            <div class="status-item"><span class="label">Hard budget ceiling</span><span id="hardBudgetCeiling">...</span></div>
            <div class="status-item"><span class="label">Effective target</span><span id="allowedPower">...</span></div>
            <div class="status-item"><span class="label">Power headroom</span><span id="powerHeadroom">...</span></div>
            <div class="status-item"><span class="label">Last DLM adjustment</span><span id="lastAdjustment">...</span></div>
            <div class="status-item"><span class="label">Tibber data age</span><span id="tibberAge">...</span></div>
            <div class="status-item"><span class="label">Tibber API requests</span><span id="tibberApiRequests">...</span></div>
            <div class="status-item"><span class="label">Last API request</span><span id="tibberApiLast">...</span></div>
            <div class="status-item"><span class="label">Last API result</span><span id="tibberApiResult">...</span></div>
        </div>
    </section>

    <p id="error"></p>
</div>

<script>
function setText(id, value) {
    document.getElementById(id).textContent = value;
}

function staleSuffix(stale) {
    return stale ? " (stale)" : "";
}

function formatChargeMode(mode) {
    switch (mode) {
    case "ALWAYS_ON": return "Available for charging";
    case "ALWAYS_OFF": return "Not available for charging";
    case "SCHEMA": return "Schedule";
    default: return mode || "-";
    }
}

function formatPhaseCurrents(c, prefix) {
    if (!c[prefix + "_valid"]) {
        return "-";
    }
    const p1 = c[prefix + "_phase1_a"];
    const p2 = c[prefix + "_phase2_a"];
    const p3 = c[prefix + "_phase3_a"];
    return p1.toFixed(1) + " / " + p2.toFixed(1) + " / " + p3.toFixed(1) + " A" +
        staleSuffix(c[prefix + "_stale"]);
}

function formatPilotCurrents(c) {
    const pilots = c.pilot_levels || [];
    if (!c.pilot_valid || pilots.length === 0) {
        return "-";
    }
    return pilots.map(function (p) { return p.serial_number + ": " + p.pilot_a + " A"; }).join(" / ") +
        staleSuffix(c.pilot_stale);
}

async function refreshStatus() {
    try {
        const r = await fetch("/api/status", {cache: "no-store"});
        const s = await r.json();
        const t = s.tibber || {};
        const c = s.controller || {};

        setText("garo", s.garo_online ? "Online" : "Offline");
        setText("fuse100", s.load_balancing_fuse !== undefined ? s.load_balancing_fuse + " A" + staleSuffix(c.dlm_config_stale) : "-");
        setText("fuse101", s.load_balancing_fuse_101 !== undefined ? s.load_balancing_fuse_101 + " A" + staleSuffix(c.dlm_config_stale) : "-");
        setText("controllerStatus", s.enabled ? s.mode : "Disabled");
        setText("chargeAvailability", s.charge_mode_valid ? formatChargeMode(s.charge_mode) + staleSuffix(s.charge_mode_stale) : "-");
        setText("charging", c.central101_valid ? (c.charging ? "Yes" : "No") + staleSuffix(c.central101_stale) : "-");
        setText("phaseMode", c.central101_valid ? (c.phase_mode || "-") + staleSuffix(c.central101_stale) : "-");
        setText("pilotCurrents", formatPilotCurrents(c));
        setText("calculatedDlmTarget", c.calculated_dlm_target_a ? c.calculated_dlm_target_a + " A" : "-");
        setText("decision", c.decision || "-");
        setText("central100Current", formatPhaseCurrents(c, "central100"));
        setText("central101Current", formatPhaseCurrents(c, "central101"));
        setText("dlmHeadroom", c.central100_valid && c.dlm_config_valid ? c.dlm_headroom_a.toFixed(1) + " A" + staleSuffix(c.central100_stale || c.dlm_config_stale) : "-");
        setText("lastAdjustment", c.last_adjustment_age_seconds !== undefined ? c.last_adjustment_age_seconds + " s ago" : "-");
        setText("powerHeadroom", c.energy_valid ? Math.round(c.power_headroom_w) + " W" : "-");
        setText("controlSource", c.source || "-");
        setText("hourEnergy", c.energy_valid ? c.hour_energy_kwh.toFixed(3) + " kWh" : "-");
        setText("expectedHourEnergy", c.energy_valid ? c.expected_hour_energy_kwh.toFixed(3) + " kWh" : "-");
        setText("pacingError", c.energy_valid ? (c.energy_pacing_error_kwh >= 0 ? "+" : "") + c.energy_pacing_error_kwh.toFixed(3) + " kWh" : "-");
        setText("remainingEnergy", c.energy_valid ? c.remaining_energy_kwh.toFixed(3) + " kWh" : "-");
        setText("baseTargetPower", c.energy_valid ? Math.round(c.base_target_power_w) + " W" : "-");
        setText("pacingCorrection", c.energy_valid ? (c.pacing_correction_w >= 0 ? "+" : "") + Math.round(c.pacing_correction_w) + " W" : "-");
        setText("pacingTarget", c.energy_valid ? Math.round(c.pacing_target_power_w) + " W" : "-");
        setText("hardBudgetCeiling", c.energy_valid ? Math.round(c.hard_budget_ceiling_w) + " W" : "-");
        setText("allowedPower", c.energy_valid ? Math.round(c.effective_target_power_w) + " W" : "-");

        setText("tibber", !t.configured ? "Not configured" : (t.connected ? "Connected" : "Disconnected"));
        setText("tibberHome", t.home_name || t.home_id || "-");
        setText("tibberPower", t.connected ? Math.round(t.power_w) + " W" : "-");
        setText("tibberProduction", t.connected ? Math.round(t.power_production_w) + " W" : "-");
        setText("tibberHour", t.last_update ? t.accumulated_consumption_last_hour_kwh.toFixed(3) + " kWh" : "-");
        setText("tibberAge", t.last_update ? t.age_seconds + " s" : "-");
        setText("tibberApiRequests", t.api_request_count ?? 0);
        setText("tibberApiLast", t.last_api_request ? t.last_api_request_age_seconds + " s ago" : "-");
        setText("tibberApiResult", t.last_api_result || "-");

        const modeText = s.enabled ? s.mode.charAt(0).toUpperCase() + s.mode.slice(1) : "Disabled";
        const chargeText = c.central101_valid ? (c.charging ? " - Charging" : " - Not charging") + staleSuffix(c.central101_stale) : " - Charge state unavailable";
        setText("topStatus", modeText + chargeText);

        if (t.error) {
            setText("error", "Tibber: " + t.error);
        } else {
            setText("error", s.error || "");
        }
    } catch (e) {
        setText("error", "Status request failed: " + e);
    }
}

refreshStatus();
setInterval(refreshStatus, 5000);
</script>
</body>
</html>`

const settingsPage = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GARO Energy Controller - Settings</title>
<link rel="stylesheet" href="/garo-jquery/jquery.mobile.icons.min.css">
<style>` + pageCSS + `</style>
</head>
<body>
<div class="topbar">
    <a href="/" class="garo-icon-button ui-icon-home"
       data-role="button" data-icon="home" data-mini="true" data-iconpos="notext"
       aria-label="Home">Home</a>
    <span>Settings</span>
</div>

<div class="page">
    <div class="logo-wrap">
        <img id="garoLogo" src="/garo-assets/garologo.png" alt="GARO">
    </div>

    <section class="panel">
        <div class="panel-title">Charging availability</div>
        <div class="panel-body">
            <div class="data-line"><span class="data-label">Current mode:</span> <span id="chargeAvailability">...</span></div>
            <div class="actions">
                <button onclick="setChargeMode('ALWAYS_ON')">Available for charging</button>
                <button onclick="setChargeMode('ALWAYS_OFF')">Not available for charging</button>
            </div>
            <div class="note">Availability is a group-level GARO mode. Schedule support will use the same mode control later.</div>
        </div>
    </section>

    <section class="panel">
        <div class="panel-title">Configuration</div>
        <div class="panel-body">
            <div class="config-grid">
                <label for="enabled">Controller enabled</label>
                <input id="enabled" type="checkbox">

                <label for="mode">Controller mode</label>
                <select id="mode">
                    <option value="automatic">Automatic</option>
                    <option value="safe">Safe</option>
                    <option value="manual">Manual</option>
                </select>

                <label for="hourlyLimit">Hourly grid limit</label>
                <div><input id="hourlyLimit" type="number" min="0.1" step="0.01"> kWh</div>

                <label for="safeCurrent">Safe/default CENTRAL100 current</label>
                <div><input id="safeCurrent" type="number" min="6" step="1"> A</div>

                <label for="minimumCurrent">Minimum CENTRAL100 current</label>
                <div><input id="minimumCurrent" type="number" min="6" step="1"> A</div>

                <label for="maximumCurrent">Maximum CENTRAL100 current</label>
                <div><input id="maximumCurrent" type="number" min="6" step="1"> A</div>

                <label for="manualCurrent">Manual CENTRAL100 current</label>
                <div><input id="manualCurrent" type="number" min="6" step="1"> A</div>

                <label for="loadBalancingFuse101">CENTRAL101 limit</label>
                <div>
                    <input id="loadBalancingFuse101" type="number" min="16" max="2500" step="1"> A
                    <span class="current-value">Current GARO: <span id="currentFuse101">...</span></span>
                </div>

                <label for="tibberTimeout">Tibber stale timeout</label>
                <div><input id="tibberTimeout" type="number" min="5" step="1"> s</div>

                <label for="controlInterval">Control interval</label>
                <div><input id="controlInterval" type="number" min="5" step="1"> s</div>

                <label for="idleTimeout">Idle timeout</label>
                <div><input id="idleTimeout" type="number" min="30" step="1"> s</div>

                <label for="tibberHomeId">Tibber home ID</label>
                <input id="tibberHomeId" type="text">
            </div>

            <div class="actions">
                <button onclick="saveConfig()">Save configuration</button>
                <button onclick="applySafe()">Restore safe current</button>
            </div>
            <div class="note">Saving CENTRAL101 updates GARO only when the configured value differs from the charger.</div>
        </div>
    </section>

    <section id="controlTuningPanel" class="panel advanced" hidden>
        <div class="panel-title">Control tuning</div>
        <div class="panel-body">
            <div class="config-grid">
                <label for="downDeadband">Downward deadband</label>
                <div><input id="downDeadband" type="number" min="0" step="50"> W</div>

                <label for="urgentDown">Urgent downward threshold</label>
                <div><input id="urgentDown" type="number" min="0" step="50"> W</div>

                <label for="onePhaseGate">1-phase upward gate</label>
                <div><input id="onePhaseGate" type="number" min="0" step="50"> W</div>

                <label for="onePhaseTwoAmp">1-phase +2 A threshold</label>
                <div><input id="onePhaseTwoAmp" type="number" min="0" step="50"> W</div>

                <label for="onePhaseCalculated">1-phase calculated threshold</label>
                <div><input id="onePhaseCalculated" type="number" min="0" step="50"> W</div>

                <label for="onePhaseMaxStep">1-phase calculated max step</label>
                <div><input id="onePhaseMaxStep" type="number" min="1" max="32" step="1"> A</div>

                <label for="onePhaseWattsPerAmp">1-phase watts per amp</label>
                <div><input id="onePhaseWattsPerAmp" type="number" min="1" step="1"> W/A</div>

                <label for="multiPhaseGate">3-phase/mixed upward gate</label>
                <div><input id="multiPhaseGate" type="number" min="0" step="50"> W</div>

                <label for="multiPhaseTwoAmp">3-phase/mixed +2 A threshold</label>
                <div><input id="multiPhaseTwoAmp" type="number" min="0" step="50"> W</div>

                <label for="multiPhaseCalculated">3-phase/mixed calculated threshold</label>
                <div><input id="multiPhaseCalculated" type="number" min="0" step="50"> W</div>

                <label for="multiPhaseMaxStep">3-phase/mixed calculated max step</label>
                <div><input id="multiPhaseMaxStep" type="number" min="1" max="32" step="1"> A</div>

                <label for="multiPhaseWattsPerAmp">3-phase/mixed watts per amp</label>
                <div><input id="multiPhaseWattsPerAmp" type="number" min="1" step="1"> W/A</div>

                <label for="calculatedReserve">Calculated increase reserve</label>
                <div><input id="calculatedReserve" type="number" min="0" step="50"> W</div>

                <label for="pacingHorizon">Pacing horizon</label>
                <div><input id="pacingHorizon" type="number" min="60" step="60"> s</div>

                <label for="maxPacingAdjustment">Maximum pacing adjustment</label>
                <div><input id="maxPacingAdjustment" type="number" min="0" step="100"> W</div>

                <label for="normalDwell">Normal dwell</label>
                <div><input id="normalDwell" type="number" min="0" step="1"> s</div>

                <label for="midUpDwell">Mid upward dwell</label>
                <div><input id="midUpDwell" type="number" min="0" step="1"> s</div>

                <label for="nearUpDwell">Near-target upward dwell</label>
                <div><input id="nearUpDwell" type="number" min="0" step="1"> s</div>

                <label for="unusedDlmHeadroom">Unused DLM headroom threshold</label>
                <div><input id="unusedDlmHeadroom" type="number" min="0" step="0.1"> A</div>
            </div>
            <div class="actions">
                <button onclick="saveConfig()">Save configuration</button>
            </div>
        </div>
    </section>

    <p id="message"></p>
    <p id="error"></p>
</div>

<script>
function setText(id, value) {
    document.getElementById(id).textContent = value;
}

function staleSuffix(stale) {
    return stale ? " (stale)" : "";
}

function formatChargeMode(mode) {
    switch (mode) {
    case "ALWAYS_ON": return "Available for charging";
    case "ALWAYS_OFF": return "Not available for charging";
    case "SCHEMA": return "Schedule";
    default: return mode || "-";
    }
}

async function refreshSettingsStatus() {
    try {
        const r = await fetch("/api/status", {cache: "no-store"});
        if (!r.ok) {
            throw new Error(await r.text());
        }
        const s = await r.json();
        const c = s.controller || {};
        setText("chargeAvailability", s.charge_mode_valid ? formatChargeMode(s.charge_mode) + staleSuffix(s.charge_mode_stale) : "-");
        setText("currentFuse101", s.load_balancing_fuse_101 !== undefined ? s.load_balancing_fuse_101 + " A" + staleSuffix(c.dlm_config_stale) : "-");
        if (s.error) {
            setText("error", s.error);
        }
    } catch (e) {
        setText("error", "Status request failed: " + e);
    }
}

async function loadConfiguration() {
    try {
        const r = await fetch("/api/config", {cache: "no-store"});
        if (!r.ok) {
            throw new Error(await r.text());
        }

        const c = await r.json();
        document.getElementById("enabled").checked = c.enabled;
        document.getElementById("mode").value = c.mode;
        document.getElementById("hourlyLimit").value = c.hourly_limit_kwh;
        document.getElementById("safeCurrent").value = c.safe_current_a;
        document.getElementById("minimumCurrent").value = c.minimum_current_a;
        document.getElementById("maximumCurrent").value = c.maximum_current_a;
        document.getElementById("manualCurrent").value = c.manual_current_a;
        document.getElementById("loadBalancingFuse101").value = c.load_balancing_fuse_101_a;
        document.getElementById("tibberTimeout").value = c.tibber_timeout_seconds;
        document.getElementById("controlInterval").value = c.control_interval_seconds;
        document.getElementById("idleTimeout").value = c.idle_timeout_seconds;
        document.getElementById("tibberHomeId").value = c.tibber_home_id || "";

        const t = c.control_tuning || {};
        document.getElementById("downDeadband").value = t.down_deadband_w;
        document.getElementById("urgentDown").value = t.urgent_down_w;
        document.getElementById("onePhaseGate").value = t.one_phase_up_gate_w;
        document.getElementById("onePhaseTwoAmp").value = t.one_phase_two_amp_threshold_w;
        document.getElementById("onePhaseCalculated").value = t.one_phase_calculated_threshold_w;
        document.getElementById("onePhaseMaxStep").value = t.one_phase_max_calculated_step_a;
        document.getElementById("onePhaseWattsPerAmp").value = t.one_phase_watts_per_amp;
        document.getElementById("multiPhaseGate").value = t.multi_phase_up_gate_w;
        document.getElementById("multiPhaseTwoAmp").value = t.multi_phase_two_amp_threshold_w;
        document.getElementById("multiPhaseCalculated").value = t.multi_phase_calculated_threshold_w;
        document.getElementById("multiPhaseMaxStep").value = t.multi_phase_max_calculated_step_a;
        document.getElementById("multiPhaseWattsPerAmp").value = t.multi_phase_watts_per_amp;
        document.getElementById("calculatedReserve").value = t.calculated_reserve_w;
        document.getElementById("pacingHorizon").value = t.pacing_horizon_seconds;
        document.getElementById("maxPacingAdjustment").value = t.max_pacing_adjustment_w;
        document.getElementById("normalDwell").value = t.normal_dwell_seconds;
        document.getElementById("midUpDwell").value = t.mid_up_dwell_seconds;
        document.getElementById("nearUpDwell").value = t.near_up_dwell_seconds;
        document.getElementById("unusedDlmHeadroom").value = t.unused_dlm_headroom_a;
    } catch (e) {
        setText("error", "Configuration request failed: " + e);
    }
}

async function saveConfig() {
    setText("message", "");
    setText("error", "");

    const config = {
        enabled: document.getElementById("enabled").checked,
        mode: document.getElementById("mode").value,
        hourly_limit_kwh: Number(document.getElementById("hourlyLimit").value),
        safe_current_a: Number(document.getElementById("safeCurrent").value),
        minimum_current_a: Number(document.getElementById("minimumCurrent").value),
        maximum_current_a: Number(document.getElementById("maximumCurrent").value),
        manual_current_a: Number(document.getElementById("manualCurrent").value),
        load_balancing_fuse_101_a: Number(document.getElementById("loadBalancingFuse101").value),
        tibber_timeout_seconds: Number(document.getElementById("tibberTimeout").value),
        control_interval_seconds: Number(document.getElementById("controlInterval").value),
        idle_timeout_seconds: Number(document.getElementById("idleTimeout").value),
        tibber_home_id: document.getElementById("tibberHomeId").value.trim(),
        control_tuning: {
            down_deadband_w: Number(document.getElementById("downDeadband").value),
            urgent_down_w: Number(document.getElementById("urgentDown").value),
            one_phase_up_gate_w: Number(document.getElementById("onePhaseGate").value),
            one_phase_two_amp_threshold_w: Number(document.getElementById("onePhaseTwoAmp").value),
            one_phase_calculated_threshold_w: Number(document.getElementById("onePhaseCalculated").value),
            one_phase_max_calculated_step_a: Number(document.getElementById("onePhaseMaxStep").value),
            one_phase_watts_per_amp: Number(document.getElementById("onePhaseWattsPerAmp").value),
            multi_phase_up_gate_w: Number(document.getElementById("multiPhaseGate").value),
            multi_phase_two_amp_threshold_w: Number(document.getElementById("multiPhaseTwoAmp").value),
            multi_phase_calculated_threshold_w: Number(document.getElementById("multiPhaseCalculated").value),
            multi_phase_max_calculated_step_a: Number(document.getElementById("multiPhaseMaxStep").value),
            multi_phase_watts_per_amp: Number(document.getElementById("multiPhaseWattsPerAmp").value),
            calculated_reserve_w: Number(document.getElementById("calculatedReserve").value),
            pacing_horizon_seconds: Number(document.getElementById("pacingHorizon").value),
            max_pacing_adjustment_w: Number(document.getElementById("maxPacingAdjustment").value),
            normal_dwell_seconds: Number(document.getElementById("normalDwell").value),
            mid_up_dwell_seconds: Number(document.getElementById("midUpDwell").value),
            near_up_dwell_seconds: Number(document.getElementById("nearUpDwell").value),
            unused_dlm_headroom_a: Number(document.getElementById("unusedDlmHeadroom").value)
        }
    };

    const r = await fetch("/api/config", {
        method: "POST",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify(config)
    });

    if (!r.ok) {
        setText("error", await r.text());
        return;
    }

    const result = await r.json();
    if (result.warning) {
        setText("error", result.warning);
    }

    setText("message", result.saved ? "Configuration saved." : "Configuration unchanged.");
    await refreshSettingsStatus();
}

async function applySafe() {
    setText("message", "");
    setText("error", "");

    const r = await fetch("/api/safe", {method: "POST"});
    if (!r.ok) {
        setText("error", await r.text());
        return;
    }

    setText("message", "Safe current applied.");
    await refreshSettingsStatus();
}

async function setChargeMode(mode) {
    setText("message", "");
    setText("error", "");

    const r = await fetch("/api/charge-mode", {
        method: "POST",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify({mode: mode})
    });

    if (!r.ok) {
        setText("error", await r.text());
        return;
    }

    setText("message", mode === "ALWAYS_ON" ? "Charging is available." : "Charging is not available.");
    await refreshSettingsStatus();
}
document.getElementById("garoLogo").addEventListener("dblclick", function () {
    const panel = document.getElementById("controlTuningPanel");
    panel.hidden = !panel.hidden;
});


loadConfiguration();
refreshSettingsStatus();
setInterval(refreshSettingsStatus, 5000);
</script>
</body>
</html>`
