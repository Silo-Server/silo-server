package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDecisionRepositoryListFiltersAndCursor(t *testing.T) {
	ctx := context.Background()
	pool, _ := newPolicyStoreTest(t, ctx)
	repo := NewDecisionRepository(pool)

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	allowedTrue := true
	allowedFalse := false
	old := insertDecisionLogRow(t, ctx, pool, Entry{
		Timestamp:        base,
		DecisionName:     DecisionScope,
		PolicyGeneration: 1,
		UserID:           intPtr(10),
		Allowed:          nil,
		EvalTimeNS:       100,
		InputDigest:      "digest-old",
	})
	denied := insertDecisionLogRow(t, ctx, pool, Entry{
		Timestamp:        base.Add(time.Minute),
		DecisionName:     DecisionName("silo.action.decision"),
		PolicyGeneration: 1,
		UserID:           intPtr(20),
		Allowed:          &allowedFalse,
		EvalTimeNS:       200,
		InputDigest:      "digest-denied",
		Error:            "denied",
	})
	newest := insertDecisionLogRow(t, ctx, pool, Entry{
		Timestamp:        base.Add(2 * time.Minute),
		DecisionName:     DecisionName("silo.permission.decision"),
		PolicyGeneration: 2,
		UserID:           intPtr(20),
		Allowed:          &allowedTrue,
		EvalTimeNS:       300,
		InputDigest:      "digest-newest",
		InputSample:      []byte(`{"input":true}`),
		ResultSample:     []byte(`{"allowed":true}`),
	})

	firstPage, err := repo.List(ctx, ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("List(first page) error: %v", err)
	}
	if len(firstPage.Entries) != 2 {
		t.Fatalf("first page entries = %d, want 2", len(firstPage.Entries))
	}
	if firstPage.Entries[0].ID != newest.ID || firstPage.Entries[1].ID != denied.ID {
		t.Fatalf("first page order = [%d %d], want [%d %d]",
			firstPage.Entries[0].ID, firstPage.Entries[1].ID, newest.ID, denied.ID)
	}
	if firstPage.NextCursor == "" {
		t.Fatal("first page missing next cursor")
	}

	secondPage, err := repo.List(ctx, ListOptions{Limit: 2, Cursor: firstPage.NextCursor})
	if err != nil {
		t.Fatalf("List(second page) error: %v", err)
	}
	if len(secondPage.Entries) != 1 || secondPage.Entries[0].ID != old.ID {
		t.Fatalf("second page entries = %#v, want old id %d", secondPage.Entries, old.ID)
	}

	filteredByUser, err := repo.List(ctx, ListOptions{UserID: intPtr(20)})
	if err != nil {
		t.Fatalf("List(user filter) error: %v", err)
	}
	if len(filteredByUser.Entries) != 2 {
		t.Fatalf("user-filter entries = %d, want 2", len(filteredByUser.Entries))
	}

	filteredByAllowed, err := repo.List(ctx, ListOptions{Allowed: &allowedFalse})
	if err != nil {
		t.Fatalf("List(allowed filter) error: %v", err)
	}
	if len(filteredByAllowed.Entries) != 1 || filteredByAllowed.Entries[0].ID != denied.ID {
		t.Fatalf("allowed-filter entries = %#v, want denied id %d", filteredByAllowed.Entries, denied.ID)
	}

	filteredByName, err := repo.List(ctx, ListOptions{DecisionName: string(DecisionScope)})
	if err != nil {
		t.Fatalf("List(name filter) error: %v", err)
	}
	if len(filteredByName.Entries) != 1 || filteredByName.Entries[0].ID != old.ID {
		t.Fatalf("name-filter entries = %#v, want old id %d", filteredByName.Entries, old.ID)
	}

	from := base.Add(30 * time.Second)
	to := base.Add(90 * time.Second)
	filteredByTime, err := repo.List(ctx, ListOptions{From: &from, To: &to})
	if err != nil {
		t.Fatalf("List(time filter) error: %v", err)
	}
	if len(filteredByTime.Entries) != 1 || filteredByTime.Entries[0].ID != denied.ID {
		t.Fatalf("time-filter entries = %#v, want denied id %d", filteredByTime.Entries, denied.ID)
	}
}

func TestDecisionRepositoryGet(t *testing.T) {
	ctx := context.Background()
	pool, _ := newPolicyStoreTest(t, ctx)
	repo := NewDecisionRepository(pool)

	inserted := insertDecisionLogRow(t, ctx, pool, Entry{
		Timestamp:        time.Now().UTC().Truncate(time.Microsecond),
		DecisionName:     DecisionScope,
		PolicyGeneration: 3,
		UserID:           intPtr(99),
		EvalTimeNS:       400,
		InputDigest:      "digest-get",
		InputSample:      []byte(`{"user_id":99}`),
	})

	byID, err := repo.Get(ctx, inserted.ID, nil)
	if err != nil {
		t.Fatalf("Get(id) error: %v", err)
	}
	if byID.ID != inserted.ID || byID.InputDigest != "digest-get" || len(byID.InputSample) == 0 {
		t.Fatalf("Get(id) = %#v, want inserted row", byID)
	}

	byPK, err := repo.Get(ctx, inserted.ID, &inserted.Timestamp)
	if err != nil {
		t.Fatalf("Get(id,timestamp) error: %v", err)
	}
	if byPK.ID != inserted.ID {
		t.Fatalf("Get(id,timestamp) id = %d, want %d", byPK.ID, inserted.ID)
	}

	if _, err := repo.Get(ctx, inserted.ID+9999, nil); !errors.Is(err, ErrDecisionNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrDecisionNotFound", err)
	}
}

func insertDecisionLogRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, entry Entry) Entry {
	t.Helper()
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.InputDigest == "" {
		entry.InputDigest = "digest"
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO policy_decisions (
			"timestamp", decision_name, policy_generation, user_id, profile_id,
			session_id, request_id, node_id, allowed, eval_time_ns, input_digest,
			input_sample, result_sample, error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb, $14)
		RETURNING id, "timestamp"
	`,
		entry.Timestamp,
		string(entry.DecisionName),
		entry.PolicyGeneration,
		entry.UserID,
		nullableString(entry.ProfileID),
		nullableString(entry.SessionID),
		nullableString(entry.RequestID),
		nullableString(entry.NodeID),
		entry.Allowed,
		entry.EvalTimeNS,
		entry.InputDigest,
		nullableJSON(entry.InputSample),
		nullableJSON(entry.ResultSample),
		nullableString(entry.Error),
	).Scan(&entry.ID, &entry.Timestamp); err != nil {
		t.Fatalf("insert policy decision row: %v", err)
	}
	return entry
}
