package main

import (
	"context"
	"flag"
	"log"
	"net/http"
)

const listenAddress = ":8090"

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

	garoCache := NewGaroCache()
	// Prime in-memory values before the controller starts. Browser status
	// requests read this cache and never call the GARO API directly.
	garoCache.RefreshFast(garo)

	// The startup mode and CENTRAL101 are not written here: at boot this
	// service starts minutes before GARO's SerialService is ready. The
	// controller applies them on its first loop once GARO is ready
	// (garo_settings.go).
	go garoCache.RunFast(context.Background(), garo)

	tibber := NewTibberClient(secrets.TibberToken)
	go tibber.Run(context.Background(), store.Get)

	controller := NewEnergyController(
		garo,
		garoCache,
		tibber,
		store.Get,
	)
	go controller.Run(context.Background())

	handler := NewHTTPHandler(store, garo, garoCache, tibber, controller)

	log.Printf("GARO Energy Controller starting on %s", listenAddress)
	if err := http.ListenAndServe(listenAddress, handler); err != nil {
		log.Fatal(err)
	}
}
