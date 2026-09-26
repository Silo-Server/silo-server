package handlers

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAdminUnmatchedSQLPageAndBridge(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TEMP TABLE media_files (LIKE public.media_files INCLUDING DEFAULTS)`)
	exec(`INSERT INTO media_files(id,media_folder_id,file_path,file_size,container,content_id,extra_id)VALUES(10,7,'first',100,'mkv',NULL,NULL),(20,7,'matched',100,'mkv','matched',NULL),(30,7,'extra',100,'mkv',NULL,1),(40,8,'last',200,'mp4',NULL,NULL)`)
	h := &AdminHandler{pool: pool}
	rows, more, err := h.ListAdminUnmatchedFiles(t.Context(), 1, 0)
	if err != nil || !more || len(rows) != 1 || rows[0].ID != 10 {
		t.Fatalf("first %v %v %v", rows, more, err)
	}
	rows, more, err = h.ListAdminUnmatchedFiles(t.Context(), 1, 10)
	if err != nil || more || len(rows) != 1 || rows[0].ID != 40 {
		t.Fatalf("next %v %v %v", rows, more, err)
	}
	rows, more, err = h.ListAdminUnmatchedFiles(t.Context(), 200, 0)
	if err != nil || more || len(rows) != 2 {
		t.Fatalf("maximum page %v %v %v", rows, more, err)
	}
	rec := httptest.NewRecorder()
	h.HandleListUnmatched(rec, httptest.NewRequest("GET", "/admin/unmatched?limit=1&offset=1", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "[") || !strings.Contains(rec.Body.String(), `"id":40`) {
		t.Fatalf("bridge %d %s", rec.Code, rec.Body)
	}
	rows, more, err = h.ListAdminUnmatchedFiles(t.Context(), 1, 40)
	if err != nil || more || rows == nil || len(rows) != 0 {
		t.Fatalf("empty %v %v %v", rows, more, err)
	}
}
