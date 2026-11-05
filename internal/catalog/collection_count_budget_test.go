package catalog

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCollectionCountBudgetSharedLedger(t *testing.T) {
	ctx, cancel := WithCollectionCountBudget(t.Context())
	defer cancel()
	first := ctx.Value(collectionCountBudgetKey{}).(*collectionCountBudget)
	next, nextCancel := WithCollectionCountBudget(ctx)
	defer nextCancel()
	if next.Value(collectionCountBudgetKey{}) != first {
		t.Fatal("owner call reset the ledger")
	}
	deadline, _ := ctx.Deadline()
	nextDeadline, _ := next.Deadline()
	if !deadline.Equal(nextDeadline) || time.Until(deadline) > collectionCountTimeout {
		t.Fatal("owner call reset the deadline")
	}
	for i := range collectionCountMaxDefinitions {
		key := sha256.Sum256(fmt.Appendf(nil, "%d", i))
		result, fresh, err := first.reserve(key, queryMaxRules)
		if err != nil || !fresh {
			t.Fatalf("definition %d: %v", i, err)
		}
		duplicate, fresh, err := first.reserve(key, queryMaxRules)
		if err != nil || fresh || duplicate != result {
			t.Fatalf("duplicate charged again: %v", err)
		}
	}
	if first.rules != collectionCountMaxRules {
		t.Fatalf("rules = %d", first.rules)
	}
	if _, _, err := first.reserve(sha256.Sum256([]byte("next owner")), 0); !errors.Is(err, ErrCollectionCountBudget) {
		t.Fatalf("cross-owner definitions not bounded: %v", err)
	}
	// Exercise rule admission independently of the coincident definition limit.
	otherCtx, otherCancel := WithCollectionCountBudget(t.Context())
	defer otherCancel()
	other := otherCtx.Value(collectionCountBudgetKey{}).(*collectionCountBudget)
	if _, _, err := other.reserve([32]byte{1}, collectionCountMaxRules); err != nil {
		t.Fatal(err)
	}
	if _, _, err := other.reserve([32]byte{2}, 1); !errors.Is(err, ErrCollectionCountBudget) {
		t.Fatalf("cross-call rules not bounded: %v", err)
	}
}

func TestCollectionCountEffectiveDeduplication(t *testing.T) {
	c := PersonalCollectionDefinition{ID: "one", CollectionType: "smart", QueryDefinition: `{"media_scope":"movie","library_ids":[2,1,1],"groups":[{"rules":[{"field":" YEAR ","op":"GTE","value":2000}]}]}`}
	key, rules, err := effectiveCollectionCountKey(1, c, AccessFilter{ProfileID: "one"})
	if err != nil || rules != 1 {
		t.Fatalf("first definition: %v", err)
	}
	alias := c
	alias.ID = "two"
	alias.QueryDefinition = `{"media_scope":"movie","library_ids":[1,2],"groups":[{"match":"all","rules":[{"field":"year","op":"gte","value":2000}]}]}`
	second, _, err := effectiveCollectionCountKey(1, alias, AccessFilter{ProfileID: "one"})
	if err != nil || key != second {
		t.Fatalf("equivalent effective definitions differ: %v", err)
	}
	for _, tc := range []struct {
		account int
		access  AccessFilter
	}{{2, AccessFilter{ProfileID: "one"}}, {1, AccessFilter{ProfileID: "two"}}, {1, AccessFilter{ProfileID: "one", AllowedLibraryIDs: []int{1}}}, {1, AccessFilter{ProfileID: "one", AllowedLibraryIDs: []int{}}}} {
		other, _, err := effectiveCollectionCountKey(tc.account, c, tc.access)
		if err != nil || other == key {
			t.Fatalf("account/access shared a count: %v", err)
		}
	}
	alias.QueryDefinition = `{"library_ids":[` + strings.Repeat("1,", 256) + `1]}`
	if _, _, err := effectiveCollectionCountKey(1, alias, AccessFilter{}); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("deduplication hid raw excess: %v", err)
	}
	c.CollectionType, alias.CollectionType = "manual", "manual"
	c.QueryDefinition, alias.QueryDefinition = "", ""
	manual, _, _ := effectiveCollectionCountKey(1, c, AccessFilter{})
	otherManual, _, _ := effectiveCollectionCountKey(1, alias, AccessFilter{})
	if manual == otherManual {
		t.Fatal("different manual membership shared a count")
	}
}

