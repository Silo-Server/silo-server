package contractledger

import "testing"

// TestOperationalMetricsIsOutsideNativeMigrationDecisions mirrors the
// profiler exclusion: /metrics on the opt-in metrics listener needs no ledger
// entry, for every ServeMux method variant.
func TestOperationalMetricsIsOutsideNativeMigrationDecisions(t *testing.T) {
	fsys := mutatedFSWithInventory(t, nil, func(doc map[string]any) {
		doc["routes"] = append(doc["routes"].([]any), map[string]any{"listener": "operational_metrics", "method": "GET", "path": "/metrics"})
	})
	if err := verify(fsys); err != nil {
		t.Fatal(err)
	}
}

// TestOperationalMetricsExclusionCannotHideOtherRoutes pins the exclusion
// to its exact listener, path, and method set. The root listener's own
// /metrics row stays a native decision.
func TestOperationalMetricsExclusionCannotHideOtherRoutes(t *testing.T) {
	for _, tc := range []struct{ listener, method, path string }{
		{"api", "GET", "/metrics"},
		{"operational_metrics", "GET", "/api/v2/hidden"},
		{"operational_metrics", "GET", "/metrics/extra"},
		{"operational_metrics", "BREW", "/metrics"},
	} {
		t.Run(tc.listener+tc.method+tc.path, func(t *testing.T) {
			fsys := mutatedFSWithInventory(t, nil, func(doc map[string]any) {
				doc["routes"] = append(doc["routes"].([]any), map[string]any{"listener": tc.listener, "method": tc.method, "path": tc.path})
			})
			expectFailure(t, fsys, "inventory row has no ledger entry")
		})
	}
}
