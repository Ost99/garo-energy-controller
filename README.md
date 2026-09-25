# GARO Energy Controller

Small Go service for controlling GARO GLB dynamic load balancing based on hourly grid import measured by Tibber.

<img width="1186" height="319" alt="image" src="https://github.com/user-attachments/assets/5755256d-10f7-4231-b2e3-0e04f1b9a9f7" />

<img width="1188" height="299" alt="image" src="https://github.com/user-attachments/assets/3484e001-c633-4df5-b70b-a2bdbbe0487f" />

<img width="1188" height="270" alt="image" src="https://github.com/user-attachments/assets/9b9f991f-ec98-4440-8fd3-37b0eaca89d0" />

<img width="1183" height="600" alt="image" src="https://github.com/user-attachments/assets/67f93073-6318-4a27-be11-88d7815c9480" />



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


## TODO
* Stop charging if target is likely to be overshot
* Add favicon from filesystem
* Implement controls for schduled availability 

## Notice
* No icons, logos or images are included in the project, they are read from the running GARO firmware at runtime.
* All testing done with firmware version 1.3.8.
* Root access gained by adding a public key to authorized_keys for root by adapting the method described here: https://github.com/Yof3ng/IoT/blob/master/Garo/CVE-2023-30399.md
