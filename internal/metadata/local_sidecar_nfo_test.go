package metadata_test

// Local sidecar change detection against the real NFO provider: an edited
// NFO or replaced artwork reaches a matched item on the next check, with no
// remote provider call, and an unchanged library costs no sidecar lookups.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/nfo"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// countingNFO is the real NFO provider, counting the item sidecar lookups a
// check runs.
type countingNFO struct {
	*nfo.Provider
	itemLookups atomic.Int64
}

func (c *countingNFO) ItemSidecarPaths(ctx context.Context, req metadata.MetadataRequest) metadata.LocalSidecars {
	c.itemLookups.Add(1)
	return c.Provider.ItemSidecarPaths(ctx, req)
}

// countingRemote stands in for a remote provider and counts every call.
type countingRemote struct {
	calls atomic.Int64
}

func (r *countingRemote) Slug() string       { return "remote" }
func (r *countingRemote) Name() string       { return "Remote" }
func (r *countingRemote) ForTypes() []string { return []string{"movie", "series"} }
func (r *countingRemote) GetMetadata(context.Context, metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	r.calls.Add(1)
	return &metadata.MetadataResult{HasMetadata: true, Title: "Remote Title"}, nil
}
func (r *countingRemote) GetImages(context.Context, metadata.ImageRequest) ([]metadata.RemoteImage, error) {
	r.calls.Add(1)
	return nil, nil
}

// writeNewerFile writes contents and moves the modification time forward, so
// an edit within the filesystem's timestamp resolution still registers.
func writeNewerFile(t *testing.T, path, contents string) {
	t.Helper()
	writeFixtureFile(t, path, contents)
	next := time.Now().Add(time.Minute)
	if info, err := os.Stat(path); err == nil && !info.ModTime().Before(next) {
		next = info.ModTime().Add(time.Second)
	}
	if err := os.Chtimes(path, next, next); err != nil {
		t.Fatalf("Chtimes(%s): %v", path, err)
	}
}

func movieNFO(title, plot, ids string) string {
	return `<movie><title>` + title + `</title><plot>` + plot + `</plot><year>2020</year>` + ids + `</movie>`
}

func tmdbID(id string) string {
	return `<uniqueid type="tmdb" default="true">` + id + `</uniqueid>`
}

type movieFixture struct {
	x         *metadata.NFOSeriesHarness
	local     *countingNFO
	remote    *countingRemote
	root      string
	dir       string
	video     string
	contentID string
}

const sidecarFolderID = 10

func newMovieFixture(t *testing.T, storedTmdbID string) *movieFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "movies")
	dir := filepath.Join(root, "Film (2020)")
	video := filepath.Join(dir, "Film (2020).mkv")
	writeFixtureFile(t, video, "video")
	writeFixtureFile(t, filepath.Join(dir, "movie.nfo"), movieNFO("Film NFO v1", "Plot v1", tmdbID("100")))
	writeFixtureFile(t, filepath.Join(dir, "poster.jpg"), "poster-v1")

	f := &movieFixture{
		x:         metadata.NewNFOSeriesHarness(),
		local:     &countingNFO{Provider: nfo.NewProvider()},
		remote:    &countingRemote{},
		root:      root,
		dir:       dir,
		video:     video,
		contentID: "movie-tmdb-" + storedTmdbID,
	}
	if err := f.x.SeedMatchedItem(models.MediaItem{
		ContentID:               f.contentID,
		Type:                    "movie",
		Title:                   "Film NFO v1",
		Overview:                "Plot v1",
		TmdbID:                  storedTmdbID,
		DefaultMetadataLanguage: "en",
	}); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	f.x.SeedSeriesFile(1, sidecarFolderID, video, dir, f.contentID)
	f.x.UseLocalSidecars(sidecarFolderID, f.local, f.remote)
	return f
}

// scan runs the check a library scan of root would, from a fresh listing.
func (f *movieFixture) scan(t *testing.T) int {
	t.Helper()
	return scanFolder(t, f.x, sidecarFolderID, f.root, f.root, f.dir)
}

func scanFolder(t *testing.T, x *metadata.NFOSeriesHarness, folderID int, scope string, dirs ...string) int {
	t.Helper()
	reapplied, err := x.Service().CheckLocalSidecars(context.Background(), folderID, scope, scanner.ReadSidecarListing(dirs))
	if err != nil {
		t.Fatalf("CheckLocalSidecars: %v", err)
	}
	return len(reapplied)
}

