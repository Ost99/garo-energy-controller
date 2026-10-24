
# GARO Energy Controller

Small Go service for controlling GARO GLB dynamic load balancing based on hourly grid import measured by Tibber.

<img width="1216" height="984" alt="Garo-main-page" src="https://github.com/user-attachments/assets/b8fb3c3d-cf69-478b-af44-707312a3cdf5" />

<img width="1223" height="1013" alt="Garo-settings-page" src="https://github.com/user-attachments/assets/5d27ad7b-f350-43c6-8fc6-11942e3cd9f7" />


## How it works

The controller runs on the GARO's own Raspberry Pi and talks to the charger's local REST API (`http://127.0.0.1:8080`). Every control cycle it reads the energy imported so far this hour from Tibber (Pulse real-time data) and raises or lowers the GARO dynamic load balancing (DLM) current so the hour ends just under `hourly_limit_kwh`.

* **Target.** Regulation aims `target_margin_wh` below the hourly limit. The limit itself is absolute.
* **Hard stop.** If a late-hour load spike makes it impossible to stay under the limit by ramping down, charging is switched off until the next hour. The hard-stop state is the only thing the controller writes to disk during normal operation (`/var/lib/garo-energy-controller/hard-stop`), so it survives a restart.
* **Tibber outage.** If Tibber data is older than `tibber_timeout_seconds`, the controller falls back to integrating the GARO CENTRAL100 meter (total household consumption). This ignores solar production, so it is deliberately conservative.
* **CENTRAL101.** The charger sub-fuse limit is owned by this controller's config (`load_balancing_fuse_101_a`) and written to GARO when it differs.
* **SD card.** GARO API calls that cause SD-card writes on the Pi are kept to a minimum.

## Build

From the repository root:

```bash
go build -o garo-energy-controller ./cmd/garo-energy-controller
```

For the GARO Raspberry Pi:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o garo-energy-controller ./cmd/garo-energy-controller
```

Run the tests on the build host (not cross-compiled):

```bash
go test ./...
```

## Install

Copy the binary to `/usr/local/bin/garo-energy-controller` on the GARO and install it as a systemd service, for example `/etc/systemd/system/garo-energy-controller.service`:

```ini
[Unit]
Description=GARO Energy Controller
After=network.target cron.service

[Service]
Type=simple
User=root
Group=root

StartLimitInterval=60

# Wait (up to ~2 minutes) for the GARO REST API to come up after boot.
ExecStartPre=/bin/sh -c 'i=0; while [ "$i" -lt 60 ]; do /usr/bin/curl -fsS http://127.0.0.1:8080/servlet/rest/chargebox/lbconfig/false >/dev/null 2>&1 && exit 0; i=$((i+1)); sleep 2; done; exit 0'

