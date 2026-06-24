package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNormalizeDisplayQueryFragment_EmptyInputs(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"null",
		`{"match":"all","groups":[]}`,
		`{"match":"all","groups":[{"match":"all","rules":[]}]}`,
		`{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":"all"}]}]}`,
	} {
		got, err := NormalizeDisplayQueryFragment([]byte(raw))
		if err != nil {
			t.Fatalf("NormalizeDisplayQueryFragment(%q) returned error: %v", raw, err)
		}
		if got != "" {
			t.Fatalf("NormalizeDisplayQueryFragment(%q) = %q, want empty", raw, got)
		}
	}
}

func TestNormalizeDisplayQueryFragment_CanonicalizesTypeAndWatched(t *testing.T) {
	raw := `{"match":"all","groups":[{"match":"all","rules":[
		{"field":"watched","op":"is","value":true},
		{"field":"type","op":"is","value":"movie"}
	]}]}`
	got, err := NormalizeDisplayQueryFragment([]byte(raw))
	if err != nil {
		t.Fatalf("NormalizeDisplayQueryFragment returned error: %v", err)
	}

	// Canonical output is filter-only: no sort/limit/library_ids/media_scope.
	for _, banned := range []string{"sort", "limit", "library_ids", "media_scope"} {
		if strings.Contains(got, banned) {
			t.Fatalf("canonical fragment must not contain %q, got %q", banned, got)
		}
	}

	var def QueryDefinition
	if err := json.Unmarshal([]byte(got), &def); err != nil {
		t.Fatalf("canonical fragment does not round-trip: %v", err)
	}
	if def.Match != "all" || len(def.Groups) != 1 || len(def.Groups[0].Rules) != 2 {
		t.Fatalf("unexpected canonical shape: %+v", def)
	}
}

func TestNormalizeDisplayQueryFragment_RejectsExecutionControllingFields(t *testing.T) {
	cases := map[string]string{
		"limit":       `{"match":"all","limit":5,"groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":true}]}]}`,
		"sort":        `{"match":"all","sort":{"field":"title","order":"asc"},"groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":true}]}]}`,
		"library_ids": `{"match":"all","library_ids":[3],"groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":true}]}]}`,
		"media_scope": `{"match":"all","media_scope":"movie","groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":true}]}]}`,
	}
	for name, raw := range cases {
		if _, err := NormalizeDisplayQueryFragment([]byte(raw)); err == nil {
			t.Fatalf("%s: expected rejection of execution-controlling field, got nil error", name)
		}
	}
}

func TestNormalizeDisplayQueryFragment_RejectsUnsupportedFieldsAndValues(t *testing.T) {
	cases := map[string]string{
		"unsupported field": `{"match":"all","groups":[{"match":"all","rules":[{"field":"genre","op":"is","value":"Drama"}]}]}`,
		"watched non-bool":  `{"match":"all","groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":"yes"}]}]}`,
		"type non-string":   `{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":3}]}]}`,
		"type empty string": `{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":"  "}]}]}`,
		"type unsupported":  `{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":"ebook"}]}]}`,
		"malformed json":    `{"match":"all",`,
	}
	for name, raw := range cases {
		if _, err := NormalizeDisplayQueryFragment([]byte(raw)); err == nil {
			t.Fatalf("%s: expected error, got nil", name)
		}
	}
}

func TestFilterCollectionItemsByDisplayQuery(t *testing.T) {
	ctx := context.Background()
	input := []*models.MediaItem{
		{ContentID: "movie-b"},
		{ContentID: "series-1"},
		{ContentID: "movie-a"},
	}
	empty, err := FilterCollectionItemsByDisplayQuery(ctx, nil, input, "  ", AccessFilter{})
	if err != nil {
		t.Fatalf("empty display query returned error: %v", err)
	}
	if got := contentIDsForTest(empty); strings.Join(got, ",") != "movie-b,series-1,movie-a" {
		t.Fatalf("empty display query order = %v", got)
	}

	pool := newDisplayQueryFilterTestPool(t)
	suffix := time.Now().UnixNano()
	movieA := fmt.Sprintf("display-filter-movie-a-%d", suffix)
	movieB := fmt.Sprintf("display-filter-movie-b-%d", suffix)
	series := fmt.Sprintf("display-filter-series-%d", suffix)
	outsideMovie := fmt.Sprintf("display-filter-outside-movie-%d", suffix)
	ids := []string{movieA, movieB, series, outsideMovie}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
	})
	seedDisplayFilterMediaItem(t, pool, movieA, "movie")
	seedDisplayFilterMediaItem(t, pool, movieB, "movie")
	seedDisplayFilterMediaItem(t, pool, series, "series")
	seedDisplayFilterMediaItem(t, pool, outsideMovie, "movie")

	items := []*models.MediaItem{
		{ContentID: movieB},
		{ContentID: series},
		{ContentID: movieA},
	}
	filtered, err := FilterCollectionItemsByDisplayQuery(
		ctx,
		pool,
		items,
		`{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":"movie"}]}]}`,
		AccessFilter{},
	)
	if err != nil {
		t.Fatalf("FilterCollectionItemsByDisplayQuery returned error: %v", err)
	}
	got := contentIDsForTest(filtered)
	want := []string{movieB, movieA}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("filtered content IDs = %v, want %v", got, want)
	}
}

func newDisplayQueryFilterTestPool(t *testing.T) *pgxpool.Pool {
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
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.media_items')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check media_items table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied base schema")
	}
	return pool
}

func seedDisplayFilterMediaItem(t *testing.T, pool *pgxpool.Pool, contentID string, mediaType string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO media_items (content_id, type, title, genres)
		VALUES ($1, $2, $3, '{}'::text[])
	`, contentID, mediaType, contentID); err != nil {
		t.Fatalf("seed media item %s: %v", contentID, err)
	}
}

func contentIDsForTest(items []*models.MediaItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			ids = append(ids, item.ContentID)
		}
	}
	return ids
}
