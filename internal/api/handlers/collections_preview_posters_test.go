package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// fakeItemPosters signs each item poster path it is given and records the
// batches, so a test can see which posters a preview asked for.
type fakeItemPosters struct{ batches [][]string }

func (f *fakeItemPosters) PresignImageURLs(_ context.Context, paths []string, imageType, size string) map[string]string {
	f.batches = append(f.batches, slices.Clone(paths))
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		out[p] = "https://img.example/" + imageType + "/" + size + "/" + strings.ReplaceAll(p, "://", "/")
	}
	return out
}

// TestPersonalCollectionPreviewPostersDB covers the poster each smart preview
// item carries for /api/v2, the frozen /api/v1 preview body that never had
// one (and so signs none), and #193 S3/S4: a PG profile's preview holds no title above its
// ceiling and none from a library it cannot see, and signs no poster for them.
func TestPersonalCollectionPreviewPostersDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	// f.ids[0] lives in f.hidden, the rest in f.library. Ages: G, G, R, PG,
	// unrated. A PG ceiling over f.library admits only f.ids[1] and f.ids[3].
	posters := []string{"tmdb://hidden.jpg", "tmdb://one.jpg", "tmdb://restricted.jpg", "", "tmdb://unrated.jpg"}
	for i, age := range []any{0, 0, 17, 8, nil} {
		f.exec(t, `UPDATE media_items SET title=$2, content_rating_age=$3, poster_path=$4 WHERE content_id=$1`, f.ids[i], fmt.Sprintf("Preview %d", i), age, posters[i])
	}
	h := NewCollectionHandler(pgstore.NewPostgresProvider(f.pool))
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	signer := &fakeItemPosters{}
	h.ItemPosters = signer

	query := fmt.Sprintf(`{"library_ids":[%d,%d],"media_scope":"movie","match":"all","groups":[],"sort":{"field":"title","order":"asc"}}`, f.library, f.hidden)
	scope := access.Scope{AllowedLibraryIDs: []int{f.library}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	filter := catalog.AccessFilter{AllowedLibraryIDs: scope.AllowedLibraryIDs, MaturityLimits: scope.MaturityLimits}
	visible := []string{f.ids[1], f.ids[3]}

	t.Run("items carry a signed poster only when they have one", func(t *testing.T) {
		signer.batches = nil
		got, err := h.PreviewPersonalCollection(t.Context(), PersonalCollectionPreviewRequest{QueryDefinition: []byte(query), Limit: 20, WithPosters: true}, filter)
		if err != nil {
			t.Fatal(err)
		}
		if got.Total != 2 || len(got.Items) != 2 {
			t.Fatalf("preview = %+v, want only %v", got, visible)
		}
		want := []PersonalCollectionPreviewItemView{
			{ContentID: f.ids[1], Title: "Preview 1", Type: "movie", PosterURL: "https://img.example/poster/small/tmdb/one.jpg"},
			{ContentID: f.ids[3], Title: "Preview 3", Type: "movie"},
		}
		if !slices.Equal(got.Items, want) {
			t.Fatalf("items = %+v, want %+v", got.Items, want)
		}
		// One batch, holding only the visible title's poster: nothing above
		// the ceiling or from the hidden library is signed.
		if len(signer.batches) != 1 || !slices.Equal(signer.batches[0], []string{"tmdb://one.jpg"}) {
			t.Fatalf("signed batches = %v", signer.batches)
		}
	})

	t.Run("without a poster signer items carry no poster", func(t *testing.T) {
		unsigned := NewCollectionHandler(pgstore.NewPostgresProvider(f.pool))
		unsigned.Executor = h.Executor
		got, err := unsigned.PreviewPersonalCollection(t.Context(), PersonalCollectionPreviewRequest{QueryDefinition: []byte(query), Limit: 20, WithPosters: true}, filter)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range got.Items {
			if item.PosterURL != "" {
				t.Fatalf("item %s poster = %q, want none", item.ContentID, item.PosterURL)
			}
		}
	})

	t.Run("v1 preview body is unchanged and signs no posters", func(t *testing.T) {
		signer.batches = nil
		// A client cannot opt the frozen route into posters through the body.
		req := httptest.NewRequest(http.MethodPost, "/api/v1/collections/preview", strings.NewReader(`{"query_definition":`+query+`,"limit":20,"WithPosters":true}`))
		req = req.WithContext(access.SetScope(req.Context(), scope))
		rec := httptest.NewRecorder()
		h.HandlePreviewCollection(rec, req)
		want := fmt.Sprintf(`{"items":[{"content_id":%q,"title":"Preview 1","type":"movie"},{"content_id":%q,"title":"Preview 3","type":"movie"}],"total":2}`+"\n", f.ids[1], f.ids[3])
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Fatalf("v1 preview = %d %s, want %s", rec.Code, rec.Body.String(), want)
		}
		if len(signer.batches) != 0 {
			t.Fatalf("v1 preview signed batches %v, want none", signer.batches)
		}
	})
}
