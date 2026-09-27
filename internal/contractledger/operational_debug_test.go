package contractledger

import "testing"

func TestOperationalProfilerIsOutsideNativeMigrationDecisions(t *testing.T) {
	fsys := mutatedFSWithInventory(t, nil, func(doc map[string]any) {
		doc["routes"] = append(doc["routes"].([]any), map[string]any{"listener": "operational_debug", "method": "GET", "path": "/debug/pprof/heap"})
	})
	if err := verify(fsys); err != nil {
		t.Fatal(err)
	}
}

func TestOperationalProfilerExclusionCannotHideOtherRoutes(t *testing.T) {
	for _, tc := range []struct{ listener, method, path string }{
		{"api", "GET", "/debug/pprof/heap"},
		{"root", "GET", "/debug/pprof/heap"},
		{"operational_debug", "GET", "/api/v2/hidden"},
		{"operational_debug", "GET", "/debug/pprof/cmdline"},
		{"operational_debug", "BREW", "/debug/pprof/heap"},
	} {
		t.Run(tc.listener+tc.method+tc.path, func(t *testing.T) {
			fsys := mutatedFSWithInventory(t, nil, func(doc map[string]any) {
				doc["routes"] = append(doc["routes"].([]any), map[string]any{"listener": tc.listener, "method": tc.method, "path": tc.path})
			})
			expectFailure(t, fsys, "inventory row has no ledger entry")
		})
	}
}
