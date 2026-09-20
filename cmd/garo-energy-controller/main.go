package main

import (
	"bytes"
	"encoding/json"
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
	garoBaseURL   = "http://127.0.0.1:8080/servlet/rest/chargebox"

	safeCurrentA = 8
)

type GaroClient struct {
	baseURL string
	client  *http.Client
	mu      sync.Mutex
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
		return nil, fmt.Errorf("GET lbconfig: HTTP %d: %s",
			resp.StatusCode, string(body))
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

	// Always start with the complete current GARO configuration.
	cfg, err := g.getLBConfig()
	if err != nil {
		return err
	}

	// Modify only CENTRAL100's current limit.
	cfg["loadBalancingFuse"] =
		json.RawMessage(strconv.Itoa(currentA))

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
		return fmt.Errorf("POST lbconfig: HTTP %d: %s",
			resp.StatusCode, string(body))
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

	LoadBalancingFuse    *int `json:"load_balancing_fuse,omitempty"`
	LoadBalancingFuse101 *int `json:"load_balancing_fuse_101,omitempty"`

	SafeCurrentA int `json:"safe_current_a"`

	Error string `json:"error,omitempty"`
}

func main() {
	garo := NewGaroClient(garoBaseURL)

	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		status := Status{
			SafeCurrentA: safeCurrentA,
		}

		cfg, err := garo.GetLBConfig()
		if err != nil {
			status.Error = err.Error()
			json.NewEncoder(w).Encode(status)
			return
		}

		status.GAROOnline = true
		status.LoadBalancingFuse =
			rawInt(cfg["loadBalancingFuse"])
		status.LoadBalancingFuse101 =
			rawInt(cfg["loadBalancingFuse101"])

		json.NewEncoder(w).Encode(status)
	})

	http.HandleFunc("/api/safe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}

		if err := garo.SetLoadBalancingFuse(safeCurrentA); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w,
			`{"ok":true,"load_balancing_fuse":%d}`,
			safeCurrentA,
		)
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
	max-width: 700px;
	margin: 40px auto;
	padding: 0 20px;
}
table {
	border-collapse: collapse;
}
td {
	padding: 5px 20px 5px 0;
}
button {
	padding: 8px 14px;
}
#error {
	color: #a00;
}
</style>
</head>

<body>

<h1>GARO Energy Controller</h1>

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
	<td>Safe current</td>
	<td id="safe">...</td>
</tr>
</table>

<p>
<button onclick="applySafe()">
	Restore safe current
</button>
</p>

<p id="error"></p>

<script>
async function refresh() {
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

	document.getElementById("safe").textContent =
		s.safe_current_a + " A";

	document.getElementById("error").textContent =
		s.error || "";
}

async function applySafe() {
	const r = await fetch("/api/safe", {
		method: "POST"
	});

	if (!r.ok) {
		document.getElementById("error").textContent =
			await r.text();
		return;
	}

	await refresh();
}

refresh();
setInterval(refresh, 5000);
</script>

</body>
</html>`
