package handlers

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestEbookAnnotationsPostgres(t *testing.T) {
	pool := ebookProgressTestPool(t)
	_, err := pool.Exec(t.Context(), `CREATE TABLE ebook_reader_annotations(
 id text PRIMARY KEY,user_id integer NOT NULL,profile_id text NOT NULL,content_id text NOT NULL,
 kind text NOT NULL,cfi_range text,location text,selected_text text NOT NULL,note text NOT NULL,
 style text NOT NULL,color text NOT NULL,metadata jsonb NOT NULL,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	store := NewPGEbookReaderAnnotationStore(pool)
	at := time.Now().UTC().Truncate(time.Microsecond)
	original := EbookReaderAnnotation{ID: "b", UserID: 1, ProfileID: "one", ContentID: "book", Kind: "note", Location: "chapter1", Note: "original", Metadata: json.RawMessage(`{}`), CreatedAt: at, UpdatedAt: at}
	// Concurrent identical intents create only one row and return its stored winner.
	start := make(chan struct{})
	type result struct {
		row     *EbookReaderAnnotation
		created bool
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			row, created, err := store.CreateOrGet(t.Context(), original)
			results <- result{row, created, err}
		}()
	}
	close(start)
	createdCount := 0
	for range 2 {
		result := <-results
		if result.err != nil || result.row.Note != "original" {
			t.Fatalf("%+v", result)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created %d", createdCount)
	}
	// The legacy update path and v2 guards operate on the same locked row.
	updated, err := store.Update(t.Context(), 1, "one", "book", "b", func(row EbookReaderAnnotation) (EbookReaderAnnotation, error) {
		row.Note = "legacy edit"
		row.UpdatedAt = at.Add(time.Second)
		return row, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, created, err := store.CreateOrGet(t.Context(), original)
	if err != nil || created || replay.Note != updated.Note {
		t.Fatalf("replay: %+v %v %v", replay, created, err)
	}
	foreign := original
	foreign.ProfileID = "two"
	if row, _, err := store.CreateOrGet(t.Context(), foreign); err == nil || row != nil {
		t.Fatalf("foreign collision: %+v %v", row, err)
	}
	denied := errors.New("stale validator")
	stale := func(row EbookReaderAnnotation) error {
		if !row.UpdatedAt.Equal(original.UpdatedAt) {
			return denied
		}
		return nil
	}
	if err := store.DeleteGuarded(t.Context(), 1, "one", "book", "b", stale); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	handler := &EbookReaderHandler{AnnotationStore: store}
	if _, err := handler.patchReaderAnnotation(t.Context(), 1, "one", "book", "b", EbookAnnotationPatch{Location: new("")}, func(EbookReaderAnnotation) error { return nil }); err == nil {
		t.Fatal("invalid merged location accepted")
	}

	if _, err := handler.patchReaderAnnotation(t.Context(), 1, "one", "book", "b", EbookAnnotationPatch{Note: new("stale edit")}, stale); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if err := store.DeleteGuarded(t.Context(), 1, "two", "book", "b", func(EbookReaderAnnotation) error { t.Fatal("foreign row reached guard"); return nil }); !errors.Is(err, ErrEbookAnnotationNotFound) {
		t.Fatal(err)
	}
	// Tie-breaking includes IDs and paging does not include other profiles.
	for _, id := range []string{"a", "c", "d"} {
		row := original
		row.ID = id
		row.UpdatedAt = updated.UpdatedAt
		if id == "d" {
			row.ProfileID = "two"
		}
		if err := store.Create(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ListPage(t.Context(), 1, "one", "book", nil, 2)
	if err != nil || len(first) != 2 || first[0].ID != "c" || first[1].ID != "b" {
		t.Fatalf("first: %+v %v", first, err)
	}
	last := first[1]
	second, err := store.ListPage(t.Context(), 1, "one", "book", &EbookAnnotationPosition{UpdatedAt: last.UpdatedAt, ID: last.ID}, 2)
	if err != nil || len(second) != 1 || second[0].ID != "a" {
		t.Fatalf("second: %+v %v", second, err)
	}
	for _, limit := range []int{0, 52} {
		if _, err := store.ListPage(t.Context(), 1, "one", "book", nil, limit); err == nil {
			t.Fatal("unbounded page accepted")
		}
	}
	if err := store.DeleteGuarded(t.Context(), 1, "one", "book", "b", func(row EbookReaderAnnotation) error {
		if row.Note != "legacy edit" {
			t.Fatalf("changed by stale mutation: %+v", row)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteGuarded(t.Context(), 1, "one", "book", "b", func(EbookReaderAnnotation) error { return nil }); !errors.Is(err, ErrEbookAnnotationNotFound) {
		t.Fatal(err)
	}
}
