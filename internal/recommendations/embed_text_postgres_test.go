package recommendations

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

// The SQL copy of the embedding text must order people who share a
// sort_order the way Go's byte-wise sortItemPeople does. Otherwise the text
// scan flags the item as stale on every run and Go skips it every time.
func TestEmbeddingTextSQLMatchesGoForTiedPeoplePostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := context.Background()
	const (
		prefix = "w1a-collate-"
		model  = "collate-test-model"
	)
	cleanupRecoMediaItems(t, pool, prefix)
	itemID := prefix + "1"
	seedRecoMediaItemTitled(t, pool, itemID, "movie", "matched", "Collation Test")

	// Each pair sorts one way byte by byte and the other way under a
	// language collation such as en_US.UTF-8.
	credits := []struct {
		name, character string
		kind            models.PersonKind
	}{
		{"Walker", "Pilot", models.PersonKindActor},
		{"van Dien", "Rico", models.PersonKindActor},
		{"DeVito", "", models.PersonKindDirector},
		{"Dean", "", models.PersonKindDirector},
		{"Fred", "", models.PersonKindWriter},
		{"Émile", "", models.PersonKindWriter},
	}
	base := time.Now().UnixNano()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM people WHERE id >= $1 AND id < $2`, base, base+int64(len(credits)))
	})
	for i, credit := range credits {
		id := base + int64(i)
		if _, err := pool.Exec(ctx, `INSERT INTO people (id, name) VALUES ($1, $2)`, id, credit.name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO item_people (id, content_id, person_id, kind, character, sort_order)
			VALUES ($1, $2, $1, $3, $4, 0)`, id, itemID, credit.kind, credit.character); err != nil {
			t.Fatal(err)
		}
	}

	var sortsLikeBytes bool
	if err := pool.QueryRow(ctx, `SELECT 'DeVito' < 'Dean'`).Scan(&sortsLikeBytes); err != nil {
		t.Fatal(err)
	}
	if sortsLikeBytes {
		t.Log("the database collation sorts like bytes, so this run does not exercise the tie-break")
	}

	items, err := catalog.NewItemRepository(pool).GetByIDs(ctx, []string{itemID})
	if err != nil || len(items) != 1 {
		t.Fatalf("load item: %v", err)
	}
	people, err := catalog.NewPersonRepository(pool).ListForItems(ctx, []string{itemID})
	if err != nil {
		t.Fatal(err)
	}
	items[0].People = people[itemID]
	goText := embeddings.BuildEmbeddingText(items[0])
	for _, want := range []string{"Cast: Walker as Pilot, van Dien as Rico", "Directed by DeVito, Dean", "Written by Fred, Émile"} {
		if !strings.Contains(goText, want) {
			t.Fatalf("Go text %q lacks %q", goText, want)
		}
	}
	seedRecoEmbedding(t, pool, itemID, model, goText)

	repo := NewRepo(pool)
	flagged := func() bool {
		t.Helper()
		candidates, err := repo.ListEmbeddingTextCandidates(ctx, prefix, model, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range candidates {
			if c.MediaItemID == itemID {
				return true
			}
		}
		return false
	}
	if flagged() {
		t.Fatalf("the SQL text differs from the Go text %q", goText)
	}

	// A real change is still found.
	if _, err := pool.Exec(ctx, `UPDATE media_item_embeddings SET canonical_text = 'stale' WHERE media_item_id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	if !flagged() {
		t.Fatal("a text-stale item was not flagged")
	}
	// Rows from another model are Pass 1's, not the text scan's.
	if _, err := pool.Exec(ctx, `UPDATE media_item_embeddings SET model = 'other-model' WHERE media_item_id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	if flagged() {
		t.Fatal("the text scan returned a row embedded with another model")
	}
}
