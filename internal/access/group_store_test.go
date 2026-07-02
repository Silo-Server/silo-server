package access

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGroupStoreCRUDAndMemberCountsDB(t *testing.T) {
	ctx, pool, store, suffix := newGroupStoreDBTest(t)
	group := createTestGroup(t, ctx, store, suffix, "crud")
	insertAccessGroupTestUser(t, ctx, pool, suffix, &group.ID, 1)
	insertAccessGroupTestUser(t, ctx, pool, suffix, &group.ID, 2)

	got, err := store.Get(ctx, group.ID)
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if got.MemberCount != 2 {
		t.Fatalf("member_count = %d, want 2", got.MemberCount)
	}
	if !reflect.DeepEqual(got.LibraryIDs, []int{1, 3}) {
		t.Fatalf("library_ids = %#v, want [1 3]", got.LibraryIDs)
	}

	groups, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	found := false
	for _, listed := range groups {
		if listed.ID == group.ID {
			found = true
			if listed.MemberCount != 2 {
				t.Fatalf("listed member_count = %d, want 2", listed.MemberCount)
			}
		}
	}
	if !found {
		t.Fatalf("created group %d not found in List()", group.ID)
	}

	description := "updated"
	maxStreams := 1
	updated, err := store.Update(ctx, group.ID, UpdateGroupInput{
		Description: &description,
		MaxStreams:  &maxStreams,
	})
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	if updated.Description != "updated" || updated.MaxStreams != 1 {
		t.Fatalf("updated group = %#v, want description/max_streams update", updated)
	}

	if err := store.Delete(ctx, group.ID); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	var assigned int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM users
		WHERE username LIKE $1 AND access_group_id IS NOT NULL`,
		"access-group-test-"+suffix+"%",
	).Scan(&assigned); err != nil {
		t.Fatalf("count assigned users after delete: %v", err)
	}
	if assigned != 0 {
		t.Fatalf("assigned users after delete = %d, want 0", assigned)
	}
}

func TestGroupStoreGetPolicyForUserDB(t *testing.T) {
	ctx, pool, store, suffix := newGroupStoreDBTest(t)
	group := createTestGroup(t, ctx, store, suffix, "policy")
	memberID := insertAccessGroupTestUser(t, ctx, pool, suffix, &group.ID, 1)
	noGroupID := insertAccessGroupTestUser(t, ctx, pool, suffix, nil, 1)

	policy, err := store.GetPolicyForUser(ctx, memberID)
	if err != nil {
		t.Fatalf("GetPolicyForUser(member) error: %v", err)
	}
	if policy == nil || policy.ID != group.ID || !reflect.DeepEqual(policy.LibraryIDs, []int{1, 3}) {
		t.Fatalf("policy = %#v, want group policy", policy)
	}
	policy, err = store.GetPolicyForUser(ctx, noGroupID)
	if err != nil {
		t.Fatalf("GetPolicyForUser(no group) error: %v", err)
	}
	if policy != nil {
		t.Fatalf("policy = %#v, want nil", policy)
	}
}

func TestGroupStoreQualityUpdateBumpsMemberRevisionsDB(t *testing.T) {
	ctx, pool, store, suffix := newGroupStoreDBTest(t)
	group := createTestGroup(t, ctx, store, suffix, "quality")
	memberID := insertAccessGroupTestUser(t, ctx, pool, suffix, &group.ID, 10)
	nonMemberID := insertAccessGroupTestUser(t, ctx, pool, suffix, nil, 20)

	description := "no revision bump"
	if _, err := store.Update(ctx, group.ID, UpdateGroupInput{Description: &description}); err != nil {
		t.Fatalf("Update(description) error: %v", err)
	}
	if got := accessPolicyRevisionForUser(t, ctx, pool, memberID); got != 10 {
		t.Fatalf("member revision after description update = %d, want 10", got)
	}

	quality := PlaybackQualityStandard
	if _, err := store.Update(ctx, group.ID, UpdateGroupInput{MaxPlaybackQuality: &quality}); err != nil {
		t.Fatalf("Update(max quality) error: %v", err)
	}
	if got := accessPolicyRevisionForUser(t, ctx, pool, memberID); got != 11 {
		t.Fatalf("member revision after quality update = %d, want 11", got)
	}
	if got := accessPolicyRevisionForUser(t, ctx, pool, nonMemberID); got != 20 {
		t.Fatalf("non-member revision after quality update = %d, want 20", got)
	}
}

func newGroupStoreDBTest(t *testing.T) (context.Context, *pgxpool.Pool, *GroupStore, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.access_groups')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check access_groups table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied access groups migration")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1`, "access-group-test-"+suffix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM access_groups WHERE name LIKE $1`, "Access Group Test "+suffix+"%")
	})
	return ctx, pool, NewGroupStore(pool), suffix
}

func createTestGroup(t *testing.T, ctx context.Context, store *GroupStore, suffix, label string) *Group {
	t.Helper()
	group, err := store.Create(ctx, CreateGroupInput{
		Name:                     "Access Group Test " + suffix + " " + label,
		Description:              "test group",
		LibraryIDs:               []int{1, 3},
		MaxPlaybackQuality:       PlaybackQuality4K,
		DownloadAllowed:          true,
		DownloadTranscodeAllowed: true,
		MaxStreams:               3,
		MaxTranscodes:            2,
		AllowedPermissions:       []string{"marker_edit"},
		RequestsAllowed:          true,
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	return group
}

func insertAccessGroupTestUser(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
	groupID *int64,
	revision int64,
) int {
	t.Helper()
	username := fmt.Sprintf("access-group-test-%s-%d", suffix, time.Now().UnixNano())
	var id int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, role, enabled, access_group_id, access_policy_revision)
		VALUES ($1, 'user', true, $2, $3)
		RETURNING id`,
		username,
		groupID,
		revision,
	).Scan(&id); err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	return id
}

func accessPolicyRevisionForUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int) int64 {
	t.Helper()
	var revision int64
	if err := pool.QueryRow(ctx, `
		SELECT access_policy_revision
		FROM users
		WHERE id = $1`, userID).Scan(&revision); err != nil {
		t.Fatalf("load access_policy_revision for user %d: %v", userID, err)
	}
	return revision
}
