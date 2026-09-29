package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	garoStableImageDir    = "/var/lib/tomcat8/webapps/serialweb/images"
	garoFallbackImageDir  = "/tmp/serialwebapp/webapp/images"
	garoStableJQueryDir   = "/var/lib/tomcat8/webapps/serialweb/jquery"
	garoFallbackJQueryDir = "/tmp/serialwebapp/webapp/jquery"
)

type Status struct {
	GAROOnline bool `json:"garo_online"`

	LoadBalancingFuse    *int `json:"load_balancing_fuse,omitempty"`
	LoadBalancingFuse101 *int `json:"load_balancing_fuse_101,omitempty"`

	ChargeMode           string `json:"charge_mode,omitempty"`
	ChargeModeValid      bool   `json:"charge_mode_valid"`
	ChargeModeAgeSeconds int64  `json:"charge_mode_age_seconds,omitempty"`
	ChargeModeStale      bool   `json:"charge_mode_stale"`
	ChargeModeError      string `json:"charge_mode_error,omitempty"`

	Enabled      bool               `json:"enabled"`
	Mode         string             `json:"mode"`
	SafeCurrentA int                `json:"safe_current_a"`
	Tibber       TibberSnapshot     `json:"tibber"`
	Controller   ControllerSnapshot `json:"controller"`
	Error        string             `json:"error,omitempty"`
}

type SaveResponse struct {
	OK      bool   `json:"ok"`
	Saved   bool   `json:"saved"`
	Warning string `json:"warning,omitempty"`
}

type HTTPServer struct {
	store      *ConfigStore
	garo       *GaroClient
	garoCache  *GaroCache
	tibber     *TibberClient
	controller *EnergyController
}

func NewHTTPHandler(
	store *ConfigStore,
	garo *GaroClient,
	garoCache *GaroCache,
	tibber *TibberClient,
	controller *EnergyController,
) http.Handler {
	s := &HTTPServer{
		store:      store,
		garo:       garo,
		garoCache:  garoCache,
		tibber:     tibber,
		controller: controller,
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return mux
}

func (s *HTTPServer) registerRoutes(mux *http.ServeMux) {
	if imageDir := findExistingDir(garoStableImageDir, garoFallbackImageDir); imageDir != "" {
		log.Printf("serving GARO interface images from %s", imageDir)
		mux.Handle(
			"/garo-assets/",
			http.StripPrefix("/garo-assets/", http.FileServer(http.Dir(imageDir))),
		)
	} else {
		log.Printf("warning: GARO interface image directory not found")
	}

	if jqueryDir := findExistingDir(garoStableJQueryDir, garoFallbackJQueryDir); jqueryDir != "" {
		log.Printf("serving GARO jQuery Mobile icon CSS from %s", jqueryDir)
		mux.Handle(
			"/garo-jquery/",
			http.StripPrefix("/garo-jquery/", http.FileServer(http.Dir(jqueryDir))),
		)
	} else {
		log.Printf("warning: GARO jQuery directory not found")
	}

	mux.Handle("/assets/", http.StripPrefix("/assets/", webAssetsHandler()))
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/safe", s.handleSafe)
	mux.HandleFunc("/api/charge-mode", s.handleChargeMode)
	mux.HandleFunc("/settings", s.handleSettingsPage)
	mux.HandleFunc("/", s.handleStatusPage)
}

func findExistingDir(candidates ...string) string {
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
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

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (s *HTTPServer) handleStatus(w http.ResponseWriter, _ *http.Request) {
	cfg := s.store.Get()
	garoState := s.garoCache.Snapshot(time.Now())
	controllerStatus := s.controller.Snapshot()
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
		Tibber:               s.tibber.Snapshot(),
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
}

func (s *HTTPServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.store.Get())
		return

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		// Decode on top of the current configuration so newly added tuning
		// fields retain their existing/default values if an older client omits them.
		newCfg := s.store.Get()
		if err := decoder.Decode(&newCfg); err != nil {
			http.Error(w, "invalid configuration: "+err.Error(), http.StatusBadRequest)
			return
		}

		oldCfg := s.store.Get()
		saved, err := s.store.Set(newCfg)
		if err != nil {
			http.Error(w, "configuration not saved: "+err.Error(), http.StatusBadRequest)
			return
		}

		response := SaveResponse{OK: true, Saved: saved}

		// CENTRAL101 is a fixed charger-subfeed ceiling. The setter uses the
		// tested no-slaves lbconfig payload, avoiding Derby slave-row writes.
		if err := ensureLoadBalancingFuse101(newCfg, s.garo, s.garoCache); err != nil {
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
			if err := applyMode(newCfg, s.garo); err != nil {
				appendWarning(
					&response,
					"Configuration saved, but GARO update failed: "+err.Error(),
				)
			}
		}

		writeJSON(w, response)
		return

	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}

func (s *HTTPServer) handleSafe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	cfg := s.store.Get()
	garoState := s.garoCache.Snapshot(time.Now())
	oldCurrent := cfg.SafeCurrentA
	if garoState.LBValid {
		oldCurrent = garoState.LoadBalancingFuse
	}

	if err := s.garo.SetLoadBalancingFuse(cfg.SafeCurrentA); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.garoCache.NoteLoadBalancingFuse(cfg.SafeCurrentA)

	// Record the operator action so automatic DLM control cannot overwrite the
	// safe-current request on the very next tick. The controller holds for at
	// least one normal dwell/control interval.
	s.controller.NoteManualAction(cfg.SafeCurrentA - oldCurrent)

	writeJSON(w, map[string]any{
		"ok":                  true,
		"load_balancing_fuse": cfg.SafeCurrentA,
	})
}

func (s *HTTPServer) handleChargeMode(w http.ResponseWriter, r *http.Request) {
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

	if err := s.garo.SetChargeMode(request.Mode); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.garoCache.NoteChargeMode(request.Mode)

	// Charge-availability changes are deliberate operator actions. Hold DLM
	// automation briefly so the next tick cannot race the GARO mode transition.
	s.controller.NoteManualAction(0)

	writeJSON(w, map[string]any{
		"ok":   true,
		"mode": request.Mode,
	})
}

func (s *HTTPServer) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/settings" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, settingsPage)
}

func (s *HTTPServer) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, statusPage)
}
