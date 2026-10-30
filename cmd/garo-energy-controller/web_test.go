package main

import (
	"strings"
	"testing"
)

func TestSettingsPageControlStrategyFields(t *testing.T) {
	for _, key := range []string{
		"control_strategy",
	} {
		// Loaded into the form (t.<key>) and sent back on save (<key>:).
		if !strings.Contains(settingsPage, "t."+key) {
			t.Errorf("settings page does not load %s", key)
		}
		if !strings.Contains(settingsPage, key+":") {
			t.Errorf("settings page does not save %s", key)
		}
	}
}
