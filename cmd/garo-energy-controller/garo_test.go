package main

import (
	"math"
	"testing"
)

func TestGaroMeterInfoCurrentsAUsesTenthsOfAnAmp(t *testing.T) {
	meter := GaroMeterInfo{
		Phase1Current: 123,
		Phase2Current: -45,
		Phase3Current: 7,
	}

	got1, got2, got3 := meter.CurrentsA()
	want := []float64{12.3, -4.5, 0.7}
	got := []float64{got1, got2, got3}

	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("phase %d current = %v A, want %v A", i+1, got[i], want[i])
		}
	}
}
