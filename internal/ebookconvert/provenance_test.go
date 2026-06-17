package ebookconvert

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// The committed mobitool.wasm must match the sha256 recorded next to it. This
// is the cheap half of the artifact guardrail: together with the real fixture
// conversions in converter_test.go (the "smoke test"), it ensures a careless
// artifact swap or a corrupted checkout is caught in CI instead of shipping a
// mystery binary. The sidecar is sha256sum format (`<hex>  <path>`); we compare
// only the hash, since the recorded path is build-environment specific.
func TestEmbeddedWasmMatchesRecordedSHA256(t *testing.T) {
	raw, err := os.ReadFile("mobitool.wasm.sha256")
	if err != nil {
		t.Fatalf("read sha256 sidecar: %v", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		t.Fatal("mobitool.wasm.sha256 is empty")
	}
	want := strings.ToLower(fields[0])

	sum := sha256.Sum256(mobitoolWasm)
	got := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("embedded mobitool.wasm sha256 = %s, recorded = %s — artifact and provenance disagree", got, want)
	}
}
