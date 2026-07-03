package access

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

func TestDefaultAccessGroupSeedAndUniqueDB(t *testing.T) {
	ctx, pool, store, suffix := newGroupStoreDBTest(t)
	seedID := defaultAccessGroupSeedID(t, ctx, pool)
	t.Cleanup(func() {
		restoreDefaultAccessGroup(t, ctx, pool, seedID)
	})

	assertDefaultGroupSeed(t, ctx, pool)

	_, err := pool.Exec(ctx, `
		INSERT INTO access_groups (name, is_default)
		VALUES ($1, true)`,
		"Access Group Test "+suffix+" second default",
	)
	if err == nil {
		t.Fatal("second default access group insert succeeded, want unique violation")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second default insert error = %v, want unique violation", err)
	}

	group := createTestGroup(t, ctx, store, suffix, "swap-default")
	isDefault := true
	updated, err := store.Update(ctx, group.ID, UpdateGroupInput{IsDefault: &isDefault})
	if err != nil {
		t.Fatalf("Update(is_default true) error: %v", err)
	}
	if !updated.IsDefault {
		t.Fatalf("updated IsDefault = false, want true")
	}
	assertSingleDefaultGroup(t, ctx, pool, group.ID)

	isDefault = false
	if _, err := store.Update(ctx, group.ID, UpdateGroupInput{IsDefault: &isDefault}); !errors.Is(err, ErrDefaultGroupRequired) {
		t.Fatalf("Update(is_default false) on the default group error = %v, want ErrDefaultGroupRequired", err)
	}
	assertSingleDefaultGroup(t, ctx, pool, group.ID)
}

func TestGroupStoreDeleteDefaultRejectedDB(t *testing.T) {
	ctx, pool, store, suffix := newGroupStoreDBTest(t)
	seedID := defaultAccessGroupSeedID(t, ctx, pool)
	t.Cleanup(func() {
		restoreDefaultAccessGroup(t, ctx, pool, seedID)
	})

	group := createTestGroup(t, ctx, store, suffix, "delete-default")
	isDefault := true
	if _, err := store.Update(ctx, group.ID, UpdateGroupInput{IsDefault: &isDefault}); err != nil {
		t.Fatalf("Update(is_default true) error: %v", err)
	}
	userID := insertAccessGroupTestUser(t, ctx, pool, suffix, &group.ID, 1)

	if err := store.Delete(ctx, group.ID); !errors.Is(err, ErrDefaultGroupRequired) {
		t.Fatalf("Delete(default) error = %v, want ErrDefaultGroupRequired", err)
	}
	var hasGroup bool
	if err := pool.QueryRow(ctx, `
		SELECT access_group_id IS NOT NULL
		FROM users
		WHERE id = $1`, userID).Scan(&hasGroup); err != nil {
		t.Fatalf("load default group member: %v", err)
	}
	if !hasGroup {
		t.Fatalf("user access_group_id cleared by rejected default-group delete")
	}
	assertSingleDefaultGroup(t, ctx, pool, group.ID)

	// Deleting a non-default group still clears memberships through the FK.
	other := createTestGroup(t, ctx, store, suffix, "delete-non-default")
	otherUserID := insertAccessGroupTestUser(t, ctx, pool, suffix, &other.ID, 2)
	if err := store.Delete(ctx, other.ID); err != nil {
		t.Fatalf("Delete(non-default) error: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT access_group_id IS NOT NULL
		FROM users
		WHERE id = $1`, otherUserID).Scan(&hasGroup); err != nil {
		t.Fatalf("load deleted group member: %v", err)
	}
	if hasGroup {
		t.Fatalf("user access_group_id remained set after deleting non-default group")
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
	if !accessGroupDefaultColumnExists(t, ctx, pool) {
		t.Skip("test database has not applied default access group migration")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1`, "access-group-test-"+suffix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM access_groups WHERE name LIKE $1`, "Access Group Test "+suffix+"%")
	})
	return ctx, pool, NewGroupStore(pool), suffix
}

func accessGroupDefaultColumnExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'access_groups'
			  AND column_name = 'is_default'
		)`).Scan(&exists); err != nil {
		t.Fatalf("check access_groups.is_default column: %v", err)
	}
	return exists
}

func defaultAccessGroupSeedID(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `
		SELECT id
		FROM access_groups
		WHERE name = 'Default Group'
		  AND is_default`).Scan(&id); err != nil {
		t.Fatalf("load seeded default access group: %v", err)
	}
	return id
}

func assertDefaultGroupSeed(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var (
		description              string
		libraryIDsNull           bool
		maxPlaybackQuality       string
		downloadAllowed          bool
		downloadTranscodeAllowed bool
		maxStreams               int
		maxTranscodes            int
		allowedPermissions       []string
		requestsAllowed          bool
	)
	if err := pool.QueryRow(ctx, `
		SELECT description, library_ids IS NULL, max_playback_quality,
			download_allowed, download_transcode_allowed, max_streams,
			max_transcodes, allowed_permissions, requests_allowed
		FROM access_groups
		WHERE name = 'Default Group'
		  AND is_default`).Scan(
		&description,
		&libraryIDsNull,
		&maxPlaybackQuality,
		&downloadAllowed,
		&downloadTranscodeAllowed,
		&maxStreams,
		&maxTranscodes,
		&allowedPermissions,
		&requestsAllowed,
	); err != nil {
		t.Fatalf("load seeded default access group details: %v", err)
	}
	if description != "Applied automatically to newly created users." ||
		!libraryIDsNull ||
		maxPlaybackQuality != "" ||
		!downloadAllowed ||
		downloadTranscodeAllowed ||
		maxStreams != 5 ||
		maxTranscodes != 5 ||
		!slices.Equal(allowedPermissions, []string{"marker_edit"}) ||
		!requestsAllowed {
		t.Fatalf("seeded default group does not match the migration's starter policy")
	}
}

func assertSingleDefaultGroup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wantID int64) {
	t.Helper()
	var (
		gotID int64
		count int
	)
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(MIN(id), 0), COUNT(*)::int
		FROM access_groups
		WHERE is_default`).Scan(&gotID, &count); err != nil {
		t.Fatalf("count default access groups: %v", err)
	}
	if count != 1 || gotID != wantID {
		t.Fatalf("default groups = count %d id %d, want count 1 id %d", count, gotID, wantID)
	}
}

func restoreDefaultAccessGroup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seedID int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE access_groups
		SET is_default = false
		WHERE is_default
		  AND id <> $1`, seedID); err != nil {
		t.Fatalf("clear non-seed default groups: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE access_groups
		SET is_default = true
		WHERE id = $1`, seedID); err != nil {
		t.Fatalf("restore seeded default group: %v", err)
	}
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
