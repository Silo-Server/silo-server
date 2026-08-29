package nodepool

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fakeRow exercises scanNode without a database. pgx.Row is a single Scan
// method, so a stored capability payload can be handed to the real row scanner
// by writing into the destination pointers pgx would have filled.
type fakeRow struct {
	capabilities []byte
}

func (r fakeRow) Scan(dest ...any) error {
	if want := strings.Count(nodeColumns, ",") + 1; len(dest) != want {
		return fmt.Errorf("scanNode passed %d destinations for %d columns in nodeColumns", len(dest), want)
	}
	// capabilities is the first of the two jsonb columns scanned as raw bytes;
	// last_stats is the second. Everything else keeps its zero value.
	for _, d := range dest {
		if raw, ok := d.(*[]byte); ok {
			*raw = r.capabilities
			return nil
		}
	}
	return errors.New("scanNode has no raw-bytes destination for capabilities")
}

// The admin node list is served straight from stored rows, so scanNode is the
// only producer of the physical_gpu_keys an operator sees. Losing the
// derivation there empties every Shared GPU badge silently, because the pools
// derive their own keys and keep routing correctly.
func TestScanNodeDerivesPhysicalGPUKeys(t *testing.T) {
	n, err := scanNode(fakeRow{capabilities: []byte(gpuAAACapabilities)})
	if err != nil {
		t.Fatalf("scanNode: %v", err)
	}
	if got := n.PhysicalGPUKeys; !slices.Equal(got, []string{"GPU-aaa"}) {
		t.Fatalf("scanned row derived %v, want [GPU-aaa]", got)
	}
	if string(n.Capabilities) != gpuAAACapabilities {
		t.Fatalf("stored payload was not carried through: %s", n.Capabilities)
	}

	// A node that never reported capabilities carries no identities rather
	// than empty ones a client would have to special-case.
	bare, err := scanNode(fakeRow{})
	if err != nil {
		t.Fatalf("scanNode: %v", err)
	}
	if bare.Capabilities != nil || bare.PhysicalGPUKeys != nil {
		t.Fatalf("row without a stored report derived %v", bare.PhysicalGPUKeys)
	}
}
