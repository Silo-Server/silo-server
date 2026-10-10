package bridgeimport

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userdb/bridgeimport/testdata"
)

// The collection writer applies #1615: a shared collection whose allow list
// names every source profile (its creator implied) stays shared; one shared
// with fewer profiles, or with nobody but its creator, is imported private.
// Imported rows are native, and the semantic digest verifies the converted
// values against the target. It runs in a rolled-back transaction on any
// migrated database.
func TestImportCollectionsAppliesSharingRuleDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	path := filepath.Join(t.TempDir(), "1.db")
	source, err := testdata.NewSource(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	const stamp = "2026-01-01T00:00:00Z"
	for _, statement := range []string{
		`INSERT INTO profiles(id,name,is_primary,created_at,updated_at) VALUES('parent','parent',1,'` + stamp + `','` + stamp + `'),('child','child',0,'` + stamp + `','` + stamp + `'),('guest','guest',0,'` + stamp + `','` + stamp + `')`,
		`INSERT INTO personal_collections VALUES
			('whole','parent','parent','Whole login','manual',1,'{}','{}','` + stamp + `','` + stamp + `'),
			('subset','parent','parent','Subset','manual',1,'{}','{}','` + stamp + `','` + stamp + `'),
			('creator','parent','parent','Creator only','manual',1,'{}','{}','` + stamp + `','` + stamp + `'),
			('private','child','child','Private','manual',0,'{}','{}','` + stamp + `','` + stamp + `')`,
		`INSERT INTO personal_collection_profiles VALUES('whole','child'),('whole','guest'),('subset','parent'),('subset','child'),('creator','parent'),('private','child')`,
	} {
		if _, err := source.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := source.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Rollback() })

	target, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Rollback(context.Background()) })
	var userID int
	if err := target.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("bridge-sharing-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	verification, err := importTable(ctx, snapshot, target, userID, Mapping{Source: sourceCollections, Target: "user_" + sourceCollections})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if verification.Rows != 4 {
		t.Fatalf("imported %d rows, want 4", verification.Rows)
	}
	rows, err := target.Query(ctx, `SELECT id, is_shared, native FROM user_personal_collections WHERE user_id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var id string
		var shared, native bool
		if err := rows.Scan(&id, &shared, &native); err != nil {
			t.Fatal(err)
		}
		if !native {
			t.Fatalf("%s imported without the native mark", id)
		}
		got[id] = shared
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := map[string]bool{"whole": true, "subset": false, "creator": false, "private": false}; !maps.Equal(got, want) {
		t.Fatalf("imported is_shared = %v, want %v", got, want)
	}
}