func (f *movieFixture) item(t *testing.T) *models.MediaItem {
	t.Helper()
	item, err := f.x.Item(f.contentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	return item
}

func (f *movieFixture) fingerprint() string {
	return f.x.LocalSidecarFingerprint(sidecarFolderID, metadata.RefreshTargetItem, f.contentID)
}

func TestLocalSidecarCheck_UnchangedFilesCostNoLookups(t *testing.T) {
	f := newMovieFixture(t, "100")

	if n := f.scan(t); n != 0 {
		t.Fatalf("first check re-applied %d items, want 0 (it records a baseline)", n)
	}
	if f.fingerprint() == "" {
		t.Fatal("first check recorded no fingerprint")
	}
	lookups := f.local.itemLookups.Load()
	for range 3 {
		if n := f.scan(t); n != 0 {
			t.Fatalf("unchanged check re-applied %d items", n)
		}
	}
	if got := f.local.itemLookups.Load(); got != lookups {
		t.Fatalf("unchanged checks ran %d sidecar lookups, want none", got-lookups)
	}
	if calls := f.remote.calls.Load(); calls != 0 {
		t.Fatalf("remote provider called %d times", calls)
	}
}

func TestLocalSidecarCheck_EditedNFOIsReappliedWithoutRemoteCalls(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	if n := f.scan(t); n != 1 {
		t.Fatalf("check after an NFO edit re-applied %d items, want 1", n)
	}
	item := f.item(t)
	if item.Title != "Film NFO v2" || item.Overview != "Plot v2" {
		t.Fatalf("item = %q / %q, want the edited NFO values", item.Title, item.Overview)
	}
	if calls := f.remote.calls.Load(); calls != 0 {
		t.Fatalf("a text-only change called the remote provider %d times", calls)
	}
	if n := f.scan(t); n != 0 {
		t.Fatalf("second check after the edit re-applied %d items, want 0", n)
	}
}

func TestLocalSidecarCheck_EditAppliedByAnotherNodeIsNotAppliedAgain(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	var other int
	f.x.BeforeNextLocalSidecarLock(func() { other = f.scan(t) })
	if n := f.scan(t); n != 0 {
		t.Fatalf("check that waited for the lock re-applied %d items, want 0", n)
	}
	if other != 1 {
		t.Fatalf("check that held the lock re-applied %d items, want 1", other)
	}
	if item := f.item(t); item.Title != "Film NFO v2" {
		t.Fatalf("title = %q, want Film NFO v2", item.Title)
	}
}

func TestLocalSidecarCheck_UnreadableFileListRecordsNothing(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	baseline := f.fingerprint()

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	f.x.FailFileListings(errors.New("database unavailable"))
	if n := f.scan(t); n != 0 {
		t.Fatalf("check without the item's files re-applied %d items, want 0", n)
	}
	if got := f.fingerprint(); got != baseline {
		t.Fatal("check without the item's files changed the stored fingerprint")
	}

	f.x.FailFileListings(nil)
	if n := f.scan(t); n != 1 {
		t.Fatalf("check after the files are readable again re-applied %d items, want 1", n)
	}
	if item := f.item(t); item.Title != "Film NFO v2" {
		t.Fatalf("title = %q, want Film NFO v2", item.Title)
	}
}

func TestLocalSidecarCheck_ItemLockedByAnotherCheckIsLeftToIt(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	baseline := f.fingerprint()

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	f.x.HoldLocalSidecarLocks(true)
	if n := f.scan(t); n != 0 || f.fingerprint() != baseline {
		t.Fatalf("check of a locked item re-applied %d items or moved its fingerprint", n)
	}
	if _, err := f.x.Service().CheckLocalSidecarTarget(context.Background(), metadata.RefreshTargetItem, f.contentID); err != nil {
		t.Fatalf("scheduled check of a locked item: %v, want it skipped quietly", err)
	}

	f.x.HoldLocalSidecarLocks(false)
	if n := f.scan(t); n != 1 {
		t.Fatalf("check after the lock was released re-applied %d items, want 1", n)
	}
}

func TestLocalSidecarCheck_ItemWithoutALanguageTakesItsLibrarys(t *testing.T) {
	f := newMovieFixture(t, "100")
	item := f.item(t)
	item.DefaultMetadataLanguage = ""
	if err := f.x.SeedMatchedItem(*item); err != nil {
		t.Fatalf("clear language: %v", err)
	}
	f.x.SetFolderLanguage(sidecarFolderID, "fr")
	f.scan(t)

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	if n := f.scan(t); n != 1 {
		t.Fatalf("re-applied %d items, want 1", n)
	}
	if got := f.item(t).DefaultMetadataLanguage; got != "fr" {
		t.Fatalf("language = %q, want the library's fr", got)
	}
}

func TestLocalSidecarCheck_ReappliedItemIsStoredOnce(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	upserts := f.x.LocalSidecarUpserts()

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	if n := f.scan(t); n != 1 {
		t.Fatalf("re-applied %d items, want 1", n)
	}
	// Stored under the item's lock, and not again outside it.
	if got := f.x.LocalSidecarUpserts() - upserts; got != 1 {
		t.Fatalf("re-applying an item stored its record %d times, want once", got)
	}
}

func TestLocalSidecarCheck_FailedLookupsRecordNothing(t *testing.T) {
	unavailable := errors.New("database unavailable")
	for _, tc := range []struct {
		name    string
		prepare func(f *movieFixture)
		fail    func(f *movieFixture, err error)
		nfo     string
	}{
		{
			name: "observed root",
			fail: func(f *movieFixture, err error) { f.x.FailObservedLocationLookups(err) },
			nfo:  movieNFO("Film NFO v2", "Plot v2", tmdbID("100")),
		},
		{
			name: "library language",
			prepare: func(f *movieFixture) {
				item := f.item(t)
				item.DefaultMetadataLanguage = ""
				if err := f.x.SeedMatchedItem(*item); err != nil {
					t.Fatalf("clear language: %v", err)
				}
				f.x.SetFolderLanguage(sidecarFolderID, "fr")
			},
			fail: func(f *movieFixture, err error) { f.x.FailFolderLookups(sidecarFolderID, err) },
			nfo:  movieNFO("Film NFO v2", "Plot v2", tmdbID("100")),
		},
		{
			// A conflicting new ID needs the Identify choice before it may
			// re-anchor.
			name: "Identify choice",
			fail: func(f *movieFixture, err error) { f.x.FailIdentityChoiceReads(err) },
			nfo:  movieNFO("Other Film", "Other plot", tmdbID("200")),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMovieFixture(t, "100")
			if tc.prepare != nil {
				tc.prepare(f)
			}
			f.scan(t)
			baseline := f.fingerprint()
			processed := f.x.RecordProcess()

			writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), tc.nfo)
			tc.fail(f, unavailable)
			if n := f.scan(t); n != 0 || f.fingerprint() != baseline || len(processed()) != 0 {
				t.Fatalf("check with a failed lookup re-applied %d items, ran %d refreshes or moved its fingerprint", n, len(processed()))
			}
			tc.fail(f, nil)
			if n := f.scan(t); n != 1 {
				t.Fatalf("check after the lookup recovered re-applied %d items, want 1", n)
			}
		})
	}
}

func TestLocalSidecarCheck_LockedFieldsSurviveReapply(t *testing.T) {
	f := newMovieFixture(t, "100")
	item := f.item(t)
	item.Title = "Admin Title"
	item.LockedFields = []int{int(metadata.FieldName)}
	if err := f.x.SeedMatchedItem(*item); err != nil {
		t.Fatalf("lock title: %v", err)
	}
	f.scan(t)

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	if n := f.scan(t); n != 1 {
		t.Fatalf("re-applied %d items, want 1", n)
	}
	item = f.item(t)
	if item.Title != "Admin Title" {
		t.Fatalf("locked title = %q, want Admin Title", item.Title)
	}
	if item.Overview != "Plot v2" {
		t.Fatalf("unlocked overview = %q, want Plot v2", item.Overview)
	}
}

func TestLocalSidecarCheck_PosterReplacedUnderTheSameNameGetsANewSource(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	// Apply local artwork once, so the item holds the poster's source.
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	f.scan(t)
	before := f.item(t).PosterSourcePath
	if !strings.HasPrefix(before, "file://"+filepath.Join(f.dir, "poster.jpg")+"?v=") {
		t.Fatalf("poster source = %q, want the local poster", before)
	}

	writeNewerFile(t, filepath.Join(f.dir, "poster.jpg"), "poster-v2, a different size")
	if n := f.scan(t); n != 1 {
		t.Fatalf("check after replacing poster.jpg re-applied %d items, want 1", n)
	}
	if after := f.item(t).PosterSourcePath; after == before || !strings.HasPrefix(after, "file://"+filepath.Join(f.dir, "poster.jpg")+"?v=") {
		t.Fatalf("poster source = %q after the replacement (was %q), want a new source for the same file", after, before)
	}
}

func TestLocalSidecarCheck_NewAndRemovedArtwork(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)

	fanart := filepath.Join(f.dir, "fanart.jpg")
	writeNewerFile(t, fanart, "fanart")
	if n := f.scan(t); n != 1 {
		t.Fatalf("check after a new fanart.jpg re-applied %d items, want 1", n)
	}
	if got := f.item(t).BackdropSourcePath; !strings.HasPrefix(got, "file://"+fanart+"?v=") {
		t.Fatalf("backdrop source = %q, want the new sidecar", got)
	}

	before := f.fingerprint()
	if err := os.Remove(filepath.Join(f.dir, "poster.jpg")); err != nil {
		t.Fatalf("remove poster: %v", err)
	}
	if n := f.scan(t); n != 1 {
		t.Fatalf("check after removing poster.jpg re-applied %d items, want 1", n)
	}
	if after := f.fingerprint(); after == before {
		t.Fatal("removing a sidecar left the fingerprint unchanged")
	}
	if calls := f.remote.calls.Load(); calls != 0 {
		t.Fatalf("remote provider called %d times", calls)
	}
}

