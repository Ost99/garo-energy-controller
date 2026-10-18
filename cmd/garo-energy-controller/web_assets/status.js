
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
