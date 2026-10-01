package catalog

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestFilterUnreferencedImageDirsPostgres runs the reconcile filter against the
// migrated schema. A candidate counts as referenced when any surviving path
// starts with it, so the cases that matter most are the ones where the
// candidate is not the surviving path's own directory: an ancestor, and a
// sibling that differs only by a LIKE metacharacter. Getting the first wrong
// deletes artwork that is still in use.
func TestFilterUnreferencedImageDirsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `
 INSERT INTO media_items(content_id,type,title,poster_path,backdrop_path,logo_path) VALUES
 ('imgdir-kept','movie','Kept','imgdirtest/movies/1/poster/a.webp','imgdirtest/movies/1/backdrop/b.webp',''),
 ('imgdir-gone','movie','Gone','imgdirtest/movies/2/poster/a.webp','',''),
 ('imgdir-abc','movie','Abc','imgdirtest/abc/p.webp','',''),
 ('imgdir-exact','movie','Exact','imgdirtest/exact/','',''),
 ('imgdir-series','series','Series','','',''),
 ('imgdir-gone-series','series','Gone series','','','');
 INSERT INTO seasons(content_id,series_id,season_number,poster_path) VALUES
 ('imgdir-series-s1','imgdir-series',1,'imgdirtest/series/3/season1/p.webp');
 INSERT INTO episodes(content_id,series_id,season_number,episode_number,still_path) VALUES
 ('imgdir-series-e1','imgdir-series',1,1,'imgdirtest/series/3/e1/still.webp'),
 ('imgdir-gone-series-e1','imgdir-gone-series',1,1,'imgdirtest/series/4/e1/still.webp');`); err != nil {
		t.Fatal(err)
	}

	candidates := []string{
		"imgdirtest/",                 // ancestor of live artwork: kept
		"imgdirtest/movies/",          // ancestor of live artwork: kept
		"imgdirtest/movies/1/poster/", // live poster: kept
		"imgdirtest/series/3/season1/",
		"imgdirtest/series/3/e1/",
		"imgdirtest/exact/",           // a stored path that is itself the directory
		"imgdirtest/movies/1/pos",     // no trailing '/': never reported
		"imgdirtest/movies/2/poster/", // owner is being deleted
		"imgdirtest/series/4/e1/",     // owner's series is being deleted
		"imgdirtest/a_c/",             // '_' is literal, so imgdirtest/abc/ does not hold it
		"imgdirtest/100%/",            // '%' is literal
		"imgdirtest/missing/",         // nothing there at all
		"imgdirtest/missing/",         // duplicates are reported once
	}
	// DeleteWithStats passes an empty set, so nothing is excluded as deleting and
	// the deleted-owner dirs count as referenced. nil must behave the same:
	// bound as NULL it would report every candidate.
	alwaysUnreferenced := []string{
		"imgdirtest/100%/",
		"imgdirtest/a_c/",
		"imgdirtest/missing/",
	}
	tests := []struct {
		name     string
		deleting []string
		want     []string
	}{
		{
			name:     "owners being deleted",
			deleting: []string{"imgdir-gone", "imgdir-gone-series"},
			want:     append(slices.Clone(alwaysUnreferenced), "imgdirtest/movies/2/poster/", "imgdirtest/series/4/e1/"),
		},
		{name: "empty deleting set", deleting: []string{}, want: alwaysUnreferenced},
		{name: "nil deleting set", deleting: nil, want: alwaysUnreferenced},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := filterUnreferencedImageDirs(ctx, tx, candidates, tc.deleting)
			if err != nil {
				t.Fatal(err)
			}
			slices.Sort(got)
			want := slices.Sorted(slices.Values(tc.want))
			if !slices.Equal(got, want) {
				t.Fatalf("unreferenced dirs = %q, want %q", got, want)
			}
		})
	}
}
