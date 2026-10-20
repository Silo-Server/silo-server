package jellycompat

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A local extra with an image advertises a Primary tag; one without keeps an
// empty tag map.
func TestExtraToBaseItemImageTags(t *testing.T) {
	codec := NewResourceIDCodec()
	h := &ItemsHandler{codec: codec, mapper: newMapper(codec, &config.Config{})}

	plain := h.extraToBaseItem(catalog.ItemExtraInfo{ContentID: "local-a", Kind: "featurette"}, "parent")
	if plain.ImageTags == nil || len(plain.ImageTags) != 0 {
		t.Fatalf("extra without image: ImageTags = %v, want empty map", plain.ImageTags)
	}

	withImage := h.extraToBaseItem(catalog.ItemExtraInfo{
		ContentID: "local-b", Kind: "featurette",
		StillPath: "local/extras/local-b/still/original.r.webp", StillURL: "https://img/still", StillThumbhash: "hash",
	}, "parent")
	if withImage.ImageTags["Primary"] == "" {
		t.Fatalf("extra with image: ImageTags = %v, want a Primary tag", withImage.ImageTags)
	}
}

type fakeImageExtraRepo map[string]*models.MediaExtra

func (r fakeImageExtraRepo) GetByID(_ context.Context, contentID string) (*models.MediaExtra, error) {
	if extra, ok := r[contentID]; ok {
		return extra, nil
	}
	return nil, catalog.ErrExtraNotFound
}

// accessCheckingImageItemRepo records which items had their access checked
// and denies them unless allowed.
type accessCheckingImageItemRepo struct {
	fakeImageItemRepo
	allowed bool
	checked []string
}

func (r *accessCheckingImageItemRepo) EnsureAccessible(_ context.Context, contentID string, _ catalog.AccessFilter) error {
	r.checked = append(r.checked, contentID)
	if !r.allowed {
		return catalog.ErrItemNotFound
	}
	return nil
}

// An extra's image is gated by its parent's access, so a viewer who cannot
// see the movie cannot fetch its extras' images by id either.
func TestExtraImageUsesTheParentsAccess(t *testing.T) {
	const still = "local/extras/local-x/still/original.r.webp"
	extras := fakeImageExtraRepo{"local-x": {ContentID: "local-x", ParentID: "movie-1", StillPath: still}}
	resolve := func(repo imageItemRepository, access *catalog.AccessFilter) (catalog.ResolvedImageURL, bool, error) {
		h := &ImagesHandler{itemRepo: repo, extraRepo: extras}
		return h.resolveCatalogImageURL(t.Context(), "local-x", "Primary", 0, "", access)
	}
	movie := &models.MediaItem{ContentID: "movie-1", Type: "movie"}

	denied := &accessCheckingImageItemRepo{fakeImageItemRepo: fakeImageItemRepo{item: movie}}
	var httpErr *HTTPError
	if url, ok, err := resolve(denied, &catalog.AccessFilter{}); ok || url.URL != "" || !errors.As(err, &httpErr) || httpErr.StatusCode != 404 {
		t.Fatalf("hidden parent: url=%q ok=%v err=%v, want a 404 and no image", url.URL, ok, err)
	}
	if !slices.Equal(denied.checked, []string{"movie-1"}) {
		t.Fatalf("checked %v, want the parent movie", denied.checked)
	}

	allowed := &accessCheckingImageItemRepo{fakeImageItemRepo: fakeImageItemRepo{item: movie}, allowed: true}
	if url, ok, err := resolve(allowed, &catalog.AccessFilter{}); err != nil || !ok || url.URL != still {
		t.Fatalf("visible parent: url=%q ok=%v err=%v, want the still", url.URL, ok, err)
	}

	// Without a filter, as for a signed-tag GET, only types the compat surface
	// never exposes are hidden.
	if url, ok, err := resolve(fakeImageItemRepo{item: movie}, nil); err != nil || !ok || url.URL != still {
		t.Fatalf("anonymous: url=%q ok=%v err=%v, want the still", url.URL, ok, err)
	}
	book := &models.MediaItem{ContentID: "movie-1", Type: "audiobook"}
	if url, ok, err := resolve(fakeImageItemRepo{item: book}, nil); ok || url.URL != "" || err == nil {
		t.Fatalf("anonymous, excluded parent type: url=%q ok=%v err=%v, want not found", url.URL, ok, err)
	}
}