func TestCollectionCountBatchBounds(t *testing.T) {
	statement := func(sqlBytes, args int) collectionCountStatement {
		return collectionCountStatement{id: "synthetic", sql: strings.Repeat(" ", sqlBytes), args: make([]any, args), result: &collectionCountResult{done: make(chan struct{})}}
	}
	for _, tc := range []struct {
		name    string
		queries []collectionCountStatement
		want    int
	}{
		{"SQL split", []collectionCountStatement{statement(140000, 0), statement(140000, 0)}, 2},
		{"argument split", []collectionCountStatement{statement(1, 4095), statement(1, 4095), statement(1, 1)}, 2},
		{"argument boundary", []collectionCountStatement{statement(1, 8191)}, 1},
		{"oversize SQL", []collectionCountStatement{statement(collectionCountMaxSQLBytes, 0)}, 0},
		{"oversize arguments", []collectionCountStatement{statement(1, 8192)}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobs := buildCollectionCountJobs(t.Context(), nil, 1, tc.queries)
			if len(jobs) != tc.want {
				t.Fatalf("batches = %d, want %d", len(jobs), tc.want)
			}
			for _, job := range jobs {
				if len(job.sql) > collectionCountMaxSQLBytes || len(job.args) > collectionCountMaxArgs || len(job.queries) > personalCollectionCountBatchSize {
					t.Fatal("oversized batch")
				}
			}
			if tc.want == 0 && !errors.Is(tc.queries[0].result.err, ErrCollectionCountBudget) {
				t.Fatal("oversize not classified")
			}
		})
	}
	var queries []collectionCountStatement
	for range 65 {
		queries = append(queries, statement(1, 0))
	}
	if jobs := buildCollectionCountJobs(t.Context(), nil, 1, queries); len(jobs) != 3 {
		t.Fatalf("definition split = %d batches", len(jobs))
	}
	q := collectionCountStatement{id: "parameter", sql: "SELECT $1::int + $2::int", args: []any{1, 2}, result: &collectionCountResult{done: make(chan struct{})}}
	jobs := buildCollectionCountJobs(t.Context(), nil, 1, []collectionCountStatement{q, q})
	if !strings.Contains(jobs[0].sql, "SELECT $4::text, (SELECT $5::int + $6::int)") {
		t.Fatalf("rebind: %s", jobs[0].sql)
	}
	boundary := statement(1, 0)
	boundary.sql = "SELECT 1"
	boundary.sql += strings.Repeat(" ", collectionCountMaxSQLBytes-len(collectionCountSQLPart(boundary, 0)))
	if jobs := buildCollectionCountJobs(t.Context(), nil, 1, []collectionCountStatement{boundary}); len(jobs) != 1 || len(jobs[0].sql) != collectionCountMaxSQLBytes {
		t.Fatal("exact SQL-byte boundary rejected")
	}
}

func TestCollectionCountFailFastAdmissionAndCleanup(t *testing.T) {
	admission := collectionCountAdmissionState{accounts: make(map[int]int), pools: make(map[*pgxpool.Pool]int)}
	pool := &pgxpool.Pool{}
	var releases []func()
	for _, account := range []int{1, 1, 2, 2} {
		release, err := admission.acquire(pool, account, 8)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, err := admission.acquire(pool, 3, 8); !errors.Is(err, ErrCollectionCountBudget) {
		t.Fatalf("node admission: %v", err)
	}
	// Retaining one canceled account lease must still reject a third statement.
	done := make(chan struct{})
	releaseCollectionCountAfterCleanup(done, releases[0])
	releases[2]()
	if _, err := admission.acquire(pool, 1, 8); !errors.Is(err, ErrCollectionCountBudget) {
		t.Fatalf("cleanup released early: %v", err)
	}
	close(done)
	// The completion channel gives an observable release, without sleeping.
	released := make(chan struct{})
	finished := make(chan struct{})
	releaseCollectionCountAfterCleanup(released, func() { releases[1](); close(finished) })
	close(released)
	select {
	case <-finished:
	case <-t.Context().Done():
		t.Fatal("cleanup did not release")
	}
	for _, release := range releases {
		release()
	}
	first, err := admission.acquire(pool, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admission.acquire(pool, 2, 1); !errors.Is(err, ErrCollectionCountBudget) {
		t.Fatalf("pool capacity admission: %v", err)
	}
	first()
}

func TestCollectionCountCanceledBeforeConstruction(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	counts, err := CountPersonalCollections(ctx, nil, 1, []PersonalCollectionDefinition{{CollectionType: "smart", QueryDefinition: "{"}}, AccessFilter{})
	if len(counts) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v %v", counts, err)
	}
	if _, err := executeCollectionCountBatch(ctx, nil, 1, "SELECT 1", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled execution: %v", err)
	}
}

func TestCollectionCountDeadlineKeepsIndependentResults(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	unfinished := &collectionCountResult{done: make(chan struct{})}
	completed := &collectionCountResult{done: make(chan struct{}), count: 7}
	close(completed.done)
	counts := make(map[string]int)
	err := collectCollectionCounts(ctx, []collectionCountReference{{"unfinished", unfinished}, {"completed", completed}, {"alias", completed}}, counts, nil)
	if !errors.Is(err, context.Canceled) || len(counts) != 2 || counts["completed"] != 7 || counts["alias"] != 7 {
		t.Fatalf("deadline hid independent counts: %v %v", counts, err)
	}
}
