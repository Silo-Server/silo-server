package metadata

import (
	"context"

	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeItemBackdropRepo keeps rows in memory and, like the real repository,
// keeps a row's ID while its ordinal survives a Replace.
type fakeItemBackdropRepo struct {
	rows     map[string][]models.ItemBackdrop
	nextID   int64
	replaces int
}

func newFakeItemBackdropRepo() *fakeItemBackdropRepo {
	return &fakeItemBackdropRepo{rows: make(map[string][]models.ItemBackdrop), nextID: 100}
}

func (f *fakeItemBackdropRepo) ListByContentIDs(_ context.Context, contentIDs []string) (map[string][]models.ItemBackdrop, error) {
	out := make(map[string][]models.ItemBackdrop)
	for _, id := range contentIDs {
		if rows := f.rows[id]; len(rows) > 0 {
			out[id] = slices.Clone(rows)
		}
	}
	return out, nil
}

func (f *fakeItemBackdropRepo) Replace(_ context.Context, contentID string, backdrops []models.ItemBackdrop) ([]models.ItemBackdrop, error) {
	f.replaces++
	old := f.rows[contentID]
	rows := make([]models.ItemBackdrop, len(backdrops))
	for i, b := range backdrops {
		b.Ordinal = i + 1
		// As in the database, a stored row with the same source supplies the
		// cached copy.
		for _, stored := range old {
			if stored.SourcePath == b.SourcePath {
				b.Path, b.Thumbhash = stored.Path, stored.Thumbhash
				break
			}
		}
		if i < len(old) {
			b.ID = old[i].ID
		} else {
			f.nextID++
			b.ID = f.nextID
		}
		rows[i] = b
	}
	f.rows[contentID] = rows
	return slices.Clone(rows), nil
}

// itemRetention is the item level's retention for a refresh run under ctx.
func itemRetention(ctx context.Context) *localArtworkRetention {
	return newLocalArtworkRetention(ctx, "movie-1", localArtworkItem)
}

func localSource(path string) string {
	return "file://" + path + "?v=10-1"
}

// writeSidecars writes the named files into dir and returns dir.
func writeSidecars(t *testing.T, dir string, names ...string) string {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("img"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A banner, thumb, clear art or disc slot whose cache job has not finished
// holds a source but no served path yet; the job only writes while the slot
// still holds that source.
func TestPendingItemOnlyArtworkKeepsItsSource(t *testing.T) {
	const pending = "file:///m/Film/banner.jpg?v=1-1"
	t.Run("a write that offers nothing for the slot", func(t *testing.T) {
		existing := &models.MediaItem{BannerSourcePath: pending}
		item := &models.MediaItem{}
		carryItemOnlyArtwork(item, existing)
		applyBestImages(item, nil, MergeFillEmpty, "en", "")
		prepareItemImagesForQueue(item, existing, true, false)
		if item.BannerSourcePath != pending || item.BannerPath != "" {
			t.Fatalf("banner = %q from %q, want the pending source kept", item.BannerPath, item.BannerSourcePath)
		}
	})
	t.Run("a poster keeps the old rule", func(t *testing.T) {
		// Poster, backdrop and logo are not carried over, so an unchanged
		// pending source that the write offered nothing for is still cleared,
		// as a localization row whose provider withdrew its poster needs.
		const remote = "https://img.example/de-poster.jpg"
		existing := &models.MediaItem{PosterSourcePath: remote}
		item := &models.MediaItem{PosterSourcePath: remote}
		prepareItemImagesForQueue(item, existing, true, false)
		if item.PosterSourcePath != "" {
			t.Fatalf("poster source = %q, want it cleared", item.PosterSourcePath)
		}
	})
	t.Run("locked artwork", func(t *testing.T) {
		item := &models.MediaItem{BannerSourcePath: pending, LockedFields: []int{int(FieldImages)}}
		applyBestImages(item, []RemoteImage{{ProviderID: "tmdb", URL: "https://img.example/banner.jpg", Type: ImageBanner, Rating: 9}}, MergeFillEmpty, "en", "")
		if item.BannerPath != "" {
			t.Fatalf("a locked pending banner was replaced by %q", item.BannerPath)
		}
	})
}

func TestDropGoneItemOnlyArtwork(t *testing.T) {
	searched := withLocalArtworkSearched(context.Background(), localArtworkItem)
	stored := func(banner string) *models.MediaItem {
		return &models.MediaItem{
			ContentID: "movie-1", BannerPath: "local/movies/movie-1/abcd/banner/original.r.webp",
			BannerSourcePath: banner, BannerThumbhash: "bt",
		}
	}
	cleared := func(t *testing.T, item *models.MediaItem) {
		t.Helper()
		if item.BannerPath != "" || item.BannerSourcePath != "" || item.BannerThumbhash != "" {
			t.Fatalf("banner = %q/%q/%q, want cleared", item.BannerPath, item.BannerSourcePath, item.BannerThumbhash)
		}
	}
	kept := func(t *testing.T, item *models.MediaItem, source string) {
		t.Helper()
		if item.BannerPath == "" || item.BannerSourcePath != source || item.BannerThumbhash != "bt" {
			t.Fatalf("banner = %q/%q/%q, want kept", item.BannerPath, item.BannerSourcePath, item.BannerThumbhash)
		}
	}

	t.Run("a searched refresh clears a sidecar missing from an answering folder", func(t *testing.T) {
		dir := writeSidecars(t, t.TempDir(), "poster.jpg")
		item := stored(localSource(filepath.Join(dir, "banner.jpg")))
		dropGoneItemOnlyArtwork(itemRetention(searched), item, []RemoteImage{{URL: localSource(filepath.Join(dir, "poster.jpg")), Type: ImagePoster}})
		cleared(t, item)
	})
	t.Run("a searched refresh clears a sidecar it passed over", func(t *testing.T) {
		dir := writeSidecars(t, t.TempDir(), "banner.jpg")
		item := stored(localSource(filepath.Join(dir, "banner.jpg")))
		dropGoneItemOnlyArtwork(itemRetention(searched), item, nil)
		cleared(t, item)
	})
	t.Run("a searched refresh keeps the slot while the folder does not answer", func(t *testing.T) {
		source := localSource(filepath.Join(t.TempDir(), "unmounted", "banner.jpg"))
		item := stored(source)
		dropGoneItemOnlyArtwork(itemRetention(searched), item, nil)
		kept(t, item, source)
	})
	t.Run("a refresh that did not search keeps the slot", func(t *testing.T) {
		dir := writeSidecars(t, t.TempDir(), "poster.jpg")
		source := localSource(filepath.Join(dir, "banner.jpg"))
		item := stored(source)
		dropGoneItemOnlyArtwork(itemRetention(context.Background()), item, []RemoteImage{{URL: "https://img.example/p.jpg", Type: ImagePoster, Rating: 5}})
		kept(t, item, source)
	})
	t.Run("a candidate this refresh keeps the slot", func(t *testing.T) {
		dir := writeSidecars(t, t.TempDir(), "poster.jpg")
		source := localSource(filepath.Join(dir, "banner.jpg"))
		item := stored(source)
		dropGoneItemOnlyArtwork(itemRetention(searched), item, []RemoteImage{{URL: "https://img.example/banner.jpg", Type: ImageBanner, Rating: 1}})
		kept(t, item, source)
	})
	t.Run("locked artwork keeps the slot", func(t *testing.T) {
		dir := writeSidecars(t, t.TempDir(), "poster.jpg")
		source := localSource(filepath.Join(dir, "banner.jpg"))
		item := stored(source)
		item.LockedFields = []int{int(FieldImages)}
		dropGoneItemOnlyArtwork(itemRetention(searched), item, nil)
		kept(t, item, source)
	})
}

// A removed sidecar must leave the stored item: queue preparation keeps a
// slot's cached copy when the refresh offered nothing, so the drop has to
// survive it.
func TestMergeAndPersistDropsRemovedSidecarArtwork(t *testing.T) {
	h := newTestHarness()
	ctx := withLocalArtworkSearched(context.Background(), localArtworkItem)
	dir := writeSidecars(t, t.TempDir(), "poster.jpg", "landscape.jpg")
	stored := &models.MediaItem{
		ContentID: "movie-drop", Type: "movie", Title: "Film", Year: 2020, Status: "matched",
		DefaultMetadataLanguage: "en",
		BannerPath:              "local/movies/movie-drop/aa/banner/original.r.webp",
		BannerSourcePath:        localSource(filepath.Join(dir, "banner.jpg")), BannerThumbhash: "bt",
		ThumbPath:       "local/movies/movie-drop/bb/thumb/original.r.webp",
		ThumbSourcePath: localSource(filepath.Join(dir, "landscape.jpg")), ThumbThumbhash: "tt",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}
	if err := h.itemRepo.Upsert(ctx, stored); err != nil {
		t.Fatal(err)
	}
	images := []RemoteImage{
		{ProviderID: "nfo", URL: localSource(filepath.Join(dir, "poster.jpg")), Type: ImagePoster},
		{ProviderID: "nfo", URL: stored.ThumbSourcePath, Type: ImageThumb},
	}
	if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: "movie-drop", Mode: ModeScheduledRefresh, Language: "en"},
		&MetadataResult{HasMetadata: true, Title: "Film", Year: 2020, ProviderIDs: map[string]string{}}, images, nil, nil, "movie"); err != nil {
		t.Fatalf("mergeAndPersist: %v", err)
	}
	got, err := h.itemRepo.GetByID(ctx, "movie-drop")
	if err != nil {
		t.Fatal(err)
	}
	if got.BannerPath != "" || got.BannerSourcePath != "" || got.BannerThumbhash != "" {
		t.Fatalf("banner = %q/%q/%q, want dropped", got.BannerPath, got.BannerSourcePath, got.BannerThumbhash)
	}
	if got.ThumbPath != stored.ThumbPath || got.ThumbSourcePath != stored.ThumbSourcePath || got.ThumbThumbhash != "tt" {
		t.Fatalf("thumb = %q/%q/%q, want the rediscovered one kept", got.ThumbPath, got.ThumbSourcePath, got.ThumbThumbhash)
	}
}

// A primary backdrop can come from extrafanart when the item has no named or
// numbered one. Once that folder is removed and the item folder answers, a
// searched refresh lets the provider backdrop replace it; while the item
// folder itself is missing, as on an unmounted share, the cached copy stays.
func TestPrimaryBackdropFromRemovedExtraFanartIsNotKept(t *testing.T) {
	searched := withLocalArtworkSearched(context.Background(), localArtworkItem)
	root := writeSidecars(t, t.TempDir(), "movie.mkv")
	const remote = "https://image.tmdb.org/t/p/original/b.jpg"
	if itemRetention(searched).keep(ImageBackdrop, remote, localSource(filepath.Join(root, "extrafanart", "x.jpg"))) {
		t.Fatal("kept a primary backdrop whose extrafanart folder is gone")
	}
	unmounted := localSource(filepath.Join(root, "missing-share", "extrafanart", "x.jpg"))
	if !itemRetention(searched).keep(ImageBackdrop, remote, unmounted) {
		t.Fatal("dropped a primary backdrop while its item folder is unreachable")
	}
	// An extrafanart folder that links to storage which is down is
	// unavailable, not removed.
	target := filepath.Join(t.TempDir(), "fanart-share")
	if err := os.Symlink(target, filepath.Join(root, "ExtraFanart")); err != nil {
		t.Fatal(err)
	}
	if !itemRetention(searched).keep(ImageBackdrop, remote, localSource(filepath.Join(root, "ExtraFanart", "x.jpg"))) {
		t.Fatal("dropped a primary backdrop while its extrafanart link's target is unreachable")
	}
}

func TestMergeAndPersistKeepsExtraArtworkThroughLocalizationWrite(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	repo := newFakeItemBackdropRepo()
	repo.rows["movie-extra"] = []models.ItemBackdrop{{ID: 7, Ordinal: 1, Path: "local/x", SourcePath: "file:///m/fanart-1.jpg"}}
	h.service.itemBackdropRepo = repo
	stored := &models.MediaItem{
		ContentID: "movie-extra", Type: "movie", Title: "Film", Year: 2020, Status: "matched",
		DefaultMetadataLanguage: "en",
		BannerPath:              "local/movies/movie-extra/aa/banner/original.r.webp",
		BannerSourcePath:        "file:///m/banner.jpg", BannerThumbhash: "bt",
		DiscPath: "local/movies/movie-extra/bb/disc/original.r.webp", DiscSourcePath: "file:///m/disc.png",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}
	if err := h.itemRepo.Upsert(ctx, stored); err != nil {
		t.Fatal(err)
	}

	// A French refresh of an English item writes localization rows, not
	// artwork; the item row it rewrites must keep every artwork slot.
	if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: "movie-extra", Mode: ModeManualRefresh, Language: "fr"},
		&MetadataResult{HasMetadata: true, Title: "Film", Year: 2020, ProviderIDs: map[string]string{}}, nil, nil, nil, "movie"); err != nil {
		t.Fatalf("mergeAndPersist: %v", err)
	}
	got, err := h.itemRepo.GetByID(ctx, "movie-extra")
	if err != nil {
		t.Fatal(err)
	}
	if got.BannerPath != stored.BannerPath || got.BannerSourcePath != stored.BannerSourcePath || got.BannerThumbhash != "bt" ||
		got.DiscPath != stored.DiscPath || got.DiscSourcePath != stored.DiscSourcePath {
		t.Fatalf("artwork after localization write = %+v", got)
	}
	if repo.replaces != 0 {
		t.Fatalf("a localization write replaced the extra backdrops")
	}
}

