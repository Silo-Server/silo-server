package usercollections

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

type sameItems struct{}

func (sameItems) Resolve(_ context.Context, id string) (string, bool, error) { return id, true, nil }

// TestCollectionImportRemapsLibrariesDB: a restored collection's smart rules,
// display filter, and sync source name this server's libraries, not the
// source's ids, which can mean unrelated libraries here. A collection whose
// rules would lose their whole library scope is left out rather than widened
// to every library, and a collection keeps its group.
func TestCollectionImportRemapsLibrariesDB(t *testing.T) {
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
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	sfx := hex.EncodeToString(b)
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions) VALUES ($1::text, $1::text || '@example.test', 'h', 'user', '{}') RETURNING id`, "ucbk-"+sfx).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	profile := "p-" + sfx
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, 'Main', true)`, profile, userID); err != nil {
		t.Fatal(err)
	}
	group := &collectionGroup{Label: "shelf", Title: "Shelf", Name: "Shelf", Slug: "shelf-" + sfx, DefaultSortMode: "manual"}
	doc := collectionsDocument{Collections: []personalCollection{
		{ID: "c1", Name: "Remapped", Type: "smart", WatchFilter: "all", MediaFilter: "all", Items: []string{}, SharedWith: []string{}, Group: group,
			QueryDefinition: json.RawMessage(`{"library_ids":[7,8],"match":"all","groups":[]}`), SortConfig: json.RawMessage(`{}`),
			DisplayQueryDefinition: json.RawMessage(`{"filter_library_id":7}`), SourceConfig: json.RawMessage(`{"mode":"tmdb","library_ids":[7]}`)},
		{ID: "c2", Name: "Elsewhere", Type: "smart", WatchFilter: "all", MediaFilter: "all", Items: []string{}, SharedWith: []string{},
			QueryDefinition: json.RawMessage(`{"library_ids":[8],"match":"all","groups":[]}`), SortConfig: json.RawMessage(`{}`), SourceConfig: json.RawMessage(`{}`)},
		{ID: "c3", Name: "Source elsewhere", Type: "tmdb", WatchFilter: "all", MediaFilter: "all", Items: []string{}, SharedWith: []string{},
			QueryDefinition: json.RawMessage(`{}`), SortConfig: json.RawMessage(`{}`), SourceConfig: json.RawMessage(`{"mode":"tmdb","library_ids":[8]}`)},
	}}
	data, _ := json.Marshal(doc)
	target := userdata.Target{Owner: userdata.Owner{UserID: userID, ProfileID: profile}, Items: sameItems{}, Now: time.Now(),
		Library: func(id int) (int, bool) {
			if id == 7 {
				return 70001, true
			}
			return 0, false
		}}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := BackupData{}.Import(ctx, tx, target, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.LeftOut != 2 {
		t.Fatalf("import = %+v, want one created and two left out", res)
	}
	var query, display, source, slug string
	if err := pool.QueryRow(ctx, `
		SELECT c.query_definition->>'library_ids', c.display_query_definition->>'filter_library_id', c.source_config->>'library_ids', g.slug
		FROM user_personal_collections c JOIN user_collection_groups g ON g.user_id = c.user_id AND g.id = c.group_id
		WHERE c.user_id = $1 AND c.name = 'Remapped'`, userID).Scan(&query, &display, &source, &slug); err != nil {
		t.Fatal(err)
	}
	if query != "[70001]" || display != "70001" || source != "[70001]" || slug != group.Slug {
		t.Fatalf("restored definitions query %s display %s source %s group %s", query, display, source, slug)
	}
	var others int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_personal_collections WHERE user_id = $1 AND name <> 'Remapped'`, userID).Scan(&others); err != nil || others != 0 {
		t.Fatalf("collections that lost their scope were created: %d, %v", others, err)
	}
}
