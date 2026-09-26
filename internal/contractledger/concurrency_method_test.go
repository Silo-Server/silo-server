package contractledger

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	apiv2registry "github.com/Silo-Server/silo-server/internal/apiv2"
)

func TestConcurrencyUsesMappedMethod(t *testing.T) {
	for _, tier := range []int{1, 2} {
		for _, tc := range []struct {
			name    string
			source  string
			target  *string
			allowed bool
		}{
			{"POST to PUT", http.MethodPost, new(http.MethodPut), true},
			{"POST to PATCH", http.MethodPost, new(http.MethodPatch), true},
			{"PUT to POST", http.MethodPut, new(http.MethodPost), false},
			{"DELETE to GET", http.MethodDelete, new(http.MethodGet), false},
			{"unmapped POST", http.MethodPost, nil, false},
			{"unmapped PUT", http.MethodPut, nil, true},
		} {
			t.Run(fmt.Sprintf("%s/tier%d", tc.name, tier), func(t *testing.T) {
				e := Entry{copied: copied{Method: tc.source}, Tier: tier, Disposition: DispositionPorted, Concurrency: ConcurrencyIfMatch, RetrySafety: RetrySafetyNaturalIdempotent}
				if tc.target != nil {
					e.V2 = V2Target{Method: tc.target, Path: new("/api/v2/sections/defaults"), OperationID: new("restoreSections")}
				}
				if got := eligibleForConcurrency(e); got != tc.allowed {
					t.Fatalf("eligibleForConcurrency = %v, want %v", got, tc.allowed)
				}
				problems := reviewRules(e.key(), e, inventoryRoute{})
				if (len(problems) == 0) != tc.allowed {
					t.Fatalf("reviewRules = %v, allowed = %v", problems, tc.allowed)
				}
				fsys := mutatedFS(t, func(doc map[string]any) {
					row := entryWhere(t, doc, func(row map[string]any) bool {
						return row["method"] == tc.source && row["tier"] == float64(tier) && row["disposition"] == DispositionPorted
					})
					row["concurrency"] = ConcurrencyIfMatch
					row["retry_safety"] = RetrySafetyNaturalIdempotent
					delete(row, "retry_safety_note")
					if tc.target == nil {
						row["review_state"] = ReviewProposed
						row["v2"] = map[string]any{"method": nil, "path": nil, "operation_id": nil}
					} else {
						row["v2"] = map[string]any{"method": *tc.target, "path": *e.V2.Path, "operation_id": *e.V2.OperationID}
					}
				})
				if tc.allowed {
					if err := verify(fsys); err != nil {
						t.Fatal(err)
					}
				} else {
					expectFailure(t, fsys, "concurrency")
				}
			})
		}
	}
}

func TestChangedMethodConcurrencyRegistryAgreement(t *testing.T) {
	e := Entry{copied: copied{Method: http.MethodPost, Path: "/api/v1/sections/restore-defaults"}, Tier: 2, Disposition: DispositionPorted, Concurrency: ConcurrencyIfMatch,
		RetrySafety: RetrySafetyNonRetryable, RetrySafetyNote: "Restoring defaults replaces the scope.",
		V2: V2Target{Method: new(http.MethodPut), Path: new("/api/v2/sections/defaults"), OperationID: new("restoreSections")},
	}
	op := apiv2registry.Declared{Method: http.MethodPut, Path: *e.V2.Path, OperationID: *e.V2.OperationID, Guarded: true}
	if got := concurrencyMismatches([]Entry{e}, []apiv2registry.Declared{op}, nil); len(got) != 0 {
		t.Fatal(got)
	}
	unmarked := e
	unmarked.Concurrency = ""
	if got := concurrencyMismatches([]Entry{unmarked}, []apiv2registry.Declared{op}, nil); len(got) != 1 || !strings.Contains(got[0], "is not marked concurrency") {
		t.Fatalf("missing marking accepted: %v", got)
	}
	unguarded := op
	unguarded.Guarded = false
	if got := concurrencyMismatches([]Entry{e}, []apiv2registry.Declared{unguarded}, nil); len(got) != 1 || !strings.Contains(got[0], "is not declared Guarded") {
		t.Fatalf("unguarded target accepted: %v", got)
	}
	wrongMethod := op
	wrongMethod.Method = http.MethodPost
	if got := concurrencyMismatches([]Entry{e}, []apiv2registry.Declared{wrongMethod}, nil); len(got) != 1 || !strings.Contains(got[0], "disagree with the registry") {
		t.Fatalf("wrong target method accepted: %v", got)
	}
	// Retry placement still follows the source method, independent of this change.
	if got := retrySafetyRules(e.key(), e); len(got) != 0 {
		t.Fatal(got)
	}
	e.RetrySafety = ""
	e.RetrySafetyNote = ""
	if got := retrySafetyRules(e.key(), e); len(got) != 1 || !strings.Contains(got[0], "no retry_safety") {
		t.Fatalf("mapped source POST lost retry requirement: %v", got)
	}
}