func TestLocalSidecarCheck_ChangedNFOIDsReanchorThroughManualRefresh(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	processed := f.x.RecordProcess()

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Other Film", "Other plot", tmdbID("200")))
	if n := f.scan(t); n != 1 {
		t.Fatalf("re-applied %d items, want 1", n)
	}
	requests := processed()
	if len(requests) != 1 || requests[0].Mode != metadata.ModeManualRefresh || requests[0].ContentID != f.contentID {
		t.Fatalf("full refreshes = %#v, want one manual refresh of %s", requests, f.contentID)
	}
	if item := f.item(t); item.Title != "Film NFO v1" {
		t.Fatalf("title = %q: the local merge ran instead of only the refresh", item.Title)
	}
	if n := f.scan(t); n != 0 || len(processed()) != 1 {
		t.Fatalf("a recheck re-applied %d items and ran %d more refreshes, want none", n, len(processed())-1)
	}
}

func TestLocalSidecarCheck_ReanchoredItemIsReportedAndRecordedUnderItsNewID(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	baseline := f.fingerprint()
	f.x.RecordProcess("movie-tmdb-200")

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Other Film", "Other plot", tmdbID("200")))
	reapplied, err := f.x.Service().CheckLocalSidecars(context.Background(), sidecarFolderID, f.root, scanner.ReadSidecarListing([]string{f.root, f.dir}))
	if err != nil || len(reapplied) != 1 || reapplied[0] != "movie-tmdb-200" {
		t.Fatalf("re-applied %v, %v; want the item's new content ID", reapplied, err)
	}
	// Nothing is written under the old ID: in production the rename moved
	// its record.
	if got := f.fingerprint(); got != baseline {
		t.Fatal("the check recorded the moved item under its old content ID")
	}
	if f.x.LocalSidecarFingerprint(sidecarFolderID, metadata.RefreshTargetItem, "movie-tmdb-200") == "" {
		t.Fatal("the re-anchored item has no record under its new content ID")
	}
	if n := f.scan(t); n != 0 {
		t.Fatalf("a recheck re-applied %d items, want 0", n)
	}
}

func TestLocalSidecarCheck_NewIDKeyAppliesLocally(t *testing.T) {
	// Adding an ID the item lacks is not a conflict: the NFO applies
	// locally, without a remote refresh.
	f := newMovieFixture(t, "100")
	f.scan(t)
	processed := f.x.RecordProcess()

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"),
		movieNFO("Film NFO v2", "Plot v2", tmdbID("100")+`<uniqueid type="imdb">tt0000100</uniqueid>`))
	if n := f.scan(t); n != 1 {
		t.Fatalf("re-applied %d items, want 1", n)
	}
	if requests := processed(); len(requests) != 0 {
		t.Fatalf("ran %d full refreshes, want none", len(requests))
	}
	if item := f.item(t); item.Title != "Film NFO v2" {
		t.Fatalf("title = %q, want the NFO's", item.Title)
	}
}

func TestLocalSidecarCheck_IdentifyChoiceOutranksAChangedNFOID(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	f.x.MarkIdentityChosen(f.contentID)
	processed := f.x.RecordProcess()

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Other Film", "Other plot", tmdbID("200")))
	if n := f.scan(t); n != 0 {
		t.Fatalf("re-applied %d items, want 0", n)
	}
	if requests := processed(); len(requests) != 0 {
		t.Fatalf("ran %d full refreshes over an Identify choice, want none", len(requests))
	}
	if item := f.item(t); item.Title != "Film NFO v1" || item.TmdbID != "100" {
		t.Fatalf("item = %q tmdb %s, want the identified one untouched", item.Title, item.TmdbID)
	}
	if n := f.scan(t); n != 0 || len(processed()) != 0 {
		t.Fatal("a recheck acted on the same conflict again")
	}
}

func TestLocalSidecarCheck_IdentifyChoiceOutranksAnNFOThatDoesNotConfirmIt(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	f.x.MarkIdentityChosen(f.contentID)
	processed := f.x.RecordProcess()

	// The NFO now names the title only by an IMDb ID the item lacks: it no
	// longer confirms the identity the admin chose.
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Other Film", "Other plot", `<uniqueid type="imdb">tt0000200</uniqueid>`))
	if n := f.scan(t); n != 0 || len(processed()) != 0 {
		t.Fatalf("re-applied %d items and ran %d refreshes, want none", n, len(processed()))
	}
	if item := f.item(t); item.Title != "Film NFO v1" {
		t.Fatalf("title = %q, want the identified item untouched", item.Title)
	}
}

func TestLocalSidecarCheck_URLOnlyNFOThatNamesAnotherTitleReAnchors(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	processed := f.x.RecordProcess()

	// A URL-only NFO carries no fields but names a title, as a refresh reads
	// it: a link to another title is a correction.
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), "https://www.themoviedb.org/movie/200\n")
	f.scan(t)
	if requests := processed(); len(requests) != 1 || requests[0].Mode != metadata.ModeManualRefresh {
		t.Fatalf("refreshes = %#v, want one manual refresh for the linked title", requests)
	}
}

func TestLocalSidecarCheck_DeadNFOIDIsNoConflict(t *testing.T) {
	f := newMovieFixture(t, "100")
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v1", "Plot v1", tmdbID("200")))
	f.x.RecordDeadProviderID(f.contentID, "tmdb", "200")
	f.scan(t)
	processed := f.x.RecordProcess()

	// TMDB 200 is recorded dead, so the NFO naming it names nothing: its text
	// applies, as a refresh applies it, and the identity stays.
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("200")))
	if n := f.scan(t); n != 1 || len(processed()) != 0 {
		t.Fatalf("re-applied %d items and ran %d refreshes, want 1 and none", n, len(processed()))
	}
	if item := f.item(t); item.Title != "Film NFO v2" || item.TmdbID != "100" {
		t.Fatalf("item = %q tmdb %s, want the NFO's text on TMDB 100", item.Title, item.TmdbID)
	}
}

func TestLocalSidecarCheck_NFOReplacingADeadStoredIDReAnchors(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	// TMDB 100, the item's only ID, is recorded dead; the NFO then names a
	// live replacement, which a refresh would anchor the item to.
	f.x.RecordDeadProviderID(f.contentID, "tmdb", "100")
	processed := f.x.RecordProcess()
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("200")))
	f.scan(t)
	if requests := processed(); len(requests) != 1 || requests[0].Mode != metadata.ModeManualRefresh {
		t.Fatalf("refreshes = %#v, want one manual refresh to re-anchor on TMDB 200", requests)
	}
}

