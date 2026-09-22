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

	"strconv"

	"sync"

	"time"
)

const (
	listenAddress = ":8090"

	garoBaseURL = "http://127.0.0.1:8080/servlet/rest/chargebox"
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

func (g *GaroClient) SetLoadBalancingFuse(currentA int) error {

	g.mu.Lock()

	defer g.mu.Unlock()

	// Always fetch the complete current GARO configuration first.

	cfg, err := g.getLBConfig()

	if err != nil {

		return err

	}

	// Modify only CENTRAL100.

	cfg["loadBalancingFuse"] = json.RawMessage(strconv.Itoa(currentA))

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

	tibber := NewTibberClient(secrets.TibberToken)

	go tibber.Run(

		context.Background(),

		store.Get,
	)

	controller := NewEnergyController(

		garo,

		tibber,

		store.Get,
	)

	go controller.Run(context.Background())

	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {

		cfg := store.Get()

		status := Status{

			Enabled: cfg.Enabled,

			Mode: cfg.Mode,

			SafeCurrentA: cfg.SafeCurrentA,

			Tibber: tibber.Snapshot(),

			Controller: controller.Snapshot(),
		}

		lbCfg, err := garo.GetLBConfig()

		if err != nil {

			status.Error = err.Error()

			writeJSON(w, status)

			return

		}

		status.GAROOnline = true

		status.LoadBalancingFuse =

			rawInt(lbCfg["loadBalancingFuse"])

		status.LoadBalancingFuse101 =

			rawInt(lbCfg["loadBalancingFuse101"])

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

			var newCfg Config

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

			// Do not reset DLM100 to SafeCurrentA when changing settings while
			// automatic control is already active. The controller will use the
			// new configuration on its next control cycle.
			shouldApplyMode := !(oldCfg.Enabled &&
				oldCfg.Mode == "automatic" &&
				newCfg.Enabled &&
				newCfg.Mode == "automatic")

			if shouldApplyMode {
				if err := applyMode(newCfg, garo); err != nil {

					response.Warning =

						"Configuration saved, but GARO update failed: " +

							err.Error()

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

		writeJSON(w, map[string]any{

			"ok": true,

			"load_balancing_fuse": cfg.SafeCurrentA,
		})

	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {

		if r.URL.Path != "/" {

			http.NotFound(w, r)

			return

		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		io.WriteString(w, page)

	})

	log.Printf("GARO Energy Controller starting on %s", listenAddress)

	if err := http.ListenAndServe(listenAddress, nil); err != nil {

		log.Fatal(err)

	}

}

const page = `<!doctype html>

<html>

<head>

<meta charset="utf-8">

<title>GARO Energy Controller</title>



<style>

body {

    font-family: sans-serif;

    max-width: 760px;

    margin: 40px auto;

    padding: 0 20px;

}



h2 {

    margin-top: 32px;

}



table {

    border-collapse: collapse;

}



td {

    padding: 5px 20px 5px 0;

}



.config-grid {

    display: grid;

    grid-template-columns: 260px max-content;

    gap: 10px 20px;

    align-items: center;

}

.config-grid > div {

    white-space: nowrap;

}

input[type="number"],

input[type="text"],

select {

    width: 140px;

    padding: 5px;

    box-sizing: border-box;

}



button {

    padding: 8px 14px;

    margin-right: 8px;

}



#message {

    color: #080;

}



#error {

    color: #a00;

}



.note {

    color: #666;

    font-size: 0.9em;

}

</style>

</head>



<body>



<h1>GARO Energy Controller</h1>



<h2>Status</h2>



<table>

<tr>

    <td>GARO</td>

    <td id="garo">...</td>

</tr>

<tr>

    <td>CENTRAL100 limit</td>

    <td id="fuse100">...</td>

</tr>

<tr>

    <td>CENTRAL101 limit</td>

    <td id="fuse101">...</td>

</tr>

<tr>

    <td>Controller</td>

    <td id="controllerStatus">...</td>

</tr>

<tr>

    <td>Tibber</td>

    <td id="tibber">...</td>

</tr>

<tr>

    <td>Tibber home</td>

    <td id="tibberHome">...</td>

</tr>

<tr>

    <td>Grid import now</td>

    <td id="tibberPower">...</td>

</tr>

<tr>

    <td>Grid export now</td>

    <td id="tibberProduction">...</td>

</tr>

<tr>

    <td>Imported this hour</td>

    <td id="tibberHour">...</td>

</tr>

<tr>

    <td>Tibber data age</td>

    <td id="tibberAge">...</td>

</tr>

<tr>

    <td>Tibber API requests</td>

    <td id="tibberApiRequests">...</td>

</tr>

<tr>

    <td>Last Tibber API request</td>

    <td id="tibberApiLast">...</td>

</tr>

<tr>

    <td>Last Tibber API result</td>

    <td id="tibberApiResult">...</td>

</tr>

<tr>

    <td>Charging</td>

    <td id="charging">...</td>

</tr>

<tr>

    <td>Control source</td>

    <td id="controlSource">...</td>

</tr>

<tr>

    <td>Hourly consumption</td>

    <td id="hourEnergy">...</td>

</tr>

<tr>

    <td>Remaining allowance</td>

    <td id="remainingEnergy">...</td>

</tr>

<tr>

    <td>Allowed average power</td>

    <td id="allowedPower">...</td>

</tr>

<tr>

    <td>Controller decision</td>

    <td id="decision">...</td>

</tr>

<tr>

    <td>CENTRAL100 current</td>

    <td id="central100Current">...</td>

</tr>

<tr>

    <td>CENTRAL101 current</td>

    <td id="central101Current">...</td>

</tr>





</table>



<h2>Configuration</h2>



<div class="config-grid">



<label for="enabled">Enabled</label>

<input id="enabled" type="checkbox">



<label for="mode">Mode</label>

<select id="mode">

    <option value="automatic">Automatic</option>

    <option value="safe">Safe</option>

    <option value="manual">Manual</option>

</select>



<label for="hourlyLimit">Hourly grid limit</label>

<div>

    <input id="hourlyLimit" type="number"

        min="0.1" step="0.01"> kWh

</div>



<label for="safeCurrent">Safe/default current</label>

<div>

    <input id="safeCurrent" type="number"

        min="6" step="1"> A

</div>



<label for="minimumCurrent">Minimum current</label>

<div>

    <input id="minimumCurrent" type="number"

        min="6" step="1"> A

</div>



<label for="maximumCurrent">Maximum CENTRAL100 current</label>

<div>

    <input id="maximumCurrent" type="number"

        min="6" step="1"> A

</div>



<label for="manualCurrent">Manual current</label>

<div>

    <input id="manualCurrent" type="number"

        min="6" step="1"> A

</div>



<label for="tibberTimeout">Tibber stale timeout</label>

<div>

    <input id="tibberTimeout" type="number"

        min="5" step="1"> s

</div>



<label for="controlInterval">Control interval</label>

<div>

    <input id="controlInterval" type="number"

        min="5" step="1"> s

</div>



<label for="idleTimeout">Idle timeout</label>

<div>

    <input id="idleTimeout" type="number"

        min="30" step="1"> s

</div>

<label for="tibberHomeId">Tibber home ID</label>

<div>

    <input id="tibberHomeId" type="text">

</div>

</div>



<p>

<button onclick="saveConfig()">Save configuration</button>

<button onclick="applySafe()">Restore safe current</button>

</p>



<p class="note">

Automatic mode controls CENTRAL100 from the configured hourly grid-import limit.

Saving settings while already in Automatic mode does not reset the DLM current.

</p>



<p id="message"></p>

<p id="error"></p>



<script>



async function refreshStatus() {

    try {

        const r = await fetch("/api/status");

        const s = await r.json();



        document.getElementById("garo").textContent =

            s.garo_online ? "Online" : "Offline";



        document.getElementById("fuse100").textContent =

            s.load_balancing_fuse !== undefined

                ? s.load_balancing_fuse + " A"

                : "-";



        document.getElementById("fuse101").textContent =

            s.load_balancing_fuse_101 !== undefined

                ? s.load_balancing_fuse_101 + " A"

                : "-";



        document.getElementById("controllerStatus").textContent =

            s.enabled

                ? s.mode

                : "Disabled";



        const t = s.tibber || {};

        document.getElementById("tibberApiRequests").textContent =

            t.api_request_count ?? 0;



        document.getElementById("tibberApiLast").textContent =

            t.last_api_request

                ? t.last_api_request_age_seconds + " s ago"

                : "-";



        document.getElementById("tibberApiResult").textContent =

            t.last_api_result || "-";





        document.getElementById("tibber").textContent =

            !t.configured

                ? "Not configured"

                : t.connected

                    ? "Connected"

                    : "Disconnected";



        document.getElementById("tibberHome").textContent =

            t.home_name || t.home_id || "-";



        document.getElementById("tibberPower").textContent =

            t.connected

                ? Math.round(t.power_w) + " W"

                : "-";



        document.getElementById("tibberProduction").textContent =

            t.connected

                ? Math.round(t.power_production_w) + " W"

                : "-";



        document.getElementById("tibberHour").textContent =

            t.last_update

                ? t.accumulated_consumption_last_hour_kwh.toFixed(3) + " kWh"

                : "-";



        document.getElementById("tibberAge").textContent =

            t.last_update

                ? t.age_seconds + " s"

                : "-";



        if (t.error) {

            document.getElementById("error").textContent =

                "Tibber: " + t.error;

        } else {

            document.getElementById("error").textContent =

                s.error || "";

        }



        const c = s.controller || {};





        document.getElementById("central100Current").textContent =

            c.central100_phase1_a !== undefined

                ? c.central100_phase1_a.toFixed(1) + " / " +

                c.central100_phase2_a.toFixed(1) + " / " +

                c.central100_phase3_a.toFixed(1) + " A"

                : "-";



        document.getElementById("central101Current").textContent =

            c.central101_phase1_a !== undefined

                ? c.central101_phase1_a.toFixed(1) + " / " +

                c.central101_phase2_a.toFixed(1) + " / " +

                c.central101_phase3_a.toFixed(1) + " A"

                : "-";



        document.getElementById("charging").textContent =

            c.charging ? "Yes" : "No";



        document.getElementById("controlSource").textContent =

            c.source || "-";



        document.getElementById("hourEnergy").textContent =

            c.energy_valid

                ? c.hour_energy_kwh.toFixed(3) + " kWh"

                : "-";



        document.getElementById("remainingEnergy").textContent =

            c.energy_valid

                ? c.remaining_energy_kwh.toFixed(3) + " kWh"

                : "-";



        document.getElementById("allowedPower").textContent =

            c.energy_valid

                ? Math.round(c.allowed_average_power_w) + " W"

                : "-";



        document.getElementById("decision").textContent =

            c.decision || "-";





    } catch (e) {

        document.getElementById("error").textContent =

            "Status request failed: " + e;

    }

}



async function loadConfiguration() {

    try {

        const r = await fetch("/api/config");



        if (!r.ok) {

            throw new Error(await r.text());

        }



        const c = await r.json();



        document.getElementById("enabled").checked =

            c.enabled;



        document.getElementById("mode").value =

            c.mode;



        document.getElementById("hourlyLimit").value =

            c.hourly_limit_kwh;



        document.getElementById("safeCurrent").value =

            c.safe_current_a;



        document.getElementById("minimumCurrent").value =

            c.minimum_current_a;



        document.getElementById("maximumCurrent").value =

            c.maximum_current_a;



        document.getElementById("manualCurrent").value =

            c.manual_current_a;



        document.getElementById("tibberTimeout").value =

            c.tibber_timeout_seconds;



        document.getElementById("controlInterval").value =

            c.control_interval_seconds;



        document.getElementById("idleTimeout").value =

            c.idle_timeout_seconds;



        document.getElementById("tibberHomeId").value =

            c.tibber_home_id || "";

    } catch (e) {

        document.getElementById("error").textContent =

            "Configuration request failed: " + e;

    }

}



async function saveConfig() {

    document.getElementById("message").textContent = "";

    document.getElementById("error").textContent = "";



    const config = {

        enabled:

            document.getElementById("enabled").checked,



        mode:

            document.getElementById("mode").value,



        hourly_limit_kwh:

            Number(document.getElementById("hourlyLimit").value),



        safe_current_a:

            Number(document.getElementById("safeCurrent").value),



        minimum_current_a:

            Number(document.getElementById("minimumCurrent").value),



        maximum_current_a:

            Number(document.getElementById("maximumCurrent").value),



        manual_current_a:

            Number(document.getElementById("manualCurrent").value),



        tibber_timeout_seconds:

            Number(document.getElementById("tibberTimeout").value),



        control_interval_seconds:

            Number(document.getElementById("controlInterval").value),



        idle_timeout_seconds:

            Number(document.getElementById("idleTimeout").value),



        tibber_home_id:

            document.getElementById("tibberHomeId").value.trim()

    };



    const r = await fetch("/api/config", {

        method: "POST",

        headers: {

            "Content-Type": "application/json"

        },

        body: JSON.stringify(config)

    });



    if (!r.ok) {

        document.getElementById("error").textContent =

            await r.text();

        return;

    }



    const result = await r.json();



    if (result.warning) {

        document.getElementById("error").textContent =

            result.warning;

    }



    document.getElementById("message").textContent =

        result.saved

            ? "Configuration saved."

            : "Configuration unchanged.";



    await refreshStatus();

}



async function applySafe() {

    document.getElementById("message").textContent = "";

    document.getElementById("error").textContent = "";



    const r = await fetch("/api/safe", {

        method: "POST"

    });



    if (!r.ok) {

        document.getElementById("error").textContent =

            await r.text();

        return;

    }



    document.getElementById("message").textContent =

        "Safe current applied.";



    await refreshStatus();

}



loadConfiguration();

refreshStatus();

setInterval(refreshStatus, 5000);



</script>



</body>

</html>`
