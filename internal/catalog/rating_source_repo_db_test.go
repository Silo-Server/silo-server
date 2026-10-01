package catalog

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestRatingSourceRepositoryUpsertPostgres(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	repo := NewRatingSourceRepository(pool)

	suffix := time.Now().UnixNano()
	movie := fmt.Sprintf("rating-sources-movie-%d", suffix)
	other := fmt.Sprintf("rating-sources-other-%d", suffix)
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movie, other})
	})
	for _, id := range []string{movie, other} {
		batchEquivExec(t, pool, `
			INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'movie', 'Rating Sources', '{}'::text[])
		`, id)
	}

	votes := func(n int64) *int64 { return &n }
	row := func(source string, score float64, v *int64, provider string) models.ItemRatingSource {
		return models.ItemRatingSource{ContentID: movie, Source: source, Score: score, Votes: v, Provider: provider}
	}

	// A scheduled refresh stores what it found.
	if err := repo.Upsert(ctx, movie, []models.ItemRatingSource{
		row(models.RatingSourceMDBList, 86, nil, "mdblist"),
		row(models.RatingSourceIMDB, 81, votes(1000), "mdblist"),
	}, false); err != nil {
		t.Fatalf("Upsert(fill) error = %v", err)
	}

	// A later fill-empty write keeps stored sources and adds new ones.
	if err := repo.Upsert(ctx, movie, []models.ItemRatingSource{
		row(models.RatingSourceIMDB, 50, votes(5), "other"),
		row(models.RatingSourceLetterboxd, 80, votes(20), "other"),
	}, false); err != nil {
		t.Fatalf("Upsert(fill again) error = %v", err)
	}
	got, err := repo.GetByContentID(ctx, movie)
	if err != nil {
		t.Fatalf("GetByContentID() error = %v", err)
	}
	want := []models.ItemRatingSource{
		row(models.RatingSourceIMDB, 81, votes(1000), "mdblist"),
		row(models.RatingSourceLetterboxd, 80, votes(20), "other"),
		row(models.RatingSourceMDBList, 86, nil, "mdblist"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after fill-empty = %+v, want %+v", got, want)
	}

	// A replace-unlocked write overwrites the sources it reports, including
	// clearing a vote count, and leaves the others alone.
	if err := repo.Upsert(ctx, movie, []models.ItemRatingSource{
		row(models.RatingSourceIMDB, 82, nil, "mdblist"),
		row(models.RatingSourceIMDB, 99, nil, "duplicate"), // ignored: first entry wins
	}, true); err != nil {
		t.Fatalf("Upsert(replace) error = %v", err)
	}
	got, err = repo.GetByContentID(ctx, movie)
	if err != nil {
		t.Fatalf("GetByContentID() error = %v", err)
	}
	want[0] = row(models.RatingSourceIMDB, 82, nil, "mdblist")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after replace = %+v, want %+v", got, want)
	}

	batch, err := repo.ListByContentIDs(ctx, []string{movie, other})
	if err != nil {
		t.Fatalf("ListByContentIDs() error = %v", err)
	}
	if !reflect.DeepEqual(batch[movie], want) {
		t.Fatalf("ListByContentIDs()[movie] = %+v, want %+v", batch[movie], want)
	}
	if _, ok := batch[other]; ok {
		t.Fatalf("ListByContentIDs() has an entry for an item without sources: %+v", batch[other])
	}

	// The rows follow the item when silo_rename_content_id moves it, and go
	// with it when it is deleted.
	renamed := movie + "-renamed"
	batchEquivExec(t, pool, `UPDATE media_items SET content_id = $2 WHERE content_id = $1`, movie, renamed)
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = $1`, renamed)
	})
	if moved, err := repo.GetByContentID(ctx, renamed); err != nil || len(moved) != 3 {
		t.Fatalf("after rename = %d rows (err %v), want 3", len(moved), err)
	}
	batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = $1`, renamed)
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_item_rating_sources WHERE content_id = $1`, renamed).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("rows left after deleting the item = %d, want 0", remaining)
	}
}

func TestRatingSourceRepositoryReplacePostgres(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	repo := NewRatingSourceRepository(pool)

	suffix := time.Now().UnixNano()
	movie := fmt.Sprintf("rating-sources-replace-%d", suffix)
	other := fmt.Sprintf("rating-sources-replace-other-%d", suffix)
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movie, other})
	})
	for _, id := range []string{movie, other} {
		batchEquivExec(t, pool, `
			INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'movie', 'Rating Sources', '{}'::text[])
		`, id)
	}

	votes := func(n int64) *int64 { return &n }
	row := func(contentID, source string, score float64, v *int64) models.ItemRatingSource {
		return models.ItemRatingSource{ContentID: contentID, Source: source, Score: score, Votes: v, Provider: "mdblist"}
	}
	for _, id := range []string{movie, other} {
		if err := repo.Upsert(ctx, id, []models.ItemRatingSource{
			row(id, models.RatingSourceIMDB, 81, votes(1000)),
			row(id, models.RatingSourceMyAnimeList, 88, votes(50)),
		}, false); err != nil {
			t.Fatalf("Upsert(%s) error = %v", id, err)
		}
	}

	// The new set overwrites the sources it reports and removes the rest.
	if err := repo.Replace(ctx, movie, []models.ItemRatingSource{
		row(movie, models.RatingSourceIMDB, 64, votes(20)),
		row(movie, models.RatingSourceLetterboxd, 70, nil),
	}); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	got, err := repo.GetByContentID(ctx, movie)
	if err != nil {
		t.Fatalf("GetByContentID() error = %v", err)
	}
	want := []models.ItemRatingSource{
		row(movie, models.RatingSourceIMDB, 64, votes(20)),
		row(movie, models.RatingSourceLetterboxd, 70, nil),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after Replace = %+v, want %+v", got, want)
	}

	// An empty set clears the item's sources and no other item's.
	if err := repo.Replace(ctx, movie, nil); err != nil {
		t.Fatalf("Replace(empty) error = %v", err)
	}
	if got, err := repo.GetByContentID(ctx, movie); err != nil || len(got) != 0 {
		t.Fatalf("after Replace(empty) = %+v (err %v), want none", got, err)
	}
	if got, err := repo.GetByContentID(ctx, other); err != nil || len(got) != 2 {
		t.Fatalf("other item after Replace = %+v (err %v), want its 2 sources", got, err)
	}
}