func TestLocalSidecarCheck_NFOReplacingOneDeadStoredIDReAnchors(t *testing.T) {
	f := newMovieFixture(t, "100")
	// The item also holds a live IMDb ID; only its TMDB anchor is dead.
	if err := f.x.SeedMatchedItem(models.MediaItem{
		ContentID: f.contentID, Type: "movie", Title: "Film NFO v1", Overview: "Plot v1",
		TmdbID: "100", ImdbID: "tt0000100", DefaultMetadataLanguage: "en",
	}); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	f.scan(t)
	f.x.RecordDeadProviderID(f.contentID, "tmdb", "100")
	processed := f.x.RecordProcess()
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("200")))
	f.scan(t)
	if requests := processed(); len(requests) != 1 || requests[0].Mode != metadata.ModeManualRefresh {
		t.Fatalf("refreshes = %#v, want one manual refresh to replace the dead TMDB anchor", requests)
	}
}

func TestLocalSidecarCheck_ConflictingUnchangedIDsAreNotApplied(t *testing.T) {
	// The item was identified as TMDB 300 while its NFO names 100: a plot
	// edit must not apply the other title's text, nor change the identity.
	f := newMovieFixture(t, "300")
	f.scan(t)
	processed := f.x.RecordProcess()

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v1", "Edited plot", tmdbID("100")))
	if n := f.scan(t); n != 0 {
		t.Fatalf("re-applied %d items, want 0", n)
	}
	if item := f.item(t); item.Overview != "Plot v1" {
		t.Fatalf("overview = %q, want the stored one", item.Overview)
	}
	if requests := processed(); len(requests) != 0 {
		t.Fatalf("ran %d full refreshes, want none", len(requests))
	}
}

func TestLocalSidecarCheck_RemovingAnIDDoesNotReanchor(t *testing.T) {
	// The item was identified as TMDB 300; its NFO still names TMDB 100.
	f := newMovieFixture(t, "300")
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v1", "Plot v1", tmdbID("100")+`<uniqueid type="imdb">tt0000100</uniqueid>`))
	f.scan(t)
	processed := f.x.RecordProcess()

	// Dropping the IMDb ID leaves the same conflicting TMDB ID.
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	if n := f.scan(t); n != 0 || len(processed()) != 0 {
		t.Fatalf("removing an ID re-applied %d items and ran %d refreshes, want none", n, len(processed()))
	}
}

func TestLocalSidecarCheck_UnreadableNFORecordsNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file permissions do not stop root from reading")
	}
	f := newMovieFixture(t, "100")
	f.scan(t)
	baseline := f.fingerprint()

	nfoPath := filepath.Join(f.dir, "movie.nfo")
	writeNewerFile(t, nfoPath, movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	if err := os.Chmod(nfoPath, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(nfoPath, 0o644) })
	if n := f.scan(t); n != 0 || f.fingerprint() != baseline {
		t.Fatalf("a check with an unreadable NFO re-applied %d items or moved its fingerprint", n)
	}

	if err := os.Chmod(nfoPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if n := f.scan(t); n != 1 {
		t.Fatalf("check after the NFO is readable again re-applied %d items, want 1", n)
	}
	if item := f.item(t); item.Title != "Film NFO v2" {
		t.Fatalf("title = %q, want the edited NFO's", item.Title)
	}
}

