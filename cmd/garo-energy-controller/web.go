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
<script src="/assets/status.js"></script>
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
            <div class="note">Saving CENTRAL101 updates GARO only when the configured value differs from the charger. Restore safe current applies DLM100 immediately and pauses automatic DLM changes for at least one normal dwell/control interval; automatic control then resumes.</div>
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
<script src="/assets/settings.js"></script>
</body>
</html>`
