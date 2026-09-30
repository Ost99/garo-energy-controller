package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web_assets/*
var embeddedWebAssets embed.FS

func webAssetsHandler() http.Handler {
	assets, err := fs.Sub(embeddedWebAssets, "web_assets")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(assets))
}

const statusPage = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GARO Energy Controller</title>
<link rel="stylesheet" href="/garo-jquery/jquery.mobile.icons.min.css">
<link rel="icon" sizes="192x192" href="/garo-assets/garologo.png">
<link rel="shortcut icon" href="/garo-assets/garologo.png">
<link rel="apple-touch-icon" href="/garo-assets/garologo.png">
<link rel="stylesheet" href="/assets/app.css">
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
                <div class="data-line"><span class="data-label">Charger load balancing:</span> <span id="chargerLoadBalancing">...</span></div>
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
            <div class="status-item charging-only"><span class="label">Control source</span><span id="controlSource">...</span></div>
            <div class="status-item"><span class="label">Grid import now</span><span id="tibberPower">...</span></div>
            <div class="status-item"><span class="label">Grid export now</span><span id="tibberProduction">...</span></div>
            <div class="status-item"><span class="label">Imported this hour</span><span id="tibberHour">...</span></div>
            <div class="status-item charging-only"><span class="label">Controller hour energy</span><span id="hourEnergy">...</span></div>
            <div class="status-item charging-only"><span class="label">Expected energy by now</span><span id="expectedHourEnergy">...</span></div>
            <div class="status-item charging-only"><span class="label">Energy pacing error</span><span id="pacingError">...</span></div>
            <div class="status-item charging-only"><span class="label">Remaining allowance</span><span id="remainingEnergy">...</span></div>
            <div class="status-item charging-only"><span class="label">Base target</span><span id="baseTargetPower">...</span></div>
            <div class="status-item charging-only"><span class="label">Pacing correction</span><span id="pacingCorrection">...</span></div>
            <div class="status-item charging-only"><span class="label">Pacing target</span><span id="pacingTarget">...</span></div>
            <div class="status-item charging-only"><span class="label">Hard budget ceiling</span><span id="hardBudgetCeiling">...</span></div>
            <div class="status-item charging-only"><span class="label">Effective target</span><span id="allowedPower">...</span></div>
            <div class="status-item charging-only"><span class="label">Power headroom</span><span id="powerHeadroom">...</span></div>
            <div class="status-item charging-only"><span class="label">Last DLM adjustment</span><span id="lastAdjustment">...</span></div>
            <div class="status-item"><span class="label">Tibber data age</span><span id="tibberAge">...</span></div>
            <div class="status-item"><span class="label">Tibber API requests</span><span id="tibberApiRequests">...</span></div>
            <div class="status-item"><span class="label">Last API request</span><span id="tibberApiLast">...</span></div>
            <div class="status-item"><span class="label">Last API result</span><span id="tibberApiResult">...</span></div>
        </div>
    </section>

    <p id="error"></p>
</div>

<script src="/assets/common.js"></script>
<script>

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
    return pilots.map(function (p) {
        const current = p.max_pilot_a ? p.pilot_a + "/" + p.max_pilot_a + " A" : p.pilot_a + " A";
        return p.serial_number + ": " + current;
    }).join(" / ") + staleSuffix(c.pilot_stale);
}

function formatChargerLoadBalancing(c) {
    const pilots = c.pilot_levels || [];
    if (!c.pilot_valid || pilots.length === 0) {
        return "Unknown";
    }
    return pilots.map(function (p) {
        let state = "Unknown";
        if (p.load_balanced_known) {
            state = p.load_balanced ? "Yes" : "NO";
        }
        return p.serial_number + ": " + state;
    }).join(" / ") + staleSuffix(c.pilot_stale);
}

async function refreshStatus() {
    try {
        const r = await fetch("/api/status", {cache: "no-store"});
        const s = await r.json();
        const t = s.tibber || {};
        const c = s.controller || {};


        const charging = c.central101_valid && c.charging;

        document.querySelectorAll(".charging-only").forEach(function (row) {
            row.hidden = !charging;
        });
        setText("garo", s.garo_online ? "Online" : "Offline");
        setText("fuse100", s.load_balancing_fuse !== undefined ? s.load_balancing_fuse + " A" + staleSuffix(c.dlm_config_stale) : "-");
        setText("fuse101", s.load_balancing_fuse_101 !== undefined ? s.load_balancing_fuse_101 + " A" + staleSuffix(c.dlm_config_stale) : "-");
        setText("controllerStatus", s.enabled ? s.mode : "Disabled");
        setText("chargeAvailability", s.charge_mode_valid ? formatChargeMode(s.charge_mode) + staleSuffix(s.charge_mode_stale) : "-");
        setText("charging", c.central101_valid ? (c.charging ? "Yes" : "No") + staleSuffix(c.central101_stale) : "-");
        setText("phaseMode", c.central101_valid ? (c.phase_mode || "-") + staleSuffix(c.central101_stale) : "-");
        setText("pilotCurrents", formatPilotCurrents(c));
        setText("chargerLoadBalancing", formatChargerLoadBalancing(c));
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
<link rel="icon" sizes="192x192" href="/garo-assets/garologo.png">
<link rel="shortcut icon" href="/garo-assets/garologo.png">
<link rel="apple-touch-icon" href="/garo-assets/garologo.png">
<link rel="stylesheet" href="/assets/app.css">
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
            <div class="note">Availability is a group-level GARO mode. Manual availability changes pause automatic DLM writes for one normal dwell period.</div>
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
            <div class="note">Saving CENTRAL101 updates GARO only when the configured value differs from the charger. Restore safe current pauses automatic DLM writes for one normal dwell period.</div>
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

<script src="/assets/common.js"></script>
<script>

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
