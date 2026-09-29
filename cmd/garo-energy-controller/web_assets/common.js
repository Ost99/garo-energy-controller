function setText(id, value) {
    const element = document.getElementById(id);
    if (!element) {
        throw new Error("missing DOM element #" + id);
    }
    element.textContent = value;
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
