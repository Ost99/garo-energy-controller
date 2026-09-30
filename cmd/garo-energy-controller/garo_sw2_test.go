package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecodeGaroSW2MaxPilotAObservedValues(t *testing.T) {
	tests := []struct {
		name     string
		settings int
		wantA    int
	}{
		{"slave 29A", 7689, 29},
		{"slave 25A", 7881, 25},
		{"slave 32A", 7913, 32},
		{"master 32A other DIP state", 7929, 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeGaroSW2MaxPilotA(tt.settings); got != tt.wantA {
				t.Fatalf("decodeGaroSW2MaxPilotA(%d) = %d A, want %d A", tt.settings, got, tt.wantA)
			}
		})
	}
}

func TestDecodeGaroSW2MaxPilotAAllCodes(t *testing.T) {
	want := []int{29, 6, 10, 13, 16, 20, 25, 32}
	const unrelatedBits = 0x1E09 &^ garoSW2CurrentMask

	for code, wantA := range want {
		settings := unrelatedBits | (code << garoSW2CurrentShift)
		if got := decodeGaroSW2MaxPilotA(settings); got != wantA {
			t.Fatalf("SW2 code %03b decoded to %d A, want %d A", code, got, wantA)
		}
	}
}

func TestCapabilityDiscoveryFromObservedFirmwareEndpoints(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"serialNumber":771285,"factoryCurrentLimit":32,"switchCurrentLimit":32,"mainCharger":{"serialNumber":771285,"dipSwitchSettings":7929}}`)
	})
	mux.HandleFunc("/slaves/false", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"serialNumber":771240,"dipSwitchSettings":7913},{"serialNumber":771285,"dipSwitchSettings":7929}]`)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewGaroClient(server.URL)
	caps, err := client.GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities() error = %v", err)
	}
	if got := caps.MaxPilotA(771285); got != 32 {
		t.Fatalf("master max pilot = %d A, want 32 A", got)
	}
	if got := caps.MaxPilotA(771240); got != 32 {
		t.Fatalf("slave max pilot = %d A, want 32 A", got)
	}
}

