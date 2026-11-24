package audiobooks

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/bookid"
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

// A matched record's work id reaches media_item_work_ids through the match
// and the persist, with its evidence and source, and never the provider IDs;
// a reset of the match clears it.
func TestAudiobookMatchStoresWorkClaimDB(t *testing.T) {
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

	contentID := "ab-work-" + uuid.NewString()
	asin := "B0" + strings.ToUpper(uuid.NewString()[:8])
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'audiobook', 'Book', '', '{}'::text[]);
		`, contentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id) VALUES ($1, 'audiobook', 'asin', $2)
		`, contentID, asin); err != nil {
		t.Fatal(err)
	}
	provider := &fakeAudiobookMetadataProvider{slug: "books-metadata", result: &metadata.MetadataResult{
		HasMetadata: true,
		Title:       "Book",
		ProviderIDs: map[string]string{"asin": asin, "books-metadata": "audnexus:" + asin},
		WorkClaims: []bookid.WorkClaims{{Source: "books-metadata", Claims: []bookid.WorkClaim{
			{Provider: bookid.KeyOpenLibraryWork, ProviderID: "OL1W", Evidence: bookid.EvidenceTitleAuthor},
		}}},
	}}
	e := &Enricher{pool: pool, itemRepo: catalog.NewItemRepository(pool), state: newEnrichmentStateStore(pool),
		providerIDs: catalog.NewProviderIDRepository(pool)}
	item := enrichmentItemRow{ContentID: contentID, Title: "Book", ProviderIDs: map[string]string{"asin": asin}}
	match := e.findMatch(ctx, item, []metadata.Provider{provider})
	if err := e.persist(ctx, item, match.ids, match.metadata); err != nil {
		t.Fatal(err)
	}
	readClaims := func() []string {
		t.Helper()
		rows, err := pool.Query(ctx, `
			SELECT provider || '=' || provider_id || '/' || evidence || '/' || source
			FROM media_item_work_ids WHERE content_id = $1 ORDER BY 1`, contentID)
		if err != nil {
			t.Fatal(err)
		}
		claims, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return claims
	}
	if got, want := readClaims(), []string{"openlibrary_work=OL1W/title_author/books-metadata"}; !slices.Equal(got, want) {
		t.Fatalf("work ids after the match = %q, want %q", got, want)
	}

	token, err := e.ClaimRefresh(ctx, contentID)
	if err != nil || token == "" {
		t.Fatalf("ClaimRefresh = %q, %v", token, err)
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return e.ResetMatchTx(ctx, tx, contentID, token) }); err != nil {
		t.Fatal(err)
	}
	if got := readClaims(); len(got) != 0 {
		t.Fatalf("work ids after a reset = %q, want none", got)
	}
}
