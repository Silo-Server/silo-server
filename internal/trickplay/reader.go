package trickplay

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Reader answers what players read: which files have servable sheets, and a
// file's manifest with a signed URL per sheet.
type Reader struct {
	repo  *Repository
	store interface{ Identity() string }
	urls  artworkurl.Resolver
}

// NewReader returns a reader of the manifests published into store, signing
// sheet URLs with urls. It returns nil without a database, store, or signer.
func NewReader(pool *pgxpool.Pool, store interface{ Identity() string }, urls artworkurl.Resolver) *Reader {
	if pool == nil || store == nil || urls == nil {
		return nil
	}
	return &Reader{repo: NewRepository(pool), store: store, urls: urls}
}

// TrickplayGrids returns the layout of the servable sheets of fileIDs; a
// file without any is absent.
func (r *Reader) TrickplayGrids(ctx context.Context, fileIDs []int) (map[int]catalog.TrickplayGrid, error) {
	manifests, err := r.repo.Manifests(ctx, fileIDs, r.store.Identity())
	if err != nil {
		return nil, err
	}
	grids := make(map[int]catalog.TrickplayGrid, len(manifests))
	for id, m := range manifests {
		grids[id] = catalog.TrickplayGrid{Width: m.Width, Height: m.Height, TileColumns: m.TileColumns, TileRows: m.TileRows,
			ThumbnailCount: m.ThumbnailCount, IntervalMS: m.IntervalMS, Bandwidth: m.Bandwidth}
	}
	return grids, nil
}

// SignedManifest is a manifest whose sheets a client can fetch until
// ExpiresAt.
type SignedManifest struct {
	Manifest
	// SheetURLs holds one URL per sheet, in sheet order.
	SheetURLs []string
	ExpiresAt time.Time
}

// SignedManifest returns fileID's servable manifest with signed sheet URLs,
// or false when the file has none. Either every sheet is signed or none is
// returned: a client cuts thumbnails by index and cannot skip a sheet.
func (r *Reader) SignedManifest(ctx context.Context, fileID int) (SignedManifest, bool, error) {
	manifests, err := r.repo.Manifests(ctx, []int{fileID}, r.store.Identity())
	if err != nil {
		return SignedManifest{}, false, err
	}
	manifest, ok := manifests[fileID]
	if !ok {
		return SignedManifest{}, false, nil
	}
	keys := make([]string, manifest.SheetCount)
	for i := range keys {
		keys[i] = manifest.SheetKey(i)
	}
	resolved := r.urls.ResolveURLs(ctx, keys)
	signed := SignedManifest{Manifest: manifest, SheetURLs: make([]string, len(keys))}
	for i, key := range keys {
		url, ok := resolved[key]
		if !ok || url.URL == "" {
			slog.WarnContext(ctx, "trickplay sheet could not be signed", "component", "trickplay", "file_id", fileID, "sheet", i)
			return SignedManifest{}, false, nil
		}
		signed.SheetURLs[i] = url.URL
		if url.ExpiresAt != nil && (signed.ExpiresAt.IsZero() || url.ExpiresAt.Before(signed.ExpiresAt)) {
			signed.ExpiresAt = *url.ExpiresAt
		}
	}
	return signed, true, nil
}