type fakeItemBackdropUpdater struct {
	id                   int64
	source, cached, hash string
}

func (f *fakeItemBackdropUpdater) UpdateArtworkIfSourceMatches(_ context.Context, id int64, sourcePath, cachedPath, thumbhash string) (bool, error) {
	f.id, f.source, f.cached, f.hash = id, sourcePath, cachedPath, thumbhash
	return true, nil
}

// An item's backdrops share its key folder, and two sidecars can hold the same
// bytes, so each backdrop's key carries a hash of its sidecar's path. Two rows
// with identical images get different keys, and a backdrop that moves to
// another row keeps its key.
func TestProcessLocalItemBackdropKeysBySidecarPath(t *testing.T) {
	root := t.TempDir()
	first := writeLocalPoster(t, root)
	second := filepath.Join(root, "fanart-2.jpg")
	if err := os.WriteFile(second, []byte("poster-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(path, rowID string) (*fakeByteImageCacher, *fakeItemBackdropUpdater) {
		t.Helper()
		job := localArtworkJob("file://" + path)
		job.TargetType = ImageCacheTargetItemBackdrop
		job.TargetContentID = rowID
		job.ImageType = ImageCacheImageBackdrop
		jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{job}}
		cacher := &fakeByteImageCacher{thumbhash: "th"}
		backdrops := &fakeItemBackdropUpdater{}
		p := NewImageCacheProcessorWithTargets(jobs, cacher, nil, ImageCacheProcessorTargets{ItemBackdrops: backdrops})
		p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{root}})
		stats, err := p.RunOnce(context.Background(), "w1", 10, 1)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Succeeded != 1 {
			t.Fatalf("stats = %+v, failed text %q", stats, jobs.failedText)
		}
		return cacher, backdrops
	}

	cacher, backdrops := run(first, "42")
	key := cacher.bytesReq[0].KeyDiscriminator
	if len(key) != len("00000000-00000000") || key[8] != '-' {
		t.Fatalf("KeyDiscriminator = %q, want <content hash>-<path hash>", key)
	}
	want := "local/movies/movie-1/" + key + "/backdrop/original.webp"
	if backdrops.id != 42 || backdrops.cached != want || backdrops.hash != "th" {
		t.Fatalf("backdrop update = %+v, want row 42 at %q", backdrops, want)
	}

	// Same bytes in another sidecar: another key.
	other, _ := run(second, "43")
	if other.bytesReq[0].KeyDiscriminator == key || other.bytesReq[0].KeyDiscriminator[:8] != key[:8] {
		t.Fatalf("keys %q and %q, want the same content hash and different path hashes", key, other.bytesReq[0].KeyDiscriminator)
	}
	// The same sidecar on another row keeps its key.
	moved, _ := run(first, "44")
	if moved.bytesReq[0].KeyDiscriminator != key {
		t.Fatalf("moved backdrop key %q, want %q", moved.bytesReq[0].KeyDiscriminator, key)
	}
}
