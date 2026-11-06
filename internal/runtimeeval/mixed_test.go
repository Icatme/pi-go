package runtimeeval

import "testing"

func TestMixedRuntimeCancellationConservation(t *testing.T) {
	for _, mode := range loadModes {
		t.Run(mode, func(t *testing.T) { runMixedAcceptance(t, mode, false) })
	}
}

func TestMixedRuntimeCrashConservation(t *testing.T) {
	for _, mode := range loadModes {
		t.Run(mode, func(t *testing.T) { runMixedAcceptance(t, mode, true) })
	}
}