func TestCapabilityDiscoveryRetriesAfterTransientFailure(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"serialNumber":771285,"factoryCurrentLimit":32,"switchCurrentLimit":32,"mainCharger":{"serialNumber":771285,"dipSwitchSettings":7929}}`)
	})
	mux.HandleFunc("/slaves/false", func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `[{"serialNumber":771240,"dipSwitchSettings":7913},{"serialNumber":771285,"dipSwitchSettings":7929}]`)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewGaroClient(server.URL)
	if _, err := client.GetCapabilities(); err == nil {
		t.Fatal("first GetCapabilities() unexpectedly succeeded")
	}
	if client.capabilitiesLoaded {
		t.Fatal("transient failure must not mark capabilities loaded")
	}

	fail.Store(false)
	client.capabilitiesNextAttempt = time.Time{}

	caps, err := client.GetCapabilities()
	if err != nil {
		t.Fatalf("retry GetCapabilities() error = %v", err)
	}
	if !client.capabilitiesLoaded {
		t.Fatal("successful retry did not mark capabilities loaded")
	}
	if got := caps.MaxPilotA(771240); got != 32 {
		t.Fatalf("slave max pilot after retry = %d A, want 32 A", got)
	}
}

func TestSetLoadBalancingFuseTransientWriteOmitsSlaves(t *testing.T) {
	var posted map[string]json.RawMessage
	mux := http.NewServeMux()
	mux.HandleFunc("/lbconfig/false", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"loadBalancingFuse":14,"loadBalancingFuse101":32,"masterPhase":4,"slaves":[{"serialNumber":771240},{"serialNumber":771285}]}`)
	})
	mux.HandleFunc("/lbconfig", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatalf("decode posted lbconfig: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewGaroClient(server.URL)
	if err := client.SetLoadBalancingFuse(15); err != nil {
		t.Fatalf("SetLoadBalancingFuse() error = %v", err)
	}
	if posted == nil {
		t.Fatal("expected lbconfig POST")
	}
	if _, ok := posted["slaves"]; ok {
		t.Fatal("CENTRAL100 transient write unexpectedly included slaves")
	}
	if got := rawInt(posted["loadBalancingFuse"]); got == nil || *got != 15 {
		t.Fatalf("posted CENTRAL100 = %v, want 15 A", got)
	}
	if got := rawInt(posted["loadBalancingFuse101"]); got == nil || *got != 32 {
		t.Fatalf("posted CENTRAL101 changed = %v, want preserved 32 A", got)
	}
}

func TestSetLoadBalancingFuse101SafeWriteOmitsSlaves(t *testing.T) {
	var posted map[string]json.RawMessage
	mux := http.NewServeMux()
	mux.HandleFunc("/lbconfig/false", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"loadBalancingFuse":14,"loadBalancingFuse101":0,"masterPhase":4,"slaves":[{"serialNumber":771240,"loadBalanced":true},{"serialNumber":771285,"loadBalanced":false}]}`)
	})
	mux.HandleFunc("/lbconfig", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatalf("decode posted lbconfig: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewGaroClient(server.URL)
	if err := client.SetLoadBalancingFuse101(32); err != nil {
		t.Fatalf("SetLoadBalancingFuse101() error = %v", err)
	}
	if posted == nil {
		t.Fatal("expected lbconfig POST")
	}
	if _, ok := posted["slaves"]; ok {
		t.Fatal("CENTRAL101 safe write unexpectedly included slaves")
	}
	if got := rawInt(posted["loadBalancingFuse101"]); got == nil || *got != 32 {
		t.Fatalf("posted CENTRAL101 = %v, want 32 A", got)
	}
	if got := rawInt(posted["loadBalancingFuse"]); got == nil || *got != 14 {
		t.Fatalf("posted CENTRAL100 changed = %v, want preserved 14 A", got)
	}
}

func TestGetFastInfoCapturesLoadBalancedState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"serialNumber":771285,"mode":"ALWAYS_ON","pilotLevel":32,"connector":"CHARGING_FINISHED","factoryCurrentLimit":32,"switchCurrentLimit":32,"mainCharger":{"serialNumber":771285,"dipSwitchSettings":7929}}`)
	})
	mux.HandleFunc("/slaves/false", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"serialNumber":771240,"pilotLevel":6,"connector":"INITIALIZATION","loadBalanced":true,"dipSwitchSettings":7913},{"serialNumber":771285,"pilotLevel":32,"connector":"CHARGING_FINISHED","loadBalanced":false,"dipSwitchSettings":7929}]`)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewGaroClient(server.URL)
	info, err := client.GetFastInfo()
	if err != nil {
		t.Fatalf("GetFastInfo() error = %v", err)
	}
	if len(info.PilotLevels) != 2 {
		t.Fatalf("got %d charger states, want 2", len(info.PilotLevels))
	}

	states := make(map[int]GaroPilotLevel)
	for _, state := range info.PilotLevels {
		states[state.SerialNumber] = state
	}
	if !states[771240].LoadBalancedKnown || !states[771240].LoadBalanced {
		t.Fatalf("771240 load-balanced state = %+v, want known/true", states[771240])
	}
	if !states[771285].LoadBalancedKnown || states[771285].LoadBalanced {
		t.Fatalf("771285 load-balanced state = %+v, want known/false", states[771285])
	}
}

func TestSetLoadBalancingFuse101NoOpDoesNotPost(t *testing.T) {
	var posts atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/lbconfig/false", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"loadBalancingFuse":14,"loadBalancingFuse101":32,"slaves":[{"serialNumber":771240},{"serialNumber":771285}]}`)
	})
	mux.HandleFunc("/lbconfig", func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewGaroClient(server.URL)
	if err := client.SetLoadBalancingFuse101(32); err != nil {
		t.Fatalf("SetLoadBalancingFuse101() error = %v", err)
	}
	if got := posts.Load(); got != 0 {
		t.Fatalf("CENTRAL101 no-op caused %d POSTs, want 0", got)
	}
}