func TestLocalSidecarCheck_ArtworkSymlinkReplacedByACopyIsApplied(t *testing.T) {
	f := newMovieFixture(t, "100")
	// poster.jpg starts as a link, which artwork discovery skips.
	poster := filepath.Join(f.dir, "poster.jpg")
	target := filepath.Join(t.TempDir(), "poster-target.jpg")
	writeFixtureFile(t, target, "poster-target")
	if err := os.Remove(poster); err != nil {
		t.Fatalf("remove poster: %v", err)
	}
	if err := os.Symlink(target, poster); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	f.scan(t)

	// The link becomes a regular copy with the same size and time.
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if err := os.Remove(poster); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	writeFixtureFile(t, poster, "poster-target")
	if err := os.Chtimes(poster, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if n := f.scan(t); n != 1 {
		t.Fatalf("check after replacing the link with a copy re-applied %d items, want 1", n)
	}
	if got := f.item(t).PosterSourcePath; !strings.HasPrefix(got, "file://"+poster+"?v=") {
		t.Fatalf("poster source = %q, want the copied poster", got)
	}
}

func TestLocalSidecarCheck_LocalProviderAddedToALevelStartsFromFirstSight(t *testing.T) {
	// The item is matched as TMDB 100; its NFO still names TMDB 99.
	f := newMovieFixture(t, "100")
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v1", "Plot v1", tmdbID("99")))
	// At first only the seasons and episodes take local metadata.
	f.x.SetLevelChain(sidecarFolderID, "movie", f.remote)
	f.scan(t)
	processed := f.x.RecordProcess()

	// The NFO provider then leads the movie level. Its stale ID was there all
	// along, so it is a baseline, not a correction.
	f.x.SetLevelChain(sidecarFolderID, "movie", f.local, f.remote)
	writeNewerFile(t, filepath.Join(f.dir, "poster.jpg"), "poster-v2, a different size")
	if n := f.scan(t); n != 0 || len(processed()) != 0 {
		t.Fatalf("first check with the new provider re-applied %d items and ran %d refreshes, want none", n, len(processed()))
	}
}

func TestLocalSidecarCheck_UnreadableMediaRecordsNothing(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	baseline := f.fingerprint()

	// The media file is gone (an offline mount reads the same way), so its
	// sidecars cannot be trusted to have been removed.
	if err := os.Rename(f.video, f.video+".offline"); err != nil {
		t.Fatalf("hide media: %v", err)
	}
	if err := os.Remove(filepath.Join(f.dir, "movie.nfo")); err != nil {
		t.Fatalf("remove nfo: %v", err)
	}
	if n := f.scan(t); n != 0 || f.fingerprint() != baseline {
		t.Fatalf("an unreadable item re-applied %d targets or moved its fingerprint", n)
	}
}

func TestLocalSidecarCheck_OfflineSecondRootRecordsNothing(t *testing.T) {
	f := newMovieFixture(t, "100")
	// A second version on another root, whose mount is offline.
	offlineDir := filepath.Join(t.TempDir(), "offline", "Film (2020)")
	f.x.SeedSeriesFile(2, sidecarFolderID, filepath.Join(offlineDir, "Film (2020) - 4K.mkv"), offlineDir, f.contentID)
	f.scan(t)
	if f.fingerprint() != "" {
		t.Fatal("an item with an offline root was recorded")
	}

	writeFixtureFile(t, filepath.Join(offlineDir, "Film (2020) - 4K.mkv"), "video")
	f.scan(t)
	baseline := f.fingerprint()
	if baseline == "" {
		t.Fatal("an item with every root back was not recorded")
	}
	if err := os.RemoveAll(filepath.Dir(offlineDir)); err != nil {
		t.Fatalf("take the root offline: %v", err)
	}
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	if n := f.scan(t); n != 0 || f.fingerprint() != baseline {
		t.Fatalf("an item with an offline root re-applied %d targets or moved its fingerprint", n)
	}
}

func TestLocalSidecarCheck_LibrariesKeepTheirOwnRecords(t *testing.T) {
	// The same title in two libraries, each with its own NFO: checking one
	// library must not make the other look changed.
	f := newMovieFixture(t, "100")
	other := filepath.Join(t.TempDir(), "4k", "Film (2020)")
	otherVideo := filepath.Join(other, "Film (2020) 4K.mkv")
	writeFixtureFile(t, otherVideo, "video")
	writeFixtureFile(t, filepath.Join(other, "movie.nfo"), movieNFO("Film NFO v1", "Plot v1", tmdbID("100")))
	f.x.SeedSeriesFile(2, sidecarFolderID+1, otherVideo, other, f.contentID)
	f.x.UseLocalSidecars(sidecarFolderID+1, f.local, f.remote)
	scanOther := func() int {
		return scanFolder(t, f.x, sidecarFolderID+1, filepath.Dir(other), filepath.Dir(other), other)
	}

	f.scan(t)
	scanOther()
	for range 2 {
		if n := f.scan(t) + scanOther(); n != 0 {
			t.Fatalf("alternating library checks re-applied %d items, want 0", n)
		}
	}
}

func TestLocalSidecarCheck_SeasonAndEpisodeEdits(t *testing.T) {
	root, episodeFiles := buildFitnessFixture(t)
	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()
	const seriesID = "local-p90x"
	if err := x.SeedSeriesSkeleton(seriesID, "P90X"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	for i, path := range episodeFiles {
		x.SeedSeriesFile(100+i, sidecarFolderID, path, root, seriesID)
	}
	if _, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID,
		Hints: &metadata.MatchHints{
			Title:                     "P90X",
			Type:                      "series",
			FilePath:                  episodeFiles[0],
			RepresentativeFilePath:    episodeFiles[0],
			AllGroupFilePaths:         episodeFiles,
			PrimarySidecarSearchPaths: []string{root},
		},
		Language: "en",
		Mode:     metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider()}); err != nil {
		t.Fatalf("initial match: %v", err)
	}
	x.UseLocalSidecars(sidecarFolderID, nfo.NewProvider())
	season1, season2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")
	fullScan := func() int { return scanFolder(t, x, sidecarFolderID, root, root, season1, season2) }
	if n := fullScan(); n != 0 {
		t.Fatalf("baseline check re-applied %d items", n)
	}
	itemFingerprint := x.LocalSidecarFingerprint(sidecarFolderID, metadata.RefreshTargetItem, seriesID)

	writeNewerFile(t, filepath.Join(season1, "P90X S01E01 - Chest and Back.nfo"),
		`<episodedetails><title>Chest, Back and Arms</title><plot>Edited.</plot></episodedetails>`)
	if n := fullScan(); n != 1 {
		t.Fatalf("episode NFO edit re-applied %d items, want the series", n)
	}
	episodes := x.Episodes(seriesID)
	if episodes[0].Title != "Chest, Back and Arms" || episodes[0].Overview != "Edited." {
		t.Fatalf("episode = %q / %q, want the edited NFO", episodes[0].Title, episodes[0].Overview)
	}
	if got := x.LocalSidecarFingerprint(sidecarFolderID, metadata.RefreshTargetItem, seriesID); got != itemFingerprint {
		t.Fatal("an episode NFO edit changed the series' own fingerprint")
	}

	// A file-change scan of one season folder reaches that season.
	writeNewerFile(t, filepath.Join(season1, "season.nfo"),
		`<season><title>Course A: Renamed</title><seasonnumber>1</seasonnumber></season>`)
	if n := scanFolder(t, x, sidecarFolderID, season1, season1); n != 1 {
		t.Fatalf("season NFO edit re-applied %d items, want the series", n)
	}
	if seasons := x.Seasons(seriesID); seasons[0].Title != "Course A: Renamed" {
		t.Fatalf("season title = %q, want Course A: Renamed", seasons[0].Title)
	}

	// An episode that appears after the series was recorded is new: its NFO
	// applies even though it has no record yet.
	episodes = x.Episodes(seriesID)
	late := episodes[len(episodes)-1]
	x.ForgetLocalSidecar(sidecarFolderID, metadata.RefreshTargetEpisode, late.ContentID)
	writeNewerFile(t, filepath.Join(season2, "P90X S02E01 - Core.nfo"),
		`<episodedetails><title>Core Synergistics</title></episodedetails>`)
	if n := fullScan(); n != 1 {
		t.Fatalf("new episode re-applied %d items, want the series", n)
	}
	episodes = x.Episodes(seriesID)
	if got := episodes[len(episodes)-1].Title; got != "Core Synergistics" {
		t.Fatalf("new episode title = %q, want its NFO's", got)
	}
}

func TestLocalSidecarCheckTarget_AppliesAnEditedItem(t *testing.T) {
	f := newMovieFixture(t, "100")
	ctx := context.Background()
	// Items no scan recorded are left to scans.
	if _, err := f.x.Service().CheckLocalSidecarTarget(ctx, metadata.RefreshTargetItem, f.contentID); err != nil || f.fingerprint() != "" {
		t.Fatalf("unrecorded item: err=%v fingerprint=%q, want nothing recorded", err, f.fingerprint())
	}
	f.scan(t)

	upserts := f.x.LocalSidecarUpserts()
	if _, err := f.x.Service().CheckLocalSidecarTarget(ctx, metadata.RefreshTargetItem, f.contentID); err != nil {
		t.Fatalf("CheckLocalSidecarTarget: %v", err)
	}
	if got := f.x.LocalSidecarUpserts(); got != upserts {
		t.Fatalf("checking an unchanged item stored %d records, want none", got-upserts)
	}

	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	if _, err := f.x.Service().CheckLocalSidecarTarget(ctx, metadata.RefreshTargetItem, f.contentID); err != nil {
		t.Fatalf("CheckLocalSidecarTarget: %v", err)
	}
	if got := f.x.LocalSidecarUpserts(); got != upserts+1 {
		t.Fatalf("re-applying an edited item stored %d records, want 1", got-upserts)
	}
	if item := f.item(t); item.Title != "Film NFO v2" {
		t.Fatalf("title = %q, want the edited NFO's", item.Title)
	}
	if calls := f.remote.calls.Load(); calls != 0 {
		t.Fatalf("remote provider called %d times", calls)
	}
}

func TestLocalSidecarCheckTarget_ItemMovedByAnotherCheckLeavesNothingToRefresh(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)
	processed := f.x.RecordProcess("movie-tmdb-200")

	// While this check waits for the item's lock, a scan re-anchors the item
	// (the rename moves its record in production).
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Other Film", "Other plot", tmdbID("200")))
	f.x.BeforeNextLocalSidecarLock(func() {
		f.scan(t)
		f.x.ForgetLocalSidecar(sidecarFolderID, metadata.RefreshTargetItem, f.contentID)
	})
	currentID, err := f.x.Service().CheckLocalSidecarTarget(context.Background(), metadata.RefreshTargetItem, f.contentID)
	if err != nil || currentID != "" {
		t.Fatalf("CheckLocalSidecarTarget = %q, %v; want nothing left to refresh", currentID, err)
	}
	if got := len(processed()); got != 1 {
		t.Fatalf("refreshes = %d, want only the scan's", got)
	}
}

