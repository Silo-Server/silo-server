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

// A banner, thumb, clear art or disc slot whose cache job has not finished
// holds a source but no served path yet; the job only writes while the slot
// still holds that source.
func TestPendingItemOnlyArtworkKeepsItsSource(t *testing.T) {
	const pending = "file:///m/Film/banner.jpg?v=1-1"
	t.Run("a write that offers nothing for the slot", func(t *testing.T) {
		existing := &models.MediaItem{BannerSourcePath: pending}
		item := &models.MediaItem{}
		carryItemOnlyArtwork(item, existing)
		applyBestImages(item, nil, MergeFillEmpty, "en")
		prepareItemImagesForQueue(item, existing, true)
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
		prepareItemImagesForQueue(item, existing, true)
		if item.PosterSourcePath != "" {
			t.Fatalf("poster source = %q, want it cleared", item.PosterSourcePath)
		}
	})
	t.Run("locked artwork", func(t *testing.T) {
		item := &models.MediaItem{BannerSourcePath: pending, LockedFields: []int{int(FieldImages)}}
		applyBestImages(item, []RemoteImage{{ProviderID: "tmdb", URL: "https://img.example/banner.jpg", Type: ImageBanner, Rating: 9}}, MergeFillEmpty, "en")
		if item.BannerPath != "" {
			t.Fatalf("a locked pending banner was replaced by %q", item.BannerPath)
		}
	})
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

func TestPlanExtraBackdrops(t *testing.T) {
	ctx := context.Background()
	searched := withLocalArtworkSearched(ctx, localArtworkItem)
	item := &models.MediaItem{ContentID: "movie-1", BackdropSourcePath: "file:///m/fanart.jpg"}
	backdrop := func(url string) RemoteImage { return RemoteImage{ProviderID: "nfo", URL: url, Type: ImageBackdrop} }

	t.Run("backdrops after the primary, keeping cached copies of unchanged sources", func(t *testing.T) {
		existing := []models.ItemBackdrop{
			{ID: 1, Ordinal: 1, Path: "local/movies/movie-1/aa-1/backdrop/original.r.webp", SourcePath: "file:///m/fanart-2.jpg", Thumbhash: "t2"},
			// Its folder answers and the file is missing, so it goes.
			{ID: 2, Ordinal: 2, Path: "", SourcePath: localSource(filepath.Join(writeSidecars(t, t.TempDir(), "other.jpg"), "gone.jpg"))},
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
	t.Run("a stored backdrop the search did not return stays while its folder does not answer", func(t *testing.T) {
		root := writeSidecars(t, t.TempDir(), "fanart.jpg", "fanart-1.jpg")
		unreadable := filepath.Join(t.TempDir(), "share", "extrafanart", "x.jpg")
		stored := []models.ItemBackdrop{
			{ID: 1, Ordinal: 1, Path: "local/a", SourcePath: localSource(filepath.Join(root, "fanart-1.jpg")), Thumbhash: "t1"},
			{ID: 2, Ordinal: 2, Path: "local/x", SourcePath: localSource(unreadable)},
			{ID: 3, Ordinal: 3, Path: "local/gone", SourcePath: localSource(filepath.Join(root, "fanart-2.jpg"))},
		}
		primary := &models.MediaItem{ContentID: "movie-1", BackdropSourcePath: localSource(filepath.Join(root, "fanart.jpg"))}
		got := planExtraBackdrops(itemRetention(searched), primary, stored, []RemoteImage{
			backdrop(primary.BackdropSourcePath), backdrop(stored[0].SourcePath),
		})
		want := []models.ItemBackdrop{
			{Path: "local/a", SourcePath: stored[0].SourcePath, Thumbhash: "t1"},
			{Path: "local/x", SourcePath: stored[1].SourcePath},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("planned = %+v, want %+v", got, want)
		}
	})
	t.Run("a removed extrafanart folder drops its backdrops", func(t *testing.T) {
		root := writeSidecars(t, t.TempDir(), "fanart.jpg", "movie.mkv")
		stored := []models.ItemBackdrop{{ID: 1, Ordinal: 1, Path: "local/x", SourcePath: localSource(filepath.Join(root, "ExtraFanart", "x.jpg"))}}
		primary := &models.MediaItem{ContentID: "movie-1", BackdropSourcePath: localSource(filepath.Join(root, "fanart.jpg"))}
		if got := planExtraBackdrops(itemRetention(searched), primary, stored, []RemoteImage{backdrop(primary.BackdropSourcePath)}); len(got) != 0 {
			t.Fatalf("planned = %+v, want none", got)
		}
		// While the extrafanart folder exists, a missing file inside it is
		// gone as before, and a present one the search returned stays.
		extra := filepath.Join(root, "ExtraFanart")
		if err := os.Mkdir(extra, 0o755); err != nil {
			t.Fatal(err)
		}
		writeSidecars(t, extra, "y.jpg")
		kept := localSource(filepath.Join(extra, "y.jpg"))
		got := planExtraBackdrops(itemRetention(searched), primary, append(stored, models.ItemBackdrop{ID: 2, Ordinal: 2, Path: "local/y", SourcePath: kept}),
			[]RemoteImage{backdrop(primary.BackdropSourcePath), backdrop(kept)})
		if want := []models.ItemBackdrop{{Path: "local/y", SourcePath: kept}}; !slices.Equal(got, want) {
			t.Fatalf("planned = %+v, want %+v", got, want)
		}
	})
	t.Run("an emptied extrafanart folder drops its backdrops", func(t *testing.T) {
		root := writeSidecars(t, t.TempDir(), "fanart.jpg")
		if err := os.Mkdir(filepath.Join(root, "extrafanart"), 0o755); err != nil {
			t.Fatal(err)
		}
		stored := []models.ItemBackdrop{{ID: 1, Ordinal: 1, Path: "local/x", SourcePath: localSource(filepath.Join(root, "extrafanart", "x.jpg"))}}
		primary := &models.MediaItem{ContentID: "movie-1", BackdropSourcePath: localSource(filepath.Join(root, "fanart.jpg"))}
		if got := planExtraBackdrops(itemRetention(searched), primary, stored, []RemoteImage{backdrop(primary.BackdropSourcePath)}); len(got) != 0 {
			t.Fatalf("planned = %+v, want none", got)
		}
	})
	t.Run("a replaced sidecar keeps its cached copy until the new one is cached", func(t *testing.T) {
		root := writeSidecars(t, t.TempDir(), "fanart.jpg", "fanart-1.jpg")
		oldSource := "file://" + filepath.Join(root, "fanart-1.jpg") + "?v=10-1"
		newSource := "file://" + filepath.Join(root, "fanart-1.jpg") + "?v=20-2"
		stored := []models.ItemBackdrop{{ID: 1, Ordinal: 1, Path: "local/old", SourcePath: oldSource, Thumbhash: "t"}}
		primary := &models.MediaItem{ContentID: "movie-1", BackdropSourcePath: localSource(filepath.Join(root, "fanart.jpg"))}
		got := planExtraBackdrops(itemRetention(searched), primary, stored, []RemoteImage{backdrop(primary.BackdropSourcePath), backdrop(newSource)})
		if want := []models.ItemBackdrop{{Path: "local/old", SourcePath: newSource, Thumbhash: "t"}}; !slices.Equal(got, want) {
			t.Fatalf("planned = %+v, want %+v", got, want)
		}
	})
	t.Run("a stored row without a local source goes", func(t *testing.T) {
		stored := []models.ItemBackdrop{{ID: 1, Ordinal: 1, Path: "tmdb/x", SourcePath: "tmdb://backdrop/x.jpg"}}
		if got := planExtraBackdrops(itemRetention(context.Background()), item, stored, nil); len(got) != 0 {
			t.Fatalf("planned = %+v, want none", got)
		}
	})
	t.Run("the stored order holds while a folder is unreadable", func(t *testing.T) {
		root := writeSidecars(t, t.TempDir(), "fanart.jpg", "fanart-1.jpg", "fanart-2.jpg")
		unreadable := filepath.Join(t.TempDir(), "share", "extrafanart")
		a, f1, f2 := localSource(filepath.Join(unreadable, "a.jpg")), localSource(filepath.Join(root, "fanart-1.jpg")), localSource(filepath.Join(root, "fanart-2.jpg"))
		stored := []models.ItemBackdrop{
			{ID: 1, Ordinal: 1, Path: "local/a", SourcePath: a},
			{ID: 2, Ordinal: 2, Path: "local/f1", SourcePath: f1},
		}
		primary := &models.MediaItem{ContentID: "movie-1", BackdropSourcePath: localSource(filepath.Join(root, "fanart.jpg"))}
		got := planExtraBackdrops(itemRetention(searched), primary, stored, []RemoteImage{backdrop(primary.BackdropSourcePath), backdrop(f1), backdrop(f2)})
		want := []models.ItemBackdrop{{Path: "local/a", SourcePath: a}, {Path: "local/f1", SourcePath: f1}, {SourcePath: f2}}
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

// Only backdrops come from extrafanart folders. A poster in a folder that
// happens to be named extrafanart, such as an empty mount point, keeps the
// plain rule and stays while that folder is empty.
func TestPosterInFolderNamedExtraFanartKeepsPlainRule(t *testing.T) {
	searched := withLocalArtworkSearched(context.Background(), localArtworkItem)
	mount := filepath.Join(t.TempDir(), "extrafanart")
	if err := os.Mkdir(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	const remote = "https://image.tmdb.org/t/p/original/p.jpg"
	if !itemRetention(searched).keep(ImagePoster, remote, localSource(filepath.Join(mount, "Movie (2008)-poster.jpg"))) {
		t.Fatal("dropped a poster while its folder, named extrafanart, is an empty mount point")
	}
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

	// Extra backdrops are local art only: with caching off the item keeps
	// none, unless its artwork is locked.
	service.SetAutoCacheImages(false)
	locked := *item
	locked.LockedFields = []int{int(FieldImages)}
	_ = service.syncItemExtraBackdrops(ctx, itemRetention(ctx), &locked, nil, nil)
	if len(repo.rows["movie-1"]) != 2 {
		t.Fatalf("locked item rows with caching off = %+v, want both kept", repo.rows["movie-1"])
	}
	_ = service.syncItemExtraBackdrops(ctx, itemRetention(ctx), item, nil, images)
	if len(repo.rows["movie-1"]) != 0 {
		t.Fatalf("rows with caching off = %+v, want none", repo.rows["movie-1"])
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
// with identical images get different keys, re-caching one deletes only its
// own stale prefix, and a backdrop that moves to another row keeps its key.
func TestProcessLocalItemBackdropKeysBySidecarPath(t *testing.T) {
	root := t.TempDir()
	first := writeLocalPoster(t, root)
	second := filepath.Join(root, "fanart-2.jpg")
	if err := os.WriteFile(second, []byte("poster-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(path, rowID, previous string) (*fakeByteImageCacher, *fakeItemBackdropUpdater, *fakePrefixDeleter) {
		t.Helper()
		job := localArtworkJob("file://" + path)
		job.TargetType = ImageCacheTargetItemBackdrop
		job.TargetContentID = rowID
		job.ImageType = ImageCacheImageBackdrop
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
		return cacher, backdrops, deleter
	}

	const previous = "local/movies/movie-1/0badf00d-0badf00d/backdrop/original.old.webp"
	cacher, backdrops, deleter := run(first, "42", previous)
	key := cacher.bytesReq[0].KeyDiscriminator
	if len(key) != len("00000000-00000000") || key[8] != '-' {
		t.Fatalf("KeyDiscriminator = %q, want <content hash>-<path hash>", key)
	}
	want := "local/movies/movie-1/" + key + "/backdrop/original.webp"
	if backdrops.id != 42 || backdrops.cached != want || backdrops.hash != "th" {
		t.Fatalf("backdrop update = %+v, want row 42 at %q", backdrops, want)
	}
	if !slices.Equal(deleter.prefixes, []string{"local/movies/movie-1/0badf00d-0badf00d/backdrop/"}) {
		t.Fatalf("deleted prefixes = %q", deleter.prefixes)
	}

	// Same bytes in another sidecar: another key, so neither re-cache can
	// delete the other's files.
	other, _, _ := run(second, "43", "")
	if other.bytesReq[0].KeyDiscriminator == key || other.bytesReq[0].KeyDiscriminator[:8] != key[:8] {
		t.Fatalf("keys %q and %q, want the same content hash and different path hashes", key, other.bytesReq[0].KeyDiscriminator)
	}
	// The same sidecar on another row keeps its key, and its current copy is
	// not deleted.
	moved, _, movedDeleter := run(first, "44", want)
	if moved.bytesReq[0].KeyDiscriminator != key || len(movedDeleter.prefixes) != 0 {
		t.Fatalf("moved backdrop key %q (want %q), deleted %q", moved.bytesReq[0].KeyDiscriminator, key, movedDeleter.prefixes)
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
