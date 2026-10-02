package access

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func guardedGroupStore(t *testing.T) *GroupStore {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("group_guard_%d", time.Now().UnixNano())
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+q+" CASCADE"); admin.Close() })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `CREATE TABLE access_groups (LIKE public.access_groups INCLUDING ALL); ALTER TABLE access_groups DROP COLUMN IF EXISTS configuration_revision; CREATE TABLE users(id bigint PRIMARY KEY,access_group_id bigint REFERENCES access_groups(id) ON DELETE SET NULL,access_policy_revision bigint NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906000909_add_access_group_configuration_revisions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, strings.Split(string(migration), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	return NewGroupStore(pool)
}
func TestGroupGuardAtomicLegacyAndGeneration(t *testing.T) {
	s := guardedGroupStore(t)
	ctx := t.Context()
	g, err := s.Create(ctx, CreateGroupInput{Name: "Group"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for n := range 6 {
		wg.Go(func() {
			_, err := s.UpdateConditional(ctx, g.ID, UpdateGroupInput{Description: new(fmt.Sprintf("writer-%d", n))}, GroupPrecondition{Revision: g.Revision})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrGroupRevisionConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("winners=%d", wins)
	}
	current, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := s.Update(ctx, g.ID, UpdateGroupInput{DownloadAllowed: new(false), LibraryIDs: new([]int{})})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Revision <= current.Revision || legacy.LibraryIDs == nil || legacy.DownloadAllowed {
		t.Fatal("legacy writer lost revision/empty/false")
	}
	if err = s.DeleteConditional(ctx, g.ID, GroupPrecondition{Revision: current.Revision}); !errors.Is(err, ErrGroupRevisionConflict) {
		t.Fatalf("stale delete=%v", err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO users(id,access_group_id) VALUES(1,$1)`, g.ID); err != nil {
		t.Fatal(err)
	}
	usage, err := s.Get(ctx, g.ID)
	if err != nil || usage.MemberCount != 1 || usage.Revision != legacy.Revision {
		t.Fatal("membership changed editor revision", err)
	}
	if err = s.DeleteConditional(ctx, g.ID, GroupPrecondition{Any: true}); err != nil {
		t.Fatal(err)
	}
	var linked *int64
	if err = s.pool.QueryRow(ctx, `SELECT access_group_id FROM users WHERE id=1`).Scan(&linked); err != nil || linked != nil {
		t.Fatal("membership FK not cleared", err)
	}
	var revision int64
	if err = s.pool.QueryRow(ctx, `INSERT INTO access_groups(id,name) OVERRIDING SYSTEM VALUE VALUES($1,'Recreated') RETURNING configuration_revision`, g.ID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision <= usage.Revision {
		t.Fatal("recreated generation did not advance")
	}
	if err = s.DeleteConditional(ctx, g.ID, GroupPrecondition{Revision: usage.Revision}); !errors.Is(err, ErrGroupRevisionConflict) {
		t.Fatal("recreated row accepted stale guard", err)
	}
}
func TestGroupGuardDefaultAndPaging(t *testing.T) {
	s := guardedGroupStore(t)
	ctx := t.Context()
	a, err := s.Create(ctx, CreateGroupInput{Name: "Default", IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, CreateGroupInput{Name: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteConditional(ctx, a.ID, GroupPrecondition{Revision: a.Revision}); !errors.Is(err, ErrDefaultGroupRequired) {
		t.Fatal(err)
	}
	if _, err = s.UpdateConditional(ctx, a.ID, UpdateGroupInput{IsDefault: new(false)}, GroupPrecondition{Any: true}); !errors.Is(err, ErrDefaultGroupRequired) {
		t.Fatal(err)
	}
	promoted, err := s.UpdateConditional(ctx, b.ID, UpdateGroupInput{IsDefault: new(true)}, GroupPrecondition{Revision: b.Revision})
	if err != nil || !promoted.IsDefault {
		t.Fatal(err)
	}
	old, err := s.Get(ctx, a.ID)
	if err != nil || old.IsDefault || old.Revision <= a.Revision {
		t.Fatal("sibling demotion not revisioned", err)
	}
	if _, err = s.UpdateConditional(ctx, a.ID, UpdateGroupInput{IsDefault: new(true)}, GroupPrecondition{Revision: a.Revision}); !errors.Is(err, ErrGroupRevisionConflict) {
		t.Fatal("stale promotion allowed", err)
	}
	still, err := s.Get(ctx, b.ID)
	if err != nil || !still.IsDefault || still.Revision != promoted.Revision {
		t.Fatal("stale write modified default", err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO access_groups(name) SELECT 'Paged-'||i FROM generate_series(1,205)i`); err != nil {
		t.Fatal(err)
	}
	page, more, err := s.ListPage(ctx, nil, 1000)
	if err != nil || len(page) != 200 || !more {
		t.Fatalf("page len=%d more=%v err=%v", len(page), more, err)
	}
	tail, more, err := s.ListPage(ctx, &GroupPageKey{ID: page[len(page)-1].ID}, 200)
	if err != nil || more || len(tail) != 7 {
		t.Fatalf("tail len=%d more=%v err=%v", len(tail), more, err)
	}
	for _, guard := range []GroupPrecondition{{}, {Revision: -1}, {Any: true, Revision: 1}} {
		if _, err = s.UpdateConditional(ctx, b.ID, UpdateGroupInput{}, guard); !errors.Is(err, ErrGroupInvalidPrecondition) {
			t.Fatal(err)
		}
	}
}

func TestGroupGuardPromotionRollbackAndConcurrentDefaults(t *testing.T) {
	s := guardedGroupStore(t)
	ctx := t.Context()
	original, err := s.Create(ctx, CreateGroupInput{Name: "Original", IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(ctx, CreateGroupInput{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	// A duplicate-name failure after clearing the sibling must roll that change back.
	_, err = s.UpdateConditional(ctx, other.ID, UpdateGroupInput{Name: new("Original"), IsDefault: new(true)}, GroupPrecondition{Revision: other.Revision})
	if !errors.Is(err, ErrGroupDuplicate) {
		t.Fatal(err)
	}
	current, err := s.Get(ctx, original.ID)
	if err != nil || !current.IsDefault || current.Revision != original.Revision {
		t.Fatal("failed promotion changed sibling", err)
	}
	third, err := s.Create(ctx, CreateGroupInput{Name: "Third"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, g := range []*Group{other, third} {
		wg.Go(func() {
			_, err := s.UpdateConditional(ctx, g.ID, UpdateGroupInput{IsDefault: new(true)}, GroupPrecondition{Revision: g.Revision})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	// Both different-row edits may serialize successfully, but exactly one default
	// remains and the first winner's subsequent demotion advances its revision.
	for err := range results {
		if err != nil && !errors.Is(err, ErrGroupRevisionConflict) {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM access_groups WHERE is_default`).Scan(&count); err != nil || count != 1 {
		t.Fatal("default invariant", count, err)
	}
}
