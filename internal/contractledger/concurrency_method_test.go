package contractledger

import (
	"fmt"
	"net/http"
	"testing"
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
