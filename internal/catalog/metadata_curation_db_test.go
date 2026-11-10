package catalog

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/contentid"
)

// A delegated curator's provider-ID edit is stored and acted on later by work
// that runs with no curator present: a refresh job, a refresh-debt retry, a
// scheduled refresh. Each merges the item onto the item already at the anchor
// the IDs derive or holding one of them, so the edit is held to the curator's
// libraries when it is written.
func TestCuratedProviderIDEditsStayInCurationScopeDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	id := func(offset int64) string { return fmt.Sprint(700000000 + (suffix+offset)%100000000) }

	var libA, libB int
	for _, lib := range []*int{&libA, &libB} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
			fmt.Sprintf("curation-identity-%d-%p", suffix, lib)).Scan(lib); err != nil {
			t.Fatalf("seed library: %v", err)
		}
	}
	var created []string
	seed := func(contentID, tmdb string, library int) {
		t.Helper()
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, status, tmdb_id) VALUES ($1, 'movie', 'Curation Identity', 'matched', $2)`, contentID, tmdb)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, library)
		created = append(created, contentID)
	}
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, created)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{libA, libB})
	})

	// Out of scope: one item at the anchor tmdb outB derives, one holding the
	// IMDb ID outImdb only as a durable provider row. In scope: one in library A.
	outB, outImdb, inA := id(1), fmt.Sprintf("tt%s", id(2)), id(3)
	anchorB, _ := contentid.ForMovie(contentid.ProviderIDs{Tmdb: outB})
	seed(anchorB, outB, libB)
	durableB := fmt.Sprintf("curation-identity-durable-%d", suffix)
	seed(durableB, "", libB)
	batchEquivExec(t, pool, `INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type) VALUES ($1, 'imdb', $2, 'movie')`, durableB, outImdb)
	seed(fmt.Sprintf("curation-identity-in-scope-%d", suffix), inA, libA)

	curator := access.MetadataCurationScope{LibraryIDs: []int{libA}}
	curatorCtx := access.WithMetadataCurationAuthorizer(ctx, curator.Authorizer())
	adminCtx := access.WithMetadataCurationAuthorizer(ctx, func(ctx context.Context, _ []int) error { return ctx.Err() })
	repo := NewItemRepository(pool)

	for i, tc := range []struct {
		name    string
		ctx     context.Context
		upd     MetadataUpdate
		refused bool
	}{
		{"curator picks the tmdb id of a library B title", curatorCtx, MetadataUpdate{TmdbID: new(" " + outB)}, true},
		{"curator picks an imdb id a library B title holds", curatorCtx, MetadataUpdate{ImdbID: new(outImdb)}, true},
		{"curator picks a library A title", curatorCtx, MetadataUpdate{TmdbID: new(inA)}, false},
		{"curator edits a title only", curatorCtx, MetadataUpdate{Title: new("Renamed")}, false},
		{"acting admin picks a library B title", adminCtx, MetadataUpdate{TmdbID: new(outB)}, false},
		{"automatic write picks a library B title", ctx, MetadataUpdate{TmdbID: new(outB)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := fmt.Sprintf("local:curation-identity-%d-%d", suffix, i)
			seed(item, id(int64(10+i)), libA)
			err := repo.UpdateMetadata(tc.ctx, item, &tc.upd)
			if refused := errors.Is(err, access.ErrMetadataCurationScope); refused != tc.refused || (!tc.refused && err != nil) {
				t.Fatalf("UpdateMetadata() error = %v, want refused=%v", err, tc.refused)
			}
			var tmdb, imdb string
			if err := pool.QueryRow(ctx, `SELECT COALESCE(tmdb_id, ''), COALESCE(imdb_id, '') FROM media_items WHERE content_id = $1`, item).Scan(&tmdb, &imdb); err != nil {
				t.Fatal(err)
			}
			if tc.refused && (tmdb != id(int64(10+i)) || imdb != "") {
				t.Fatalf("refused edit stored tmdb=%q imdb=%q", tmdb, imdb)
			}
		})
	}
}
