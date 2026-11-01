package themesongs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A subtree theme pass runs after every autoscan. It must observe the subtree
// and the directories above it up to the series root without reading the rest
// of the library, which a large library turns into seconds per scan.
func TestDirectoriesReadsOnlyTheScannedSubtreeDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('tvshows','theme directories',true) RETURNING id`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id=$1`, folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folderID)
	}()
	lib := fmt.Sprintf("/theme-directories-%d", time.Now().UnixNano())
	show, gone := lib+"/Show", lib+"/Gone"
	for _, f := range []struct {
		path, root string
		missing    bool
	}{
		{show + "/Season 1/e1.mkv", show, false},
		{show + "/Season 2/e2.mkv", show, false},
		{show + "/Season 3/Disc 1/e3.mkv", show, false},
		{lib + "/flat.mkv", lib + "/flat.mkv", false},
		{gone + "/Season 1/e1.mkv", gone, true},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_files(media_folder_id,file_path,file_size,canonical_root_path,missing_since)
			VALUES($1,$2,1024,$3,CASE WHEN $4 THEN now() END)`, folderID, f.path, f.root, f.missing); err != nil {
			t.Fatal(err)
		}
	}
	// Enough unrelated series that a full read of the folder is the expensive plan.
	if _, err := pool.Exec(ctx, `INSERT INTO media_files(media_folder_id,file_path,file_size,canonical_root_path)
		SELECT $1, $2||'/Other '||(g/50)||'/Season 1/e'||g||'.mkv', 1024, $2||'/Other '||(g/50)
		FROM generate_series(1, 10000) g`, folderID, lib); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE media_files`); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(pool)
	for _, tc := range []struct {
		name, scope string
		want        []string
	}{
		{"season", show + "/Season 1", []string{show, show + "/Season 1"}},
		{"nested disc", show + "/Season 3/Disc 1", []string{show, show + "/Season 3", show + "/Season 3/Disc 1"}},
		{"season without videos", show + "/Season 4", []string{show}},
		{"series", show, []string{show, show + "/Season 1", show + "/Season 2", show + "/Season 3", show + "/Season 3/Disc 1"}},
		{"missing videos only", gone + "/Season 1", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.Directories(ctx, folderID, tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Directories(%q) = %q, want %q", tc.scope, got, tc.want)
			}
		})
	}

	t.Run("generic plans stay on their indexes", func(t *testing.T) {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		subtree, _ := directoriesQuery(folderID, show+"/Season 1")
		for _, tc := range []struct{ query, index, cond string }{
			{subtree, "idx_media_files_folder_file_path_pattern", "file_path ~>=~"},
			{scopeRootsQuery, "idx_media_files_folder_root", "canonical_root_path"},
		} {
			assertIndexedGenericPlan(t, conn.Conn().PgConn(), tc.query, tc.index, tc.cond)
		}
	})
}

// assertIndexedGenericPlan fails unless the generic plan of query bounds cond on
// index and never reads media_files sequentially. pgx prepares statements, and
// PostgreSQL may switch them to a generic plan after five executions.
func assertIndexedGenericPlan(t *testing.T, conn *pgconn.PgConn, query, index, cond string) {
	t.Helper()
	// GENERIC_PLAN needs the simple query protocol to leave $n unbound.
	results, err := conn.Exec(t.Context(), `EXPLAIN (GENERIC_PLAN, FORMAT JSON) `+query).ReadAll()
	if err != nil || len(results) != 1 || len(results[0].Rows) != 1 {
		t.Fatalf("explain generic plan: %v", err)
	}
	raw := results[0].Rows[0][0]
	var explained []struct {
		Plan planNode `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &explained); err != nil || len(explained) != 1 {
		t.Fatalf("decode plan %s: %v", raw, err)
	}
	var indexed bool
	explained[0].Plan.walk(func(node planNode) {
		if node.RelationName == "media_files" && node.NodeType == "Seq Scan" {
			t.Errorf("generic plan reads media_files sequentially: %s", raw)
		}
		if node.IndexName == index && strings.Contains(node.IndexCond, cond) {
			indexed = true
		}
	})
	if !indexed {
		t.Errorf("generic plan does not bound %s on %s: %s", cond, index, raw)
	}
}

type planNode struct {
	NodeType     string     `json:"Node Type"`
	RelationName string     `json:"Relation Name"`
	IndexName    string     `json:"Index Name"`
	IndexCond    string     `json:"Index Cond"`
	Plans        []planNode `json:"Plans"`
}

func (n planNode) walk(visit func(planNode)) {
	visit(n)
	for _, child := range n.Plans {
		child.walk(visit)
	}
}