// newSeriesFixture matches the fitness series from its NFOs and enables local
// sidecar detection for its library.
func newSeriesFixture(t *testing.T) (*metadata.NFOSeriesHarness, string, string, []string) {
	t.Helper()
	root, episodeFiles := buildFitnessFixture(t)
	x := metadata.NewNFOSeriesHarness()
	const seriesID = "local-p90x"
	if err := x.SeedSeriesSkeleton(seriesID, "P90X"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	for i, path := range episodeFiles {
		x.SeedSeriesFile(100+i, sidecarFolderID, path, root, seriesID)
	}
	if _, err := x.Service().ProcessWithProviders(context.Background(), metadata.ProcessRequest{
		ContentID: seriesID,
		Hints: &metadata.MatchHints{
			Title:                     "P90X",
			Type:                      "series",
			FilePath:                  episodeFiles[0],
			RepresentativeFilePath:    episodeFiles[0],
			AllGroupFilePaths:         episodeFiles,
			PrimarySidecarSearchPaths: []string{root},
		},
		Language: "en",
		Mode:     metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider()}); err != nil {
		t.Fatalf("initial match: %v", err)
	}
	x.UseLocalSidecars(sidecarFolderID, nfo.NewProvider())
	return x, seriesID, root, episodeFiles
}

func TestLocalSidecarCheck_SeasonScanLeavesOtherSeasonsForTheFullScan(t *testing.T) {
	x, seriesID, root, _ := newSeriesFixture(t)
	season1, season2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")
	scanFolder(t, x, sidecarFolderID, root, root, season1, season2)

	// Season 2 changes while nothing is watching; then a file event scans
	// only Season 1.
	writeNewerFile(t, filepath.Join(season2, "P90X S02E01 - Core.nfo"), `<episodedetails><title>Core Synergistics</title></episodedetails>`)
	writeNewerFile(t, filepath.Join(season1, "season.nfo"), `<season><title>Course A: Renamed</title><seasonnumber>1</seasonnumber></season>`)
	if n := scanFolder(t, x, sidecarFolderID, season1, season1); n != 1 {
		t.Fatalf("season scan re-applied %d items, want the series", n)
	}
	episodes := x.Episodes(seriesID)
	if got := episodes[len(episodes)-1].Title; got == "Core Synergistics" {
		t.Fatal("a scan of Season 01 applied a Season 02 edit")
	}

	// The full scan must still see Season 2's edit.
	if n := scanFolder(t, x, sidecarFolderID, root, root, season1, season2); n != 1 {
		t.Fatalf("full scan re-applied %d items, want the series", n)
	}
	episodes = x.Episodes(seriesID)
	if got := episodes[len(episodes)-1].Title; got != "Core Synergistics" {
		t.Fatalf("Season 02 episode title = %q, want its edited NFO's", got)
	}
}

func TestLocalSidecarCheck_NewBannerAndDiscArtApply(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.scan(t)

	writeNewerFile(t, filepath.Join(f.dir, "banner.jpg"), "banner-v1")
	writeNewerFile(t, filepath.Join(f.dir, "disc.png"), "disc-v1")
	if n := f.scan(t); n != 1 {
		t.Fatalf("check after adding banner and disc art re-applied %d items, want 1", n)
	}
	item := f.item(t)
	if !strings.HasPrefix(item.BannerSourcePath, "file://"+filepath.Join(f.dir, "banner.jpg")+"?v=") {
		t.Fatalf("banner source = %q, want the new local banner", item.BannerSourcePath)
	}
	if !strings.HasPrefix(item.DiscSourcePath, "file://"+filepath.Join(f.dir, "disc.png")+"?v=") {
		t.Fatalf("disc source = %q, want the new local disc art", item.DiscSourcePath)
	}
	if n := f.scan(t); n != 0 {
		t.Fatalf("rescan re-applied %d items, want 0", n)
	}
}

func TestLocalSidecarCheck_ShowNFOSeasonNameApplies(t *testing.T) {
	x, seriesID, root, _ := newSeriesFixture(t)
	season1, season2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")
	fullScan := func() int { return scanFolder(t, x, sidecarFolderID, root, root, season1, season2) }
	// Season 2 has no season.nfo, so the show's <namedseason> names it.
	if err := os.Remove(filepath.Join(season2, "season.nfo")); err != nil {
		t.Fatalf("remove season.nfo: %v", err)
	}
	fullScan()

	writeNewerFile(t, filepath.Join(root, "tvshow.nfo"),
		`<tvshow><title>P90X</title><plot>90-day home fitness program.</plot><namedseason number="2">Course B: Doubles</namedseason></tvshow>`)
	if n := fullScan(); n != 1 {
		t.Fatalf("tvshow.nfo edit re-applied %d items, want the series", n)
	}
	seasons := x.Seasons(seriesID)
	if got := seasons[len(seasons)-1]; got.SeasonNumber != 2 || got.Title != "Course B: Doubles" {
		t.Fatalf("season %d title = %q, want the show NFO's name for season 2", got.SeasonNumber, got.Title)
	}
	if got := seasons[0].Title; got != "Course A: Classic" {
		t.Fatalf("season 1 title = %q, want its season.nfo's kept", got)
	}
}

func TestLocalSidecarCheck_ChildEditAppliedByAnotherCheckIsNotAppliedAgain(t *testing.T) {
	x, seriesID, root, _ := newSeriesFixture(t)
	season1, season2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")
	fullScan := func() int { return scanFolder(t, x, sidecarFolderID, root, root, season1, season2) }
	fullScan()

	// While this check waits for the series' lock, the season and episode
	// NFOs change again and another check applies the newer edits.
	seasonNFO := filepath.Join(season1, "season.nfo")
	episodeNFO := filepath.Join(season1, "P90X S01E01 - Chest and Back.nfo")
	writeNewerFile(t, seasonNFO, `<season><title>Course A: Edited</title><seasonnumber>1</seasonnumber></season>`)
	writeNewerFile(t, episodeNFO, `<episodedetails><title>Chest, Back and Arms</title></episodedetails>`)
	var other int
	x.BeforeNextLocalSidecarLock(func() {
		writeNewerFile(t, seasonNFO, `<season><title>Course A: Revised</title><seasonnumber>1</seasonnumber></season>`)
		writeNewerFile(t, episodeNFO, `<episodedetails><title>Chest and Back, Revised</title></episodedetails>`)
		other = fullScan()
	})
	if n := fullScan(); n != 0 {
		t.Fatalf("check that waited for the lock re-applied %d items, want 0", n)
	}
	if other != 1 {
		t.Fatalf("check that held the lock re-applied %d items, want 1", other)
	}
	if got := x.Seasons(seriesID)[0].Title; got != "Course A: Revised" {
		t.Fatalf("season title = %q, want the newer edit's", got)
	}
	if got := x.Episodes(seriesID)[0].Title; got != "Chest and Back, Revised" {
		t.Fatalf("episode title = %q, want the newer edit's", got)
	}
	// The stored fingerprints are the newer edits', so nothing is left to
	// apply.
	if n := fullScan(); n != 0 {
		t.Fatalf("next check re-applied %d items, want 0", n)
	}
}

func TestLocalSidecarCheck_ChildEditOfASeriesReAnchoredMeanwhileIsLeftToItsRefresh(t *testing.T) {
	x, seriesID, root, _ := newSeriesFixture(t)
	season1, season2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")
	fullScan := func() int { return scanFolder(t, x, sidecarFolderID, root, root, season1, season2) }
	fullScan()
	processed := x.RecordProcess("tvdb-100")

	// While this check waits for the series' lock, another check applies a
	// first ID from tvshow.nfo, and its refresh re-anchors the series.
	writeNewerFile(t, filepath.Join(season1, "P90X S01E01 - Chest and Back.nfo"),
		`<episodedetails><title>Chest, Back and Arms</title></episodedetails>`)
	x.BeforeNextLocalSidecarLock(func() {
		writeNewerFile(t, filepath.Join(root, "tvshow.nfo"), `<tvshow><title>P90X</title><uniqueid type="tvdb">100</uniqueid></tvshow>`)
		fullScan()
	})
	if n := fullScan(); n != 0 {
		t.Fatalf("check that waited for the lock re-applied %d items, want 0", n)
	}
	if got := len(processed()); got != 1 {
		t.Fatalf("refreshes = %d, want only the re-anchoring one", got)
	}
	if got := x.Episodes(seriesID)[0].Title; got != "Chest and Back" {
		t.Fatalf("episode title under the old series ID = %q, want it untouched", got)
	}
}

func TestLocalSidecarCheck_UncatalogedSeasonIsNotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file permissions do not stop root from reading")
	}
	x, seriesID, root, _ := newSeriesFixture(t)
	season1, season2, season3 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02"), filepath.Join(root, "Season 03")
	fullScan := func() int { return scanFolder(t, x, sidecarFolderID, root, root, season1, season2, season3) }
	fullScan()

	// A season folder with no season yet (its metadata is still to come),
	// holding an unreadable season.nfo, does not stop an episode's edit.
	late := filepath.Join(season3, "P90X S03E01 - Yoga.mkv")
	writeFixtureFile(t, late, "video")
	x.SeedSeriesFile(200, sidecarFolderID, late, root, seriesID)
	lateNFO := filepath.Join(season3, "season.nfo")
	writeNewerFile(t, lateNFO, `<season><title>Course C</title><seasonnumber>3</seasonnumber></season>`)
	if err := os.Chmod(lateNFO, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lateNFO, 0o644) })
	writeNewerFile(t, filepath.Join(season1, "P90X S01E01 - Chest and Back.nfo"),
		`<episodedetails><title>Chest, Back and Arms</title></episodedetails>`)
	if n := fullScan(); n != 1 {
		t.Fatalf("check re-applied %d items, want the series", n)
	}
	if got := x.Episodes(seriesID)[0].Title; got != "Chest, Back and Arms" {
		t.Fatalf("episode title = %q, want its edited NFO's", got)
	}
}

