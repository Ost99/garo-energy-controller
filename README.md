# GARO Energy Controller

Small Go service for controlling GARO GLB dynamic load balancing based on hourly grid import measured by Tibber.

## Build

```bash
go build -o garo-energy-controller
```

For the GARO Raspberry Pi:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o garo-energy-controller
```

## Configuration

Configuration:

```text
/etc/garo-energy-controller/config.json
```

Tibber credentials:

```text
/etc/garo-energy-controller/secrets.json
```

Example:

```json
{
  "tibber_token": "YOUR_TOKEN"
}
```

## Run

```bash
./garo-energy-controller
```

The web interface is available on port `8090`.
