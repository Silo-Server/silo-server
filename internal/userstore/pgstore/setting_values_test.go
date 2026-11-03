package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// countingQueryTracer records every statement pgx sends on behalf of a caller.
type countingQueryTracer struct {
	mu      sync.Mutex
	queries []string
}

func (c *countingQueryTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, data.SQL)
	return ctx
}

func (c *countingQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *countingQueryTracer) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = nil
}

func (c *countingQueryTracer) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.queries...)
}

// TestPostgresResolutionIssuesOneQuery pins the read path's normative rule: a
// batched resolution request costs one query no matter how many scopes, keys or
// content contexts it spans. Five sequential index lookups per key per item is a
// rejected implementation, and nothing else in the suite would notice the
// difference — the returned rows would be identical.
func TestPostgresResolutionIssuesOneQuery(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	tracer := &countingQueryTracer{}
	config.ConnConfig.Tracer = tracer
	// One connection keeps the trace deterministic: a second connection would
	// replay session setup statements into the count.
	config.MaxConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var table *string
	err = pool.QueryRow(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'user_setting_values'`).Scan(&table)
	if errors.Is(err, pgx.ErrNoRows) || table == nil {
		t.Skip("settings contract storage migration has not been applied")
	}
	if err != nil {
		t.Fatalf("check migration: %v", err)
	}

	userID := seedTestUser(t, pool, "conf-onequery")

	store := newStore(pool, userID)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Alice"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	const key = "playback.audio_language"
	identities := []userstore.SettingIdentity{
		{Key: key, Scope: settingscontract.ScopeAccount},
		{Key: key, Scope: settingscontract.ScopeProfile, ProfileID: "p1"},
		{Key: key, Scope: settingscontract.ScopeProfileDevice, ProfileID: "p1", DeviceID: "apple-tv"},
		{Key: key, Scope: settingscontract.ScopeProfileLibrary, ProfileID: "p1", LibraryID: 42},
		{Key: key, Scope: settingscontract.ScopeProfileSeries, ProfileID: "p1", SeriesID: "s-1"},
		{Key: key, Scope: settingscontract.ScopeProfileSeries, ProfileID: "p1", SeriesID: "s-2"},
		{Key: key, Scope: settingscontract.ScopeProfileSeries, ProfileID: "p1", SeriesID: "s-3"},
	}
	for _, id := range identities {
		if _, err := store.UpsertSettingValue(ctx, id, json.RawMessage(`"en"`)); err != nil {
			t.Fatalf("UpsertSettingValue(%+v): %v", id, err)
		}
	}

	tracer.reset()
	rows, err := store.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{
		Keys:       []string{key, "playback.subtitle_mode", "playback.subtitle_language"},
		ProfileIDs: []string{"p1"},
		DeviceID:   "apple-tv",
		LibraryIDs: []int{42, 43},
		SeriesIDs:  []string{"s-1", "s-2", "s-3"},
	})
	if err != nil {
		t.Fatalf("ListSettingValuesForResolution: %v", err)
	}
	if len(rows) != len(identities) {
		t.Fatalf("resolution returned %d rows, want %d", len(rows), len(identities))
	}

	issued := tracer.snapshot()
	if len(issued) != 1 {
		t.Fatalf("resolution issued %d queries, want 1:\n%v", len(issued), issued)
	}
}

func newConstraintTestUser(t *testing.T) (*pgxpool.Pool, int) {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()

	var table *string
	err := pool.QueryRow(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'user_setting_values'`).Scan(&table)
	if errors.Is(err, pgx.ErrNoRows) || table == nil {
		t.Skip("settings contract storage migration has not been applied")
	}
	if err != nil {
		t.Fatalf("check migration: %v", err)
	}

	return pool, seedTestUser(t, pool, "conf-constraints")
}

// testPool connects to SILO_TEST_DATABASE_URL, skipping the test when it is
// not set.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedTestUser inserts an account named after prefix and deletes it, with
// everything that cascades from it, when the test ends.
func seedTestUser(t *testing.T, pool *pgxpool.Pool, prefix string) int {
	t.Helper()
	var userID int
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	return userID
}
