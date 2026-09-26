package main

import "testing"

func TestDebugListenerUtilitiesDoNotActivateOrValidateProfiling(t *testing.T) {
	t.Setenv("SILO_DEBUG_LISTEN", "0.0.0.0:6060")
	t.Setenv("SILO_DEBUG_BLOCK_RATE", "1")
	stop, err := startBootstrapDebugListener(false)
	if err != nil || stop == nil {
		t.Fatalf("utility invocation evaluated profiling configuration: %v", err)
	}
	stop()
	if _, err := startBootstrapDebugListener(true); err == nil {
		t.Fatal("serving invocation accepted unsafe profiling configuration")
	}
}
