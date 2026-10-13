package migrations

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

// requireLibraryIDs checks one library_ids column, keeping NULL (every
// library) distinct from an empty array (no libraries).
func requireLibraryIDs(t *testing.T, tx pgx.Tx, table string, id int64, want []int32) {
	t.Helper()
	var got []int32
	if err := tx.QueryRow(t.Context(), `SELECT library_ids FROM `+table+` WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if (got == nil) != (want == nil) || !slices.Equal(got, want) {
		t.Fatalf("%s %d library_ids = %#v, want %#v", table, id, got, want)
	}
}

// TestDeletingLibraryPrunesAccessPolicyReferencesPostgres deletes libraries
// that access groups, account overrides and invitations name, against the
// migrated schema in a transaction that is rolled back. The deleted IDs leave
// every list, the surviving ones keep their order, a list left with nothing
// stays an empty restriction rather than widening to every library, and NULL
// stays NULL.
func TestDeletingLibraryPrunesAccessPolicyReferencesPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	folder := func(name string) int32 {
		t.Helper()
		var id int32
		if err := tx.QueryRow(t.Context(), `INSERT INTO media_folders (type, name) VALUES ('movies', $1) RETURNING id`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	gone, alsoGone, kept := folder("access prune gone"), folder("access prune also gone"), folder("access prune kept")
	insert := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := tx.QueryRow(t.Context(), query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	group := func(name string, ids []int32) int64 {
		return insert(`INSERT INTO access_groups (name, library_ids) VALUES ($1, $2) RETURNING id`, name, ids)
	}
	user := func(name string, ids []int32) int64 {
		return insert(`INSERT INTO users (username, role, library_ids) VALUES ($1, 'user', $2) RETURNING id`, name, ids)
	}
	mixedGroup := group("access prune mixed", []int32{gone, kept, alsoGone})
	onlyGoneGroup := group("access prune only gone", []int32{gone})
	allGroup := group("access prune all", nil)
	untouchedGroup := group("access prune untouched", []int32{kept})
	mixedUser := user("access-prune-mixed", []int32{alsoGone, kept})
	onlyGoneUser := user("access-prune-only-gone", []int32{gone, alsoGone})
	inheritUser := user("access-prune-inherit", nil)
	invitation := insert(`INSERT INTO invitations (email, token_hash, library_ids, invited_by, expires_at)
		VALUES ('access-prune@example.test', 'access-prune-token', $1, $2, now() + interval '1 day') RETURNING id`,
		[]int32{kept, gone}, mixedUser)

	revision := func(id int64) int64 {
		t.Helper()
		var rev int64
		if err := tx.QueryRow(t.Context(), `SELECT configuration_revision FROM access_groups WHERE id = $1`, id).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		return rev
	}
	mixedRevision, untouchedRevision := revision(mixedGroup), revision(untouchedGroup)

	if _, err := tx.Exec(t.Context(), `DELETE FROM media_folders WHERE id = ANY($1)`, []int32{gone, alsoGone}); err != nil {
		t.Fatal(err)
	}

	requireLibraryIDs(t, tx, "access_groups", mixedGroup, []int32{kept})
	requireLibraryIDs(t, tx, "access_groups", onlyGoneGroup, []int32{})
	requireLibraryIDs(t, tx, "access_groups", allGroup, nil)
	requireLibraryIDs(t, tx, "access_groups", untouchedGroup, []int32{kept})
	requireLibraryIDs(t, tx, "users", mixedUser, []int32{kept})
	requireLibraryIDs(t, tx, "users", onlyGoneUser, []int32{})
	requireLibraryIDs(t, tx, "users", inheritUser, nil)
	requireLibraryIDs(t, tx, "invitations", invitation, []int32{kept})

	// An edited group gets a new revision, so an admin form opened before the
	// delete reports a conflict instead of saving the deleted library back.
	if revision(mixedGroup) == mixedRevision {
		t.Fatal("pruned group kept its configuration revision")
	}
	if revision(untouchedGroup) != untouchedRevision {
		t.Fatal("group without a deleted library got a new configuration revision")
	}

	// A write naming a library that does not exist, from any API, keeps only
	// the libraries that do.
	missing := gone
	written := group("access prune written", []int32{kept, missing})
	requireLibraryIDs(t, tx, "access_groups", written, []int32{kept})
	if _, err := tx.Exec(t.Context(), `UPDATE users SET library_ids = $1 WHERE id = $2`, []int32{missing}, inheritUser); err != nil {
		t.Fatal(err)
	}
	requireLibraryIDs(t, tx, "users", inheritUser, []int32{})
}

// TestLibraryDeleteRacingAccessWritePostgres commits a library delete while
// another transaction is adding that library to an access group. The write
// waits for the delete, as it would behind a foreign key, and stores only the
// libraries that still exist.
func TestLibraryDeleteRacingAccessWritePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	connect := func() *pgx.Conn {
		t.Helper()
		conn, err := pgx.Connect(t.Context(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}
	deleter, writer := connect(), connect()
	ctx := t.Context()

	var kept, doomed int32
	var groupID int64
	if err := deleter.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('movies', 'access race kept') RETURNING id`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if err := deleter.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('movies', 'access race doomed') RETURNING id`).Scan(&doomed); err != nil {
		t.Fatal(err)
	}
	if err := deleter.QueryRow(ctx, `INSERT INTO access_groups (name, library_ids) VALUES ('access race group', $1) RETURNING id`, []int32{kept}).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = deleter.Exec(context.Background(), `DELETE FROM access_groups WHERE id = $1`, groupID)
		_, _ = deleter.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, []int32{kept, doomed})
	})

	tx, err := deleter.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, doomed); err != nil {
		t.Fatal(err)
	}

	var writerPID uint32
	if err := writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	wrote := make(chan error, 1)
	go func() {
		_, err := writer.Exec(ctx, `UPDATE access_groups SET library_ids = $1 WHERE id = $2`, []int32{kept, doomed}, groupID)
		wrote <- err
	}()
	// Wait until the write is blocked on the delete's lock.
	for {
		var waiting bool
		if err := deleter.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted)`, writerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-wrote:
			t.Fatalf("write finished before the delete committed: %v", err)
		default:
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	var got []int32
	if err := deleter.QueryRow(ctx, `SELECT library_ids FROM access_groups WHERE id = $1`, groupID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int32{kept}) {
		t.Fatalf("library_ids = %v, want [%d]", got, kept)
	}
}

const pruneDeletedLibraryAccessMigration = "20261013021242_prune_deleted_library_access_references"

// TestPruneDeletedLibraryAccessMigrationRepairsStaleIDsPostgres runs the
// migration over references that earlier library deletes left behind: it
// drops every ID that names no library and leaves NULL lists and intact lists
// alone. The rollback removes the trigger, and running it again repairs what
// a delete left behind in between.
func TestPruneDeletedLibraryAccessMigrationRepairsStaleIDsPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE media_folders (id bigint PRIMARY KEY);
CREATE TABLE access_groups (id bigint PRIMARY KEY, library_ids integer[], updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE users (id bigint PRIMARY KEY, library_ids integer[], updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE invitations (id bigint PRIMARY KEY, library_ids integer[], updated_at timestamptz NOT NULL DEFAULT now());
INSERT INTO media_folders VALUES (1), (2), (3);
INSERT INTO access_groups (id, library_ids) VALUES (1, '{3,20,1,23}'), (2, '{20,23}'), (3, NULL), (4, '{2,1}');
INSERT INTO users (id, library_ids) VALUES (1, '{23,2}'), (2, '{20}'), (3, NULL), (4, '{}');
INSERT INTO invitations (id, library_ids) VALUES (1, '{1,20}'), (2, NULL);`)

	check := func() {
		t.Helper()
		requireLibraryIDs(t, tx, "access_groups", 1, []int32{3, 1})
		requireLibraryIDs(t, tx, "access_groups", 2, []int32{})
		requireLibraryIDs(t, tx, "access_groups", 3, nil)
		requireLibraryIDs(t, tx, "access_groups", 4, []int32{2, 1})
		requireLibraryIDs(t, tx, "users", 1, []int32{2})
		requireLibraryIDs(t, tx, "users", 2, []int32{})
		requireLibraryIDs(t, tx, "users", 3, nil)
		requireLibraryIDs(t, tx, "users", 4, []int32{})
		requireLibraryIDs(t, tx, "invitations", 1, []int32{1})
		requireLibraryIDs(t, tx, "invitations", 2, nil)
	}
	migrationExec(t, tx, adminMigrationSQL(t, pruneDeletedLibraryAccessMigration, schema, false))
	check()

	// The triggers installed in the same schema filter a later write and prune
	// a later delete.
	migrationExec(t, tx, `UPDATE invitations SET library_ids = '{3,99}' WHERE id = 2`)
	requireLibraryIDs(t, tx, "invitations", 2, []int32{3})
	migrationExec(t, tx, `UPDATE invitations SET library_ids = NULL WHERE id = 2`)
	migrationExec(t, tx, `DELETE FROM media_folders WHERE id = 2`)
	requireLibraryIDs(t, tx, "access_groups", 4, []int32{1})
	requireLibraryIDs(t, tx, "users", 1, []int32{})

	// After the rollback a delete leaves its reference behind again, and
	// re-running the migration repairs it.
	migrationExec(t, tx, adminMigrationSQL(t, pruneDeletedLibraryAccessMigration, schema, true))
	migrationExec(t, tx, `DELETE FROM media_folders WHERE id = 1`)
	requireLibraryIDs(t, tx, "access_groups", 4, []int32{1})
	migrationExec(t, tx, adminMigrationSQL(t, pruneDeletedLibraryAccessMigration, schema, false))
	requireLibraryIDs(t, tx, "access_groups", 1, []int32{3})
	requireLibraryIDs(t, tx, "access_groups", 4, []int32{})
	requireLibraryIDs(t, tx, "invitations", 1, []int32{})
	requireLibraryIDs(t, tx, "access_groups", 3, nil)
}
