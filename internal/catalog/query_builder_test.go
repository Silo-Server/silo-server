package catalog

import (
	"testing"
)

func TestParseDurationRejectsMalformedInput(t *testing.T) {
	if _, err := parseDuration("1'; SELECT 1; --d"); err == nil {
		t.Fatal("expected malformed duration to be rejected")
	}
	if got, err := parseDuration("2Y"); err != nil || got != "2 years" {
		t.Fatalf("parseDuration(2Y) = %q, %v; want 2 years", got, err)
	}
}
