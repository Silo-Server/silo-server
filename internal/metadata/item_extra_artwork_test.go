package metadata

import (
	"context"
	"fmt"
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

func TestExistingImageHelpersCoverEveryItemSlot(t *testing.T) {
	item := &models.MediaItem{
		PosterPath: "p", PosterSourcePath: "ps", PosterThumbhash: "pt",
		LogoPath: "l", LogoSourcePath: "ls",
		BannerPath: "b", BannerSourcePath: "bs", BannerThumbhash: "bt",
		ThumbPath: "t", ThumbSourcePath: "ts", ThumbThumbhash: "tt",
		ClearArtPath: "c", ClearArtSourcePath: "cs",
		DiscPath: "d", DiscSourcePath: "ds",
	}
	for imageType, want := range map[ImageType][3]string{
		ImagePoster:   {"p", "ps", "pt"},
		ImageLogo:     {"l", "ls", ""},
		ImageBanner:   {"b", "bs", "bt"},
		ImageThumb:    {"t", "ts", "tt"},
		ImageClearArt: {"c", "cs", ""},
		ImageDisc:     {"d", "ds", ""},
		ImageStill:    {"", "", ""},
	} {
		got := [3]string{
			existingImagePath(item, imageType),
			existingImageSourcePath(item, imageType),
			existingImageThumbhash(item, imageType),
		}
		if got != want {
			t.Errorf("%s: path/source/thumbhash = %q, want %q", ImageTypeToString(imageType), got, want)
		}
	}
}

func TestApplyBestImagesFillsExtraSlots(t *testing.T) {
	item := &models.MediaItem{}
	applyBestImages(item, []RemoteImage{
		{ProviderID: "tmdb", URL: "https://img.example/banner.jpg", Type: ImageBanner, Rating: 9},
		{ProviderID: "nfo", URL: "file:///m/Film/banner.jpg", Type: ImageBanner},
		{ProviderID: "nfo", URL: "file:///m/Film/landscape.jpg", Type: ImageThumb},
		{ProviderID: "nfo", URL: "file:///m/Film/clearart.png", Type: ImageClearArt},
		{ProviderID: "tvdb", URL: "https://img.example/disc.png", Type: ImageDisc, Language: "en"},
	}, MergeFillEmpty, "en")

	want := map[string]string{
		"banner":   "file:///m/Film/banner.jpg",
		"thumb":    "file:///m/Film/landscape.jpg",
		"clearart": "file:///m/Film/clearart.png",
		// No local disc: a provider's fills the empty slot.
		"disc": "https://img.example/disc.png",
	}
	got := map[string]string{"banner": item.BannerPath, "thumb": item.ThumbPath, "clearart": item.ClearArtPath, "disc": item.DiscPath}
	for slot, path := range want {
		if got[slot] != path {
			t.Errorf("%s = %q, want %q", slot, got[slot], path)
		}
	}
}

func TestImageTypeFromKindMapsExtraKinds(t *testing.T) {
	for kind, want := range map[string]ImageType{
		"banner": ImageBanner, "thumb": ImageThumb, "clearart": ImageClearArt, "disc": ImageDisc,
		"backdrop": ImageBackdrop, "mystery": ImagePoster,
	} {
		if got := imageTypeFromKind(kind); got != want {
			t.Errorf("imageTypeFromKind(%q) = %d, want %d", kind, got, want)
		}
	}
}

func TestSettleItemOnlyArtwork(t *testing.T) {
	searched := withLocalArtworkSearched(context.Background(), localArtworkItem)
	stored := func(banner string) *models.MediaItem {
		return &models.MediaItem{
			ContentID: "movie-1", BannerPath: "local/movies/movie-1/abcd/banner/original.r.webp",
			BannerSourcePath: banner, BannerThumbhash: "bt",
		}
	}
	rebuilt := func(existing *models.MediaItem) *models.MediaItem {
		item := &models.MediaItem{ContentID: existing.ContentID, LockedFields: existing.LockedFields}
		carryItemOnlyArtwork(item, existing)
		return item
	}
	cleared := func(t *testing.T, item *models.MediaItem) {
		t.Helper()
		if item.BannerPath != "" || item.BannerSourcePath != "" || item.BannerThumbhash != "" {
			t.Fatalf("banner = %q/%q/%q, want cleared", item.BannerPath, item.BannerSourcePath, item.BannerThumbhash)
		}
	}
	kept := func(t *testing.T, item, existing *models.MediaItem) {
		t.Helper()
		if item.BannerPath != existing.BannerPath || item.BannerSourcePath != existing.BannerSourcePath || item.BannerThumbhash != "bt" {
			t.Fatalf("banner = %q/%q/%q, want kept", item.BannerPath, item.BannerSourcePath, item.BannerThumbhash)
		}
	}

	t.Run("a searched refresh clears a sidecar missing from an answering folder", func(t *testing.T) {
		dir := writeSidecars(t, t.TempDir(), "poster.jpg")
		existing := stored(localSource(filepath.Join(dir, "banner.jpg")))
		item := rebuilt(existing)
		settleItemOnlyArtwork(itemRetention(searched), item, existing, []RemoteImage{{URL: localSource(filepath.Join(dir, "poster.jpg")), Type: ImagePoster}})
		cleared(t, item)
	})
	t.Run("a searched refresh clears a sidecar it passed over", func(t *testing.T) {
		dir := writeSidecars(t, t.TempDir(), "banner.jpg")
		existing := stored(localSource(filepath.Join(dir, "banner.jpg")))
		item := rebuilt(existing)
		settleItemOnlyArtwork(itemRetention(searched), item, existing, nil)
		cleared(t, item)
	})
	t.Run("a searched refresh keeps the slot while the folder does not answer", func(t *testing.T) {
		existing := stored(localSource(filepath.Join(t.TempDir(), "unmounted", "banner.jpg")))
		item := rebuilt(existing)
		settleItemOnlyArtwork(itemRetention(searched), item, existing, nil)
		kept(t, item, existing)
	})
	t.Run("a refresh that did not search keeps the slot", func(t *testing.T) {
		dir := writeSidecars(t, t.TempDir(), "poster.jpg")
		existing := stored(localSource(filepath.Join(dir, "banner.jpg")))
		item := rebuilt(existing)
		settleItemOnlyArtwork(itemRetention(context.Background()), item, existing, []RemoteImage{{URL: "https://img.example/p.jpg", Type: ImagePoster, Rating: 5}})
		kept(t, item, existing)
	})
	t.Run("locked artwork keeps the slot", func(t *testing.T) {
		dir := writeSidecars(t, t.TempDir(), "poster.jpg")
		existing := stored(localSource(filepath.Join(dir, "banner.jpg")))
		existing.LockedFields = []int{int(FieldImages)}
		item := rebuilt(existing)
		settleItemOnlyArtwork(itemRetention(searched), item, existing, nil)
		kept(t, item, existing)
	})
	t.Run("a new sidecar drops the old thumbhash", func(t *testing.T) {
		dir := writeSidecars(t, t.TempDir(), "banner.jpg")
		existing := stored(localSource(filepath.Join(dir, "old-banner.jpg")))
		item := rebuilt(existing)
		images := []RemoteImage{{URL: localSource(filepath.Join(dir, "banner.jpg")), Type: ImageBanner}}
		applyBestImages(item, images, MergeFillEmpty, "en")
		settleItemOnlyArtwork(itemRetention(searched), item, existing, images)
		if item.BannerPath != images[0].URL || item.BannerThumbhash != "" {
			t.Fatalf("banner = %q/%q, want the new source and no thumbhash yet", item.BannerPath, item.BannerThumbhash)
		}
	})
}

func TestPlanExtraBackdrops(t *testing.T) {
	ctx := context.Background()
	searched := withLocalArtworkSearched(ctx, localArtworkItem)
	item := &models.MediaItem{ContentID: "movie-1", BackdropSourcePath: "file:///m/fanart.jpg"}
	backdrop := func(url string) RemoteImage { return RemoteImage{ProviderID: "nfo", URL: url, Type: ImageBackdrop} }

	t.Run("backdrops after the primary, keeping cached copies of unchanged sources", func(t *testing.T) {
		existing := []models.ItemBackdrop{
			{ID: 1, Ordinal: 1, Path: "local/movies/movie-1/aa-1/backdrop/original.r.webp", SourcePath: "file:///m/fanart-2.jpg", Thumbhash: "t2"},
			{ID: 2, Ordinal: 2, Path: "", SourcePath: "file:///m/gone.jpg"},
		}
		got := planExtraBackdrops(itemRetention(searched), item, existing, []RemoteImage{
			{ProviderID: "tmdb", URL: "https://img.example/b.jpg", Type: ImageBackdrop, Rating: 9},
			backdrop("file:///m/fanart.jpg"),
			backdrop("file:///m/fanart-1.jpg"),
			backdrop("file:///m/fanart-1.jpg"),
			backdrop("file:///m/fanart-2.jpg"),
		})
		want := []models.ItemBackdrop{
			{SourcePath: "file:///m/fanart-1.jpg"},
			{Path: "local/movies/movie-1/aa-1/backdrop/original.r.webp", SourcePath: "file:///m/fanart-2.jpg", Thumbhash: "t2"},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("planned = %+v, want %+v", got, want)
		}
	})
	dir := writeSidecars(t, t.TempDir(), "fanart-1.jpg")
	existing := []models.ItemBackdrop{
		{ID: 1, Ordinal: 1, Path: "local/a", SourcePath: localSource(filepath.Join(dir, "fanart-1.jpg")), Thumbhash: "t1"},
		{ID: 2, Ordinal: 2, Path: "local/b", SourcePath: localSource(filepath.Join(dir, "fanart-2.jpg"))},
		{ID: 3, Ordinal: 3, Path: "local/c", SourcePath: localSource(filepath.Join(dir, "unmounted", "x.jpg"))},
	}
	t.Run("a searched refresh that found no backdrop drops rows whose folder answers", func(t *testing.T) {
		got := planExtraBackdrops(itemRetention(searched), item, existing, []RemoteImage{{URL: "https://img.example/b.jpg", Type: ImageBackdrop, Rating: 2}})
		want := []models.ItemBackdrop{{Path: "local/c", SourcePath: existing[2].SourcePath}}
		if !slices.Equal(got, want) {
			t.Fatalf("planned = %+v, want %+v", got, want)
		}
	})
	t.Run("a refresh that did not search keeps every row", func(t *testing.T) {
		got := planExtraBackdrops(itemRetention(ctx), item, existing, nil)
		if len(got) != len(existing) {
			t.Fatalf("planned = %+v, want the stored rows", got)
		}
	})
	t.Run("a locked provider primary leaves every local backdrop to follow it", func(t *testing.T) {
		locked := &models.MediaItem{ContentID: "movie-1", LockedFields: []int{int(FieldImages)}, BackdropSourcePath: "tmdb://backdrop/x.jpg"}
		got := planExtraBackdrops(itemRetention(searched), locked, nil, []RemoteImage{backdrop("file:///m/fanart.jpg"), backdrop("file:///m/fanart-1.jpg")})
		want := []models.ItemBackdrop{{SourcePath: "file:///m/fanart.jpg"}, {SourcePath: "file:///m/fanart-1.jpg"}}
		if !slices.Equal(got, want) {
			t.Fatalf("planned = %+v, want %+v", got, want)
		}
	})
	t.Run("a primary that is not the first local backdrop is not repeated", func(t *testing.T) {
		pinned := &models.MediaItem{ContentID: "movie-1", BackdropSourcePath: "file:///m/fanart-1.jpg"}
		got := planExtraBackdrops(itemRetention(searched), pinned, nil, []RemoteImage{backdrop("file:///m/fanart.jpg"), backdrop("file:///m/fanart-1.jpg")})
		if want := []models.ItemBackdrop{{SourcePath: "file:///m/fanart.jpg"}}; !slices.Equal(got, want) {
			t.Fatalf("planned = %+v, want %+v", got, want)
		}
	})
	t.Run("locked artwork keeps stored rows", func(t *testing.T) {
		locked := &models.MediaItem{ContentID: "movie-1", LockedFields: []int{int(FieldImages)}}
		stored := []models.ItemBackdrop{{ID: 1, Ordinal: 1, Path: "local/a", SourcePath: "file:///m/a.jpg"}}
		got := planExtraBackdrops(itemRetention(searched), locked, stored, []RemoteImage{backdrop("file:///m/fanart.jpg"), backdrop("file:///m/b.jpg")})
		if !slices.Equal(got, stored) {
			t.Fatalf("planned = %+v, want stored rows", got)
		}
	})
	t.Run("capped with the primary", func(t *testing.T) {
		var images []RemoteImage
		for i := range MaxItemBackdrops + 5 {
			images = append(images, backdrop(fmt.Sprintf("file:///m/extrafanart/%02d.jpg", i)))
		}
		if got := planExtraBackdrops(itemRetention(searched), item, nil, images); len(got) != MaxItemBackdrops-1 {
			t.Fatalf("planned %d extra backdrops, want %d", len(got), MaxItemBackdrops-1)
		}
	})
}

func TestSyncItemExtraBackdropsStoresRowsAndQueuesJobs(t *testing.T) {
	ctx := context.Background()
	repo := newFakeItemBackdropRepo()
	enqueuer := &recordingImageCacheJobEnqueuer{}
	service := &MetadataService{itemBackdropRepo: repo}
	service.SetAutoCacheImages(true)
	service.SetImageCacheJobEnqueuer(enqueuer)
	item := &models.MediaItem{ContentID: "movie-1", Type: "movie", BackdropSourcePath: "file:///m/fanart.jpg"}
	images := []RemoteImage{
		{ProviderID: "nfo", URL: "file:///m/fanart.jpg", Type: ImageBackdrop},
		{ProviderID: "nfo", URL: "file:///m/fanart-1.jpg", Type: ImageBackdrop},
		{ProviderID: "nfo", URL: "file:///m/extrafanart/a.jpg", Type: ImageBackdrop},
	}

	service.syncItemExtraBackdrops(ctx, itemRetention(ctx), item, nil, images)
	rows := repo.rows["movie-1"]
	if len(rows) != 2 || rows[0].SourcePath != "file:///m/fanart-1.jpg" || rows[1].SourcePath != "file:///m/extrafanart/a.jpg" {
		t.Fatalf("stored rows = %+v", rows)
	}
	if len(enqueuer.inputs) != 2 {
		t.Fatalf("queued %d jobs, want 2", len(enqueuer.inputs))
	}
	for i, in := range enqueuer.inputs {
		if in.TargetType != ImageCacheTargetItemBackdrop || in.TargetContentID != fmt.Sprint(rows[i].ID) ||
			in.SeriesID != "movie-1" || in.ImageType != ImageCacheImageBackdrop ||
			in.ProviderID != "local" || in.ProviderContentID != "movie-1" || in.ContentType != "movies" ||
			in.SourcePath != rows[i].SourcePath || !in.RequeueSucceeded {
			t.Fatalf("job %d = %+v", i, in)
		}
	}

	// Once cached, an unchanged refresh writes nothing and queues without a
	// forced requeue.
	repo.rows["movie-1"][0].Path = "local/movies/movie-1/aa-101/backdrop/original.r.webp"
	repo.rows["movie-1"][1].Path = "local/movies/movie-1/bb-102/backdrop/original.r.webp"
	enqueuer.inputs = nil
	service.syncItemExtraBackdrops(ctx, itemRetention(ctx), item, nil, images)
	if repo.replaces != 1 {
		t.Fatalf("Replace calls = %d, want 1", repo.replaces)
	}
	for _, in := range enqueuer.inputs {
		if in.RequeueSucceeded {
			t.Fatalf("cached row requeued: %+v", in)
		}
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

// An item's backdrops share a key folder per file, so each row's key carries
// its row ID: re-caching one row deletes only its own stale prefix, never the
// primary's or another row's.
func TestProcessLocalItemBackdropKeysByRow(t *testing.T) {
	root := t.TempDir()
	path := writeLocalPoster(t, root)
	job := localArtworkJob("file://" + path)
	job.TargetType = ImageCacheTargetItemBackdrop
	job.TargetContentID = "42"
	job.ImageType = ImageCacheImageBackdrop
	const previous = "local/movies/movie-1/0badf00d-42/backdrop/original.old.webp"
	jobs := &fakeLocalImageCacheJobs{
		fakeImageCacheJobs: fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{job}},
		currentCached:      previous,
	}
	cacher := &fakeByteImageCacher{thumbhash: "th"}
	backdrops := &fakeItemBackdropUpdater{}
	deleter := &fakePrefixDeleter{bucket: "images"}
	p := NewImageCacheProcessorWithTargets(jobs, cacher, nil, ImageCacheProcessorTargets{ItemBackdrops: backdrops})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{root}})
	p.SetImagePrefixDeleter(deleter)

	stats, err := p.RunOnce(context.Background(), "w1", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Succeeded != 1 {
		t.Fatalf("stats = %+v, failed text %q", stats, jobs.failedText)
	}
	discriminator := cacher.bytesReq[0].KeyDiscriminator
	if len(discriminator) != len("00000000-42") || discriminator[8:] != "-42" {
		t.Fatalf("KeyDiscriminator = %q, want <content hash>-42", discriminator)
	}
	want := "local/movies/movie-1/" + discriminator + "/backdrop/original.webp"
	if backdrops.id != 42 || backdrops.cached != want || backdrops.hash != "th" || backdrops.source != job.SourcePath {
		t.Fatalf("backdrop update = %+v, want row 42 at %q", backdrops, want)
	}
	if !slices.Equal(deleter.prefixes, []string{"local/movies/movie-1/0badf00d-42/backdrop/"}) {
		t.Fatalf("deleted prefixes = %q", deleter.prefixes)
	}
}

func TestProcessItemJobForExtraSlotUpdatesItem(t *testing.T) {
	root := t.TempDir()
	path := writeLocalPoster(t, root)
	job := localArtworkJob("file://" + path)
	job.ImageType = ImageCacheImageDisc
	p, jobs, cacher, items, _ := newLocalProcessorForTest(job, []string{root}, "")
	if _, err := p.RunOnce(context.Background(), "w1", 10, 1); err != nil {
		t.Fatal(err)
	}
	if jobs.failedText != "" || items.imageType != ImageCacheImageDisc {
		t.Fatalf("failed %q, updated image type %q", jobs.failedText, items.imageType)
	}
	if got := ImageTypeToString(cacher.bytesReq[0].ImageType); got != "disc" {
		t.Fatalf("cached as %q, want disc", got)
	}
}

func TestCurrentTargetQueriesForExtraArtwork(t *testing.T) {
	cases := []struct {
		job                *models.MetadataImageCacheJob
		wantSource, wantAt string
	}{
		{
			job:        &models.MetadataImageCacheJob{TargetType: ImageCacheTargetItem, TargetContentID: "m", ImageType: ImageCacheImageBanner},
			wantSource: "SELECT banner_source_path FROM media_items WHERE content_id = $1",
			wantAt:     "SELECT banner_path FROM media_items WHERE content_id = $1",
		},
		{
			job:        &models.MetadataImageCacheJob{TargetType: ImageCacheTargetItem, TargetContentID: "m", ImageType: ImageCacheImageClearArt},
			wantSource: "SELECT clearart_source_path FROM media_items WHERE content_id = $1",
			wantAt:     "SELECT clearart_path FROM media_items WHERE content_id = $1",
		},
		{
			job:        &models.MetadataImageCacheJob{TargetType: ImageCacheTargetItemBackdrop, TargetContentID: "42", ImageType: ImageCacheImageBackdrop},
			wantSource: "SELECT source_path FROM media_item_backdrops WHERE id = $1::bigint",
			wantAt:     "SELECT path FROM media_item_backdrops WHERE id = $1::bigint",
		},
		{
			job:        &models.MetadataImageCacheJob{TargetType: ImageCacheTargetPerson, TargetContentID: "9", ImageType: ImageCacheImageProfile},
			wantSource: "SELECT photo_source_path FROM people WHERE id = $1::bigint",
			wantAt:     "SELECT photo_path FROM people WHERE id = $1::bigint",
		},
	}
	for _, tc := range cases {
		source, _, ok := currentTargetSourceQuery(tc.job)
		at, _, okAt := currentTargetCachedPathQuery(tc.job)
		if !ok || !okAt || source != tc.wantSource || at != tc.wantAt {
			t.Errorf("%s/%s: source %q, cached %q", tc.job.TargetType, tc.job.ImageType, source, at)
		}
	}
	// Localizations carry no banner, thumb, clear art or disc art.
	if _, _, ok := currentTargetSourceQuery(&models.MetadataImageCacheJob{TargetType: ImageCacheTargetItemLocalization, ImageType: ImageCacheImageBanner}); ok {
		t.Fatal("item_localization banner job resolved a target column")
	}
}

// The storage sweep and the revision GC delete any object no sweep surface
// references, so every item artwork slot must be one.
func TestArtworkSweepSurfacesCoverEveryItemArtworkSlot(t *testing.T) {
	covered := make(map[string]bool)
	for _, surface := range artworkSweepSurfaces() {
		covered[surface.table+"."+surface.pathCol] = true
	}
	for _, field := range itemArtworkFields(&models.MediaItem{}) {
		if column := "media_items." + ImageTypeToString(field.imageType) + "_path"; !covered[column] {
			t.Errorf("no artwork sweep surface for %s", column)
		}
	}
	if !covered["media_item_backdrops.path"] {
		t.Error("no artwork sweep surface for media_item_backdrops.path")
	}
}