func TestLocalSidecarCheck_FirstSeenInASeasonScanRecordsTheWholeSeries(t *testing.T) {
	x, _, root, _ := newSeriesFixture(t)
	season1, season2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")

	// The first check of the series comes from a scan of one season folder.
	if n := scanFolder(t, x, sidecarFolderID, season1, season1); n != 0 {
		t.Fatalf("first season scan re-applied %d items, want 0 (a baseline)", n)
	}
	// The full scan finds nothing changed, Season 02 included.
	if n := scanFolder(t, x, sidecarFolderID, root, root, season1, season2); n != 0 {
		t.Fatalf("full scan after a first season scan re-applied %d items, want 0", n)
	}
}

func TestLocalSidecarCheck_FirstSightKeepsBaselineModeAfterAChildFailure(t *testing.T) {
	x, seriesID, root, _ := newSeriesFixture(t)
	season1, season2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")

	// The series' own files load; loading them again for its children fails.
	x.FailNthFileListing(2, errors.New("database unavailable"))
	if n := scanFolder(t, x, sidecarFolderID, root, root, season1, season2); n != 0 {
		t.Fatalf("first check re-applied %d items, want 0", n)
	}
	if x.LocalSidecarFingerprint(sidecarFolderID, metadata.RefreshTargetItem, seriesID) != "" {
		t.Fatal("a series whose children could not be checked was recorded")
	}
	// The retry is still a first sight: nothing applies.
	if n := scanFolder(t, x, sidecarFolderID, root, root, season1, season2); n != 0 {
		t.Fatalf("retry re-applied %d items, want 0 (a baseline)", n)
	}
	if x.LocalSidecarFingerprint(sidecarFolderID, metadata.RefreshTargetItem, seriesID) == "" {
		t.Fatal("the retry did not record the series")
	}
}

func TestLocalSidecarCheck_FirstSightIgnoresStoredChildRecords(t *testing.T) {
	x, seriesID, root, _ := newSeriesFixture(t)
	season1, season2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")
	scanFolder(t, x, sidecarFolderID, root, root, season1, season2)

	// The series lost its record while its episodes kept theirs; an episode
	// NFO then changes.
	x.ForgetLocalSidecar(sidecarFolderID, metadata.RefreshTargetItem, seriesID)
	writeNewerFile(t, filepath.Join(season2, "P90X S02E01 - Core.nfo"), `<episodedetails><title>Core Synergistics</title></episodedetails>`)
	if n := scanFolder(t, x, sidecarFolderID, root, root, season1, season2); n != 0 {
		t.Fatalf("first sight re-applied %d items, want 0 (a baseline)", n)
	}
	episodes := x.Episodes(seriesID)
	if got := episodes[len(episodes)-1].Title; got == "Core Synergistics" {
		t.Fatal("first sight applied an episode NFO")
	}
}

func TestLocalSidecarCheck_StoppedSeriesCheckLeavesItsChildrenForTheNextScan(t *testing.T) {
	x, seriesID, root, _ := newSeriesFixture(t)
	season1, season2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")
	scanFolder(t, x, sidecarFolderID, root, root, season1, season2)

	writeNewerFile(t, filepath.Join(root, "tvshow.nfo"), `<tvshow><title>P90X</title><plot>Renamed plot.</plot></tvshow>`)
	writeNewerFile(t, filepath.Join(season2, "P90X S02E01 - Core.nfo"), `<episodedetails><title>Core Synergistics</title></episodedetails>`)

	// The scan stops after the series itself was re-applied, while its
	// seasons and episodes are being loaded.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x.OnNthFileListing(stoppedSeriesListing, cancel)
	x.FailNthFileListing(stoppedSeriesListing, context.Canceled)
	_, _ = x.Service().CheckLocalSidecars(ctx, sidecarFolderID, root, scanner.ReadSidecarListing([]string{root, season1, season2}))

	// The next scan still applies the episode edit.
	scanFolder(t, x, sidecarFolderID, root, root, season1, season2)
	episodes := x.Episodes(seriesID)
	if got := episodes[len(episodes)-1].Title; got != "Core Synergistics" {
		t.Fatalf("Season 02 episode title = %q after the next scan, want its edited NFO's", got)
	}
}

