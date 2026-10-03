package catalog

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSimilarContentIDsPostgres ranks synthetic titles by what they share
// with a source film. Set SILO_TEST_DATABASE_URL to a migrated disposable
// database; every inserted row is removed on completion.
func TestSimilarContentIDsPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := "similar-" + uuid.NewString() + "-"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed fixture: %v", err)
		}
	}
	base := time.Now().UnixNano()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM people WHERE id BETWEEN $1 AND $1 + 20`, base)
	})

	title := func(name, kind, released string, genres ...string) string {
		id := prefix + name
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,keywords,studios,release_date,content_rating,content_rating_age)
			VALUES($1,$2,$1,'released',$3,'{}','{}',$4::date,'PG',8)`, id, kind, append([]string{}, genres...), released)
		return id
	}
	credits := 0
	credit := func(content string, person int64, kind, order int) {
		credits++
		exec(`INSERT INTO people(id,name) VALUES($1,$2) ON CONFLICT (id) DO NOTHING`, base+person, prefix+string(rune('a'+person)))
		exec(`INSERT INTO item_people(id,content_id,person_id,kind,sort_order) VALUES($1,$2,$3,$4,$5)`,
			base+100+int64(credits), content, base+person, kind, order)
	}

	source := title("source", "movie", "2020-01-01", "Action", "Comedy", "Crime", "Drama")
	credit(source, 0, 2, 0) // first director
	credit(source, 1, 2, 0) // second director
	credit(source, 2, 3, 0) // first writer
	for actor := range 6 {
		credit(source, int64(3+actor), 1, actor)
	}

	newerThreeGenres := title("newer", "movie", "2024-01-01", "Crime", "Action", "Comedy")
	olderThreeGenres := title("older", "movie", "1999-01-01", "Action", "Comedy", "Crime")
	director := title("director", "movie", "2010-01-01")
	credit(director, 0, 2, 0)
	credit(director, 3, 1, 4) // the source's top-billed actor, billed fifth here
	writer := title("writer", "movie", "2010-01-01")
	credit(writer, 2, 3, 0)
	secondDirector := title("second-director", "movie", "2010-01-01")
	credit(secondDirector, 1, 2, 0)
	sixthActor := title("sixth-actor", "movie", "2010-01-01")
	credit(sixthActor, 8, 1, 0)
	title("fourth-genre", "movie", "2010-01-01", "Drama")
	title("series", "series", "2010-01-01", "Action", "Comedy", "Crime")

	got, err := NewItemRepository(pool).SimilarContentIDs(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	// Three shared genres, newest first; then the first director and a top
	// five actor; then the first writer. The second director, the sixth
	// actor, the fourth genre and another type count for nothing.
	want := []string{newerThreeGenres, olderThreeGenres, director, writer}
	if !slices.Equal(got, want) {
		t.Fatalf("SimilarContentIDs = %v, want %v", got, want)
	}

	if got, err := NewItemRepository(pool).SimilarContentIDs(ctx, prefix+"missing"); err != nil || len(got) != 0 {
		t.Fatalf("SimilarContentIDs(missing) = %v, %v; want none", got, err)
	}
}
