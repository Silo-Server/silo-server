package trickplay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Reader answers what players read: which files have servable sheets, a
// file's manifest with a signed URL per sheet, and the sheets themselves.
type Reader struct {
	repo  *Repository
	store SheetStore
	urls  artworkurl.Resolver
}

// SheetStore is the assets store sheets are read from.
type SheetStore interface {
	Identity() string
	Get(ctx context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error)
}

// NewReader returns a reader of the manifests published into store, signing
// sheet URLs with urls. It returns nil without a database, store, or signer.
func NewReader(pool *pgxpool.Pool, store SheetStore, urls artworkurl.Resolver) *Reader {
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
	var latestExpiry time.Time
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
		if url.ExpiresAt != nil && url.ExpiresAt.After(latestExpiry) {
			latestExpiry = *url.ExpiresAt
		}
	}
	protected, err := r.repo.ProtectRevision(ctx, fileID, manifest.Revision, latestExpiry, r.store.Identity())
	if err != nil || !protected {
		return SignedManifest{}, false, err
	}
	return signed, true, nil
}

// OpenSheet opens sheet index of fileID's servable sheets of width, for a
// caller that proxies them (Jellyfin clients fetch sheets by position, not by
// a signed URL). It returns the sheet with an ETag naming its revision, or
// false when the file has no such sheet.
func (r *Reader) OpenSheet(ctx context.Context, fileID, width, index int) (io.ReadCloser, string, bool, error) {
	manifests, err := r.repo.Manifests(ctx, []int{fileID}, r.store.Identity())
	if err != nil {
		return nil, "", false, err
	}
	manifest, ok := manifests[fileID]
	if !ok || manifest.Width != width || index < 0 || index >= manifest.SheetCount {
		return nil, "", false, nil
	}
	body, _, err := r.store.Get(ctx, manifest.SheetKey(index))
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return nil, "", false, nil
		}
		return nil, "", false, fmt.Errorf("read trickplay sheet: %w", err)
	}
	return body, strconv.Quote(strconv.FormatInt(manifest.Revision, 10) + "-" + strconv.Itoa(index)), true, nil
}