// stoppedSeriesListing is the file listing that loads a re-applied series'
// seasons and episodes: after the series' own request and its re-apply.
const stoppedSeriesListing = 2

func TestLocalSidecarCheckTarget_StoresTheFirstBaselineOfANewlyLocalLevel(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.x.SetLevelChain(sidecarFolderID, "movie", f.remote)
	f.scan(t)
	processed := f.x.RecordProcess()
	ctx := context.Background()

	// The NFO provider now leads the movie level; the scheduled check records
	// the first baseline.
	f.x.SetLevelChain(sidecarFolderID, "movie", f.local, f.remote)
	if _, err := f.x.Service().CheckLocalSidecarTarget(ctx, metadata.RefreshTargetItem, f.contentID); err != nil {
		t.Fatalf("CheckLocalSidecarTarget: %v", err)
	}
	if f.fingerprint() == "" {
		t.Fatal("the scheduled check stored no baseline for the newly local level")
	}

	// A corrected ID after that is a correction.
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v1", "Plot v1", tmdbID("200")))
	if _, err := f.x.Service().CheckLocalSidecarTarget(ctx, metadata.RefreshTargetItem, f.contentID); err != nil {
		t.Fatalf("CheckLocalSidecarTarget: %v", err)
	}
	if requests := processed(); len(requests) != 1 || requests[0].Mode != metadata.ModeManualRefresh {
		t.Fatalf("refreshes = %#v, want one manual refresh for the corrected ID", requests)
	}
}

func TestLocalSidecarCheck_FirstBaselineOfANewlyLocalLevelIsTakenUnderTheLock(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.x.SetLevelChain(sidecarFolderID, "movie", f.remote)
	f.scan(t)
	processed := f.x.RecordProcess()

	// Another node records the first baseline while this check waits for the
	// lock, after an edit that names a corrected ID.
	f.x.SetLevelChain(sidecarFolderID, "movie", f.local, f.remote)
	locked := false
	f.x.BeforeNextLocalSidecarLock(func() {
		locked = true
		writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v1", "Plot v1", tmdbID("200")))
		f.scan(t)
	})
	writeNewerFile(t, filepath.Join(f.dir, "poster.jpg"), "poster-v2, a different size")
	f.scan(t)
	if !locked {
		t.Fatal("the first baseline of a newly local level was stored without the item lock")
	}
	// The baseline the other node stored stands: its ID is no correction.
	if n := f.scan(t); n != 0 || len(processed()) != 0 {
		t.Fatalf("a later check re-applied %d items and ran %d refreshes, want none", n, len(processed()))
	}
}

func TestLocalSidecarCheck_ScanBatchNeverOverwritesANewerRecord(t *testing.T) {
	// The item is TMDB 100; the NFO still names 200 when the movie level
	// gets a local provider.
	f := newMovieFixture(t, "100")
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v1", "Plot v1", tmdbID("200")))
	f.x.SetLevelChain(sidecarFolderID, "movie", f.remote)
	f.scan(t)
	f.x.SetLevelChain(sidecarFolderID, "movie", f.local, f.remote)
	processed := f.x.RecordProcess()

	// Scan A waits for the item lock while scan B records the first
	// baseline (200), so A finds it current and leaves it. Before A writes
	// its batch, scan C applies an edit that names 100 and stores it.
	f.x.BeforeNextLocalSidecarLock(func() {
		f.scan(t) // B
		f.x.BeforeNextLocalSidecarUpsert(func() {
			writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
			f.scan(t) // C
		})
	})
	writeNewerFile(t, filepath.Join(f.dir, "poster.jpg"), "poster-v2, a different size")
	f.scan(t) // A

	// C's record stands, so naming 200 again is a correction.
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v3", "Plot v3", tmdbID("200")))
	if _, err := f.x.Service().CheckLocalSidecarTarget(context.Background(), metadata.RefreshTargetItem, f.contentID); err != nil {
		t.Fatalf("CheckLocalSidecarTarget: %v", err)
	}
	if requests := processed(); len(requests) != 1 || requests[0].Mode != metadata.ModeManualRefresh {
		t.Fatalf("refreshes = %#v, want one manual refresh for the corrected ID", requests)
	}
}

func TestLocalSidecarCheck_EpisodeEditKeepsItsSeasonTitle(t *testing.T) {
	x, seriesID, root, _ := newSeriesFixture(t)
	season1, season2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")
	scanFolder(t, x, sidecarFolderID, root, root, season1, season2)

	writeNewerFile(t, filepath.Join(season1, "P90X S01E01 - Chest and Back.nfo"),
		`<episodedetails><title>Chest and Back, Revised</title></episodedetails>`)
	if n := scanFolder(t, x, sidecarFolderID, root, root, season1, season2); n != 1 {
		t.Fatalf("re-applied %d items, want the series", n)
	}
	if got := x.Episodes(seriesID)[0].Title; got != "Chest and Back, Revised" {
		t.Fatalf("episode title = %q, want the edited one", got)
	}
	if got := x.Seasons(seriesID)[0].Title; got != "Course A: Classic" {
		t.Fatalf("season title = %q after an episode edit, want it kept", got)
	}
}

func TestLocalSidecarCheck_ConflictingSeriesIdentitySkipsSeasonsAndEpisodes(t *testing.T) {
	x, seriesID, root, _ := newSeriesFixture(t)
	season1, season2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")
	// The series was identified as TVDB 999 while its tvshow.nfo names 111.
	writeNewerFile(t, filepath.Join(root, "tvshow.nfo"),
		`<tvshow><title>P90X</title><uniqueid type="tvdb">111</uniqueid></tvshow>`)
	series, err := x.Item(seriesID)
	if err != nil {
		t.Fatalf("load series: %v", err)
	}
	series.TvdbID = "999"
	if err := x.SeedMatchedItem(*series); err != nil {
		t.Fatalf("set identity: %v", err)
	}
	scanFolder(t, x, sidecarFolderID, root, root, season1, season2)

	writeNewerFile(t, filepath.Join(season1, "P90X S01E01 - Chest and Back.nfo"),
		`<episodedetails><title>Another Show's Episode</title></episodedetails>`)
	if n := scanFolder(t, x, sidecarFolderID, root, root, season1, season2); n != 0 {
		t.Fatalf("re-applied %d items, want 0", n)
	}
	if got := x.Episodes(seriesID)[0].Title; got != "Chest and Back" {
		t.Fatalf("episode title = %q, want the stored one", got)
	}
}
