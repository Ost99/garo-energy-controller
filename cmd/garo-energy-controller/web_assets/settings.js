
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
