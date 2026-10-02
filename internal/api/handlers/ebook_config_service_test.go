package handlers

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestEbookConfigGuardedPostgres(t *testing.T) {
	pool := ebookProgressTestPool(t)
	if _, err := pool.Exec(t.Context(), `CREATE TABLE ebook_reader_config(user_id integer NOT NULL,profile_id text NOT NULL,content_id text NOT NULL,config jsonb NOT NULL DEFAULT '{}',updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(user_id,profile_id,content_id))`); err != nil {
		t.Fatal(err)
	}
	store := NewPGEbookReaderConfigStore(pool)
	denied := errors.New("stale configuration")
	config := EbookReaderConfig{UserID: 1, ProfileID: "one", ContentID: "book", Config: json.RawMessage(`{"theme":"dark"}`)}
	if _, err := store.ReplaceGuarded(t.Context(), config, func(*EbookReaderConfig) error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	current, err := store.Get(t.Context(), 1, "one", "book")
	if err != nil || current != nil {
		t.Fatalf("failed initial guard persisted a default: %+v %v", current, err)
	}
	// Both first writers expect the virtual default. Exactly one can create it.
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := store.ReplaceGuarded(t.Context(), config, func(current *EbookReaderConfig) error {
				if current != nil {
					return denied
				}
				return nil
			})
			results <- err
		}()
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, denied) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("first writer race: successes=%d conflicts=%d", successes, conflicts)
	}
	current, err = store.Get(t.Context(), 1, "one", "book")
	if err != nil {
		t.Fatal(err)
	}
	oldTime := current.UpdatedAt
	// A legacy writer updates the same resource. A guard comparing its earlier
	// snapshot must observe and reject the changed value inside the transaction.
	legacy := config
	legacy.Config = json.RawMessage(`{"theme":"light"}`)
	legacy.UpdatedAt = oldTime.Add(time.Second)
	if err := store.Upsert(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	_, err = store.ReplaceGuarded(t.Context(), config, func(current *EbookReaderConfig) error {
		if !current.UpdatedAt.Equal(oldTime) {
			return denied
		}
		return nil
	})
	if !errors.Is(err, denied) {
		t.Fatalf("legacy write not protected: %v", err)
	}
	current, err = store.Get(t.Context(), 1, "one", "book")
	if err != nil || string(current.Config) != `{"theme": "light"}` {
		t.Fatalf("stale write replaced legacy configuration: %+v %v", current, err)
	}
	other := config
	other.ProfileID = "two"
	saved, err := store.ReplaceGuarded(t.Context(), other, func(current *EbookReaderConfig) error {
		if current != nil {
			t.Fatal("another profile's configuration exposed")
		}
		return nil
	})
	if err != nil || saved.ProfileID != "two" {
		t.Fatalf("profile isolation: %+v %v", saved, err)
	}
}
