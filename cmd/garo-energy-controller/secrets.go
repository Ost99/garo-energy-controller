package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type Secrets struct {
	TibberToken string `json:"tibber_token"`
}

func loadSecrets(path string) (Secrets, error) {
	var secrets Secrets

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return secrets, nil
	}
	if err != nil {
		return secrets, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&secrets); err != nil {
		return secrets, fmt.Errorf("parse secrets: %w", err)
	}

	return secrets, nil
}
