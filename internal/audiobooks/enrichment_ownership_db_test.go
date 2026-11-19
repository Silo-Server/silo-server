package audiobooks

import (
	"os"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A match replaces only the credit kinds the provider supplies and fills only
// a missing cover: the narrator and cover the scanner read from the book stay.
func TestPersistKeepsTagNarratorAndLocalCoverDB(t *testing.T) {
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

	items := catalog.NewItemRepository(pool)
	persons := catalog.NewPersonRepository(pool)
	e := &Enricher{pool: pool, itemRepo: items, personRepo: persons, state: newEnrichmentStateStore(pool)}
	providerPoster := "https://images.example.test/cover.jpg"

	for _, tc := range []struct {
		name          string
		poster        string
		wantPoster    string
		wantThumbhash string
	}{
		{"local cover stays", "audiobooks/local/cover.jpg", "audiobooks/local/cover.jpg", "local-hash"},
		{"missing cover filled", "", providerPoster, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contentID := "ab-owner-" + uuid.NewString()
			if _, err := pool.Exec(ctx, `
				INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_thumbhash)
				VALUES ($1, 'audiobook', 'Book', '', '{}'::text[], $2, 'local-hash')
			`, contentID, tc.poster); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })

			var tagged []models.ItemPerson
			for i, c := range []struct {
				name string
				kind models.PersonKind
			}{{"Tag Author", models.PersonKindAuthor}, {"Tag Narrator", models.PersonKindNarrator}} {
				id, err := persons.FindOrCreate(ctx, models.Person{Name: c.name})
				if err != nil {
					t.Fatal(err)
				}
				tagged = append(tagged, models.ItemPerson{Person: models.Person{ID: id}, Kind: c.kind, SortOrder: i})
			}
			if err := items.ReplacePeople(ctx, contentID, tagged); err != nil {
				t.Fatal(err)
			}

			if err := e.persist(ctx, enrichmentItemRow{ContentID: contentID}, nil, &metadata.MetadataResult{
				HasMetadata: true,
				Overview:    "Provider overview",
				PosterPath:  providerPoster,
				People:      []models.ItemPerson{{Person: models.Person{Name: "Provider Author"}, Kind: models.PersonKindAuthor}},
			}); err != nil {
				t.Fatal(err)
			}

			people, err := items.GetPeople(ctx, contentID)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, p := range people {
				got = append(got, p.Name)
			}
			if want := []string{"Provider Author", "Tag Narrator"}; !slices.Equal(got, want) {
				t.Errorf("credits = %v, want %v", got, want)
			}
			var poster, thumbhash string
			if err := pool.QueryRow(ctx, `
				SELECT COALESCE(poster_path, ''), COALESCE(poster_thumbhash, '') FROM media_items WHERE content_id = $1
			`, contentID).Scan(&poster, &thumbhash); err != nil {
				t.Fatal(err)
			}
			if poster != tc.wantPoster || thumbhash != tc.wantThumbhash {
				t.Errorf("poster, thumbhash = %q, %q; want %q, %q", poster, thumbhash, tc.wantPoster, tc.wantThumbhash)
			}
		})
	}
}
