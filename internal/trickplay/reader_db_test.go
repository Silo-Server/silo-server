package trickplay

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// identityStore is a store at an identity whose objects hold their own key.
type identityStore string

func (s identityStore) Identity() string { return string(s) }

func (s identityStore) Get(_ context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	return io.NopCloser(strings.NewReader(key)), blobstore.ObjectInfo{Key: key}, nil
}

// fakeURLs signs every key but those in refuse, each expiring an hour apart.
type fakeURLs struct{ refuse map[string]bool }

func (f fakeURLs) ResolveURLs(_ context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	out := map[string]catalog.ResolvedImageURL{}
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for i, key := range keys {
		if f.refuse[key] {
			continue
		}
		expiry := base.Add(time.Duration(len(keys)-i) * time.Hour)
		out[key] = catalog.ResolvedImageURL{URL: "https://cdn.example/" + key, ExpiresAt: &expiry}
	}
	return out
}

func TestReaderSignsWholeManifestsDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	published, pending := f.file(t, folder, "published"), f.file(t, folder, "pending")
	f.reconcile(t)
	revision := f.generate(t, published, "server-a")

	reader := NewReader(f.pool, identityStore(testStore), fakeURLs{})
	grids, err := reader.TrickplayGrids(t.Context(), []int{published, pending})
	if err != nil || len(grids) != 1 || grids[published] != (catalog.TrickplayGrid{Width: 300, Height: 168, TileColumns: 10, TileRows: 8,
		ThumbnailCount: 360, IntervalMS: 10000, Bandwidth: 2000}) {
		t.Fatalf("grids %+v %v", grids, err)
	}
	signed, ok, err := reader.SignedManifest(t.Context(), published)
	if err != nil || !ok {
		t.Fatalf("manifest: %t %v", ok, err)
	}
	if len(signed.SheetURLs) != 5 || signed.SheetURLs[4] != "https://cdn.example/"+SheetKey(published, revision, 4) ||
		!signed.ExpiresAt.Equal(time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("signed %+v", signed)
	}
	if _, ok, _ := reader.SignedManifest(t.Context(), pending); ok {
		t.Fatal("a file without sheets has a manifest")
	}
	// One sheet that cannot be signed withholds the whole manifest.
	partial := NewReader(f.pool, identityStore(testStore), fakeURLs{refuse: map[string]bool{SheetKey(published, revision, 2): true}})
	if _, ok, err := partial.SignedManifest(t.Context(), published); ok || err != nil {
		t.Fatalf("partially signed manifest served: %t %v", ok, err)
	}
	// Sheets open by width and index, within the manifest.
	body, etag, ok, err := reader.OpenSheet(t.Context(), published, 300, 3)
	if err != nil || !ok {
		t.Fatalf("open sheet: %t %v", ok, err)
	}
	data, _ := io.ReadAll(body)
	_ = body.Close()
	if string(data) != SheetKey(published, revision, 3) || etag == "" {
		t.Fatalf("sheet %q etag %q", data, etag)
	}
	for _, tt := range []struct{ width, index int }{{320, 0}, {300, 5}, {300, -1}} {
		if _, _, ok, _ := reader.OpenSheet(t.Context(), published, tt.width, tt.index); ok {
			t.Errorf("sheet width %d index %d opened", tt.width, tt.index)
		}
	}
	// Another store's reader sees nothing.
	if grids, _ := NewReader(f.pool, identityStore("s3|other"), fakeURLs{}).TrickplayGrids(t.Context(), []int{published}); len(grids) != 0 {
		t.Fatal("sheets in another store are available")
	}
}

func TestReaderStopsServingOptedOutRunningFileDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	fileID := f.file(t, folder, "opt-out")
	f.reconcile(t)
	f.generate(t, fileID, "server-a")
	if _, err := f.repo.Regenerate(t.Context(), []int{fileID}); err != nil {
		t.Fatal(err)
	}
	if job, err := f.repo.ClaimFile(t.Context(), fileID, "server-b", time.Hour); err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	f.exec(t, `UPDATE public.media_folders SET trickplay_enabled=false WHERE id=$1`, folder)
	f.reconcile(t)
	if row, ok := f.row(t, fileID); !ok || row.state != stateRunning {
		t.Fatalf("running row removed: %+v %v", row, ok)
	}
	reader := NewReader(f.pool, identityStore(testStore), fakeURLs{})
	if grids, err := reader.TrickplayGrids(t.Context(), []int{fileID}); err != nil || len(grids) != 0 {
		t.Fatalf("opted-out grids: %+v %v", grids, err)
	}
	if _, ok, err := reader.SignedManifest(t.Context(), fileID); err != nil || ok {
		t.Fatalf("opted-out manifest: %v %v", ok, err)
	}
	if _, _, ok, err := reader.OpenSheet(t.Context(), fileID, 300, 0); err != nil || ok {
		t.Fatalf("opted-out sheet: %v %v", ok, err)
	}
}
