package nfo

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// Importing the nfo package must register the provider in the builtin
// registry under its capability id — buildProviders resolves the seeded
// 'nfo' chain entry through this single registration point.
func TestPackageImportRegistersBuiltinProvider(t *testing.T) {
	if !slices.Contains(metadata.BuiltinCapabilityIDs(), "nfo") {
		t.Fatalf("BuiltinCapabilityIDs() = %v, want to contain nfo", metadata.BuiltinCapabilityIDs())
	}
}