ExecStart=/usr/local/bin/garo-energy-controller -config /etc/garo-energy-controller/config.json -secrets /etc/garo-energy-controller/secrets.json

Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now garo-energy-controller
```

## Command-line flags

| Flag | Default | Description |
|---|---|---|
| `-config` | `/etc/garo-energy-controller/config.json` | Configuration file. Created by the settings page if missing; built-in defaults are used until then. |
| `-secrets` | `/etc/garo-energy-controller/secrets.json` | Tibber credentials. |
| `-tls-cert` | (none) | PEM certificate chain. Enables HTTPS together with `-tls-key`. |
| `-tls-key` | (none) | PEM private key. |

The web interface listens on port `8090`.

## Secrets

`secrets.json`:

```json
{
  "tibber_token": "YOUR_TOKEN"
}
```

## Configuration

`config.json` is normally edited from the settings page (`/settings`); changes are validated and saved immediately. Fields that are left out of the file keep their defaults.

| Field | Default | Description |
|---|---|---|
| `enabled` | `true` | Master switch. When `false` the controller leaves DLM alone (and releases an active hard stop). |
| `mode` | `"safe"` | `automatic`: regulate against the hourly limit. `safe`: fixed DLM at `safe_current_a`. `manual`: fixed DLM at `manual_current_a`. |
| `hourly_limit_kwh` | `9.5` | Absolute hourly grid import limit. |
| `target_margin_wh` | `75` | How far below the limit regulation aims (0–1000). |
| `safe_current_a` | `8` | DLM current in safe mode and when automatic mode is idle. |
| `minimum_current_a` | `6` | Lowest DLM current the controller sets (at least 6). |
| `maximum_current_a` | `50` | Highest DLM current the controller sets; normally the main fuse. |
| `manual_current_a` | `8` | DLM current in manual mode. |
| `load_balancing_fuse_101_a` | `32` | CENTRAL101 (charger sub-fuse) limit written to GARO (16–2500). |
| `tibber_timeout_seconds` | `30` | Tibber data older than this triggers the CENTRAL100 fallback. |
| `control_interval_seconds` | `30` | Control cycle interval. |
| `idle_timeout_seconds` | `120` | In automatic mode, how long the charger must be idle before DLM returns to `safe_current_a`. |
| `tibber_home_id` | (empty) | Only needed if the Tibber account has more than one home with real-time data. |
| `control_tuning` | see below | Regulation tuning. |

### Control tuning

The defaults are tuned for the author's installation and are editable on the settings page.

* `control_strategy` (`"incremental"`): `incremental` is the default step controller. `model` is a model-based PI controller that is still under test and uses `model_reserve_fraction` (`0.11`), `model_kp` (`0.5`) and `model_integral_seconds` (`300`).
* Incremental controller thresholds, separately for one-phase and multi-phase charging: `*_up_gate_w`, `*_two_amp_threshold_w`, `*_calculated_threshold_w`, `*_max_calculated_step_a` and `*_watts_per_amp`.
* Ramp-down: `down_deadband_w` (`300`) and `urgent_down_w` (`1500`).
* Pacing across the hour: `pacing_horizon_seconds` (`1800`), `max_pacing_adjustment_w` (`2500`) and `catch_up_fade_seconds` (`3600`), which fades out "catch-up" pacing towards the end of the hour.
* Minimum time between DLM changes: `normal_dwell_seconds` (`60`), `mid_up_dwell_seconds` (`90`) and `near_up_dwell_seconds` (`120`).
* `calculated_reserve_w` (`1000`) and `unused_dlm_headroom_a` (`1.0`).

## Web interface and API

| Path | Description |
|---|---|
| `/` | Status page. |
| `/settings` | Settings page. |
| `GET /api/status` | Current state as JSON. |
| `GET`/`POST /api/config` | Read or save the configuration. |
| `POST /api/safe` | Set DLM to `safe_current_a` immediately. |
| `POST /api/charge-mode` | Set the GARO charge mode, `{"mode": "ALWAYS_ON"}`, `ALWAYS_OFF` or `SCHEMA`. |

The status page never calls the GARO API directly; it reads values the controller has already cached.

### HTTPS

Pass a PEM certificate chain and private key to serve HTTPS on the same port, for example by adding them to `ExecStart` in the service file:

```bash
/usr/local/bin/garo-energy-controller \
  -config /etc/garo-energy-controller/config.json \
  -secrets /etc/garo-energy-controller/secrets.json \
  -tls-cert /etc/garo-energy-controller/tls/fullchain.pem \
  -tls-key  /etc/garo-energy-controller/tls/privkey.pem
```

Certificate renewal is picked up automatically on the next connection; no restart is needed. Without these flags, or if the certificate cannot be loaded at startup, the server uses plain HTTP so energy control keeps running. Keep `privkey.pem` at mode `0600`.


## TODO
* Implement controls for scheduled availability

## Notice
* No icons, logos or images are included in the project, they are read from the running GARO firmware at runtime.
* All testing done with firmware version 1.3.8.
* Root access gained by adding a public key to authorized_keys for root by adapting the method described here: https://github.com/Yof3ng/IoT/blob/master/Garo/CVE-2023-30399.md
