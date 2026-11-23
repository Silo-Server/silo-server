package metadata

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/bookid"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Reset to Provider on a season or episode (#3581) clears that row's locks and
// gives every lockable field the provider's value, empty included, so an edit
// the provider has nothing for doesn't stay behind. The rest of the show keeps
// its edits, and a provider that fails to answer fails the reset.
func TestResetToProvider_SeasonAndEpisodeRows(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	nonce := time.Now().UnixNano() % 100_000_000

	for i, tc := range []struct {
		name          string
		target        string
		episodesErr   error
		unlisted      bool
		otherLanguage bool
		wantErr       bool
		providerTitle string
	}{
		{name: "episode", target: RefreshTargetEpisode, providerTitle: "Provider episode"},
		{name: "season", target: RefreshTargetSeason, providerTitle: "Provider season"},
		// The provider answered and has no such episode, so nothing of it stays.
		{name: "episode the provider doesn't list", target: RefreshTargetEpisode, unlisted: true, providerTitle: "Episode 1"},
		// A refresh in another language than the series' own leaves the base
		// row's text alone, so the reset keeps it too.
		{name: "episode in another language", target: RefreshTargetEpisode, otherLanguage: true},
		{name: "provider outage", target: RefreshTargetEpisode, episodesErr: errors.New("tvdb: HTTP 503: service unavailable"), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentifyLockFixture(pool)
			seriesID := f.seed(t, strconv.FormatInt(810_000_000+nonce+int64(i), 10))
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, seriesID)
			})
			provider := &targetRefreshProvider{
				slug:             "tvdb",
				seasons:          []SeasonResult{{SeasonNumber: 1, Title: "Provider season"}},
				episodesBySeason: map[int][]EpisodeResult{1: {{SeasonNumber: 1, EpisodeNumber: 1, Title: "Provider episode"}}},
				episodesErr:      tc.episodesErr,
			}
			if tc.unlisted {
				provider.episodesBySeason = nil
			}
			// A German series refreshed through an English library is a write
			// in another language.
			folderID := 0
			if tc.otherLanguage {
				folderID = insertTestFolder(t, pool, "series")
				if _, err := pool.Exec(t.Context(), `UPDATE media_items SET default_metadata_language = 'de' WHERE content_id = $1`, seriesID); err != nil {
					t.Fatal(err)
				}
			}
			expiresAt := time.Now().Add(time.Hour)
			f.service.chainCache = map[string]chainCacheEntry{}
			for _, kind := range []string{"series", "season", "episode"} {
				f.service.chainCache[strconv.Itoa(folderID)+":"+kind] = chainCacheEntry{providers: []Provider{provider}, expiresAt: expiresAt}
			}
			season, episode := f.read(t, seriesID)
			targetID := episode.ContentID
			if tc.target == RefreshTargetSeason {
				targetID = season.ContentID
			}

			err := f.service.ResetTargetForLibrary(t.Context(), tc.target, targetID, folderID)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ResetTargetForLibrary error = %v, want error %v", err, tc.wantErr)
			}

			season, episode = f.read(t, seriesID)
			if tc.otherLanguage {
				if episode.Title != identifyEditTitle || episode.Overview != identifyEditOverview || episode.Tagline != identifyEditTagline || episode.AirDate != nil || episode.Runtime != 0 || len(episode.LockedFields) != 0 {
					t.Errorf("episode = title %q overview %q tagline %q air_date %v runtime %d locks %v, want the text kept, nothing else and no locks",
						episode.Title, episode.Overview, episode.Tagline, episode.AirDate, episode.Runtime, episode.LockedFields)
				}
				assertChildRow(t, "season", false, "", childRow{season.Title, season.Overview, season.AirDate, 0, season.LockedFields}, identifySeasonLocks)
				return
			}
			assertChildRow(t, "season", tc.target == RefreshTargetSeason && !tc.wantErr, tc.providerTitle,
				childRow{season.Title, season.Overview, season.AirDate, 0, season.LockedFields}, identifySeasonLocks)
			assertChildRow(t, "episode", tc.target == RefreshTargetEpisode && !tc.wantErr, tc.providerTitle,
				childRow{episode.Title, episode.Overview, episode.AirDate, episode.Runtime, episode.LockedFields}, identifyEpisodeLocks)
			// No provider writes the title details, so a reset leaves them empty.
			if !tc.wantErr {
				details := []string{episode.SortTitle, episode.OriginalTitle, episode.Tagline}
				if tc.target == RefreshTargetSeason {
					details = []string{season.OriginalTitle, season.Tagline, season.TvdbID}
				}
				if slices.ContainsFunc(details, func(v string) bool { return v != "" }) {
					t.Errorf("%s title details = %q, want them emptied", tc.target, details)
				}
			}
		})
	}
}

// childRow is a season's or episode's lockable fields and locks.
type childRow struct {
	title, overview string
	airDate         *time.Time
	runtime         int
	locks           []int
}

// assertChildRow checks a season or episode row: reset, it holds the
// provider's title and nothing the provider lacks, with no locks; otherwise it
// keeps the seeded edits and locks.
func assertChildRow(t *testing.T, name string, reset bool, providerTitle string, row childRow, seededLocks []int) {
	t.Helper()
	if reset {
		if row.title != providerTitle || row.overview != "" || row.airDate != nil || row.runtime != 0 || len(row.locks) != 0 {
			t.Errorf("%s = %+v, want the provider's title, nothing else and no locks", name, row)
		}
		return
	}
	if row.title != identifyEditTitle || row.overview != identifyEditOverview || row.airDate == nil || !slices.Equal(row.locks, seededLocks) {
		t.Errorf("%s = %+v, want the edits and locks kept", name, row)
	}
}

// A movie's or series' locks are kept by the stored row, which a refresh's item
// write leaves alone, so Reset to Provider (#3581) has to clear them in that
// write's transaction: otherwise the edits' locks, a locked poster's among
// them, outlive the reset.
func TestResetToProvider_ItemRowClearsStoredLocks(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	f := newIdentifyLockFixture(pool)
	seriesID := f.seed(t, strconv.FormatInt(820_000_000+time.Now().UnixNano()%100_000_000, 10))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, seriesID)
	})
	// A stored credit the provider no longer lists.
	personID := 820_000_000 + time.Now().UnixNano()%100_000_000
	if _, err := pool.Exec(t.Context(), `INSERT INTO people (id, name) VALUES ($1, 'Typed Actor')`, personID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM people WHERE id = $1`, personID)
	})
	if _, err := pool.Exec(t.Context(), `INSERT INTO item_people (id, content_id, person_id, kind, character, sort_order) VALUES ($1, $2, $1, $3, 'Lead', 0)`, personID, seriesID, models.PersonKindActor); err != nil {
		t.Fatal(err)
	}
	tagline := "Typed tagline"
	locks := []int{int(FieldOverview), int(FieldImages)}
	if err := f.items.UpdateMetadata(t.Context(), seriesID, &catalog.MetadataUpdate{Tagline: &tagline, LockedFields: &locks}); err != nil {
		t.Fatal(err)
	}
	remote := &remoteStubProvider{
		slug:     "tvdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Show", Overview: "Provider overview"},
	}

	if _, err := f.service.ProcessWithProviders(t.Context(), ProcessRequest{
		ContentID: seriesID, Language: "en", Mode: ModeManualRefresh, resetToProvider: true,
	}, []Provider{remote}); err != nil {
		t.Fatal(err)
	}

	item, err := f.items.GetByID(t.Context(), seriesID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Tagline != "" || item.Overview != "Provider overview" || len(item.LockedFields) != 0 {
		t.Errorf("tagline=%q overview=%q locked_fields=%v, want no tagline, the provider's overview and no locks", item.Tagline, item.Overview, item.LockedFields)
	}
	var credits int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM item_people WHERE content_id = $1`, seriesID).Scan(&credits); err != nil {
		t.Fatal(err)
	}
	if credits != 0 {
		t.Errorf("%d stored credits kept, want none: the provider lists no people", credits)
	}
	// The series' own reset leaves its seasons' and episodes' edits alone.
	f.assertLocksKept(t, seriesID)
}

// A reset whose write merges the item onto another existing item resets
// nothing there: that item's edits and locks are its own, as they are for the
// conflict-recovery retry (itemWriteSteps.forRetry).
func TestResetToProvider_DoesNotFollowARebind(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	f := newIdentifyLockFixture(pool)
	ctx := t.Context()
	suffix := strconv.FormatInt(time.Now().UnixNano()%1_000_000_000, 10)
	copyID, filmID, tmdbID := "reset-rebind-copy-"+suffix, "reset-rebind-film-"+suffix, "83"+suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{copyID, filmID})
	})
	if err := f.items.Upsert(ctx, &models.MediaItem{ContentID: copyID, Type: "movie", Title: "Film", SortTitle: "film", Year: 2006, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := f.items.Upsert(ctx, &models.MediaItem{
		ContentID: filmID, Type: "movie", Title: "Film", SortTitle: "film", Year: 2006, Status: "matched", TmdbID: tmdbID,
		Overview: "Curated overview", Tagline: "Curated tagline",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.providerIDs.ReplaceByContentID(ctx, filmID, map[string]string{"tmdb": tmdbID}); err != nil {
		t.Fatal(err)
	}
	locks := []int{int(FieldOverview)}
	if err := f.items.UpdateMetadata(ctx, filmID, &catalog.MetadataUpdate{LockedFields: &locks}); err != nil {
		t.Fatal(err)
	}

	result, err := f.service.mergeAndPersist(ctx, ProcessRequest{
		ContentID: copyID, Language: "en", Mode: ModeManualRefresh, resetToProvider: true,
	}, &MetadataResult{
		HasMetadata: true, Title: "Film", Year: 2006, Overview: "Provider overview",
		ProviderIDs: map[string]string{"tmdb": tmdbID},
	}, nil, nil, nil, "movie")
	if err != nil {
		t.Fatalf("mergeAndPersist: %v", err)
	}
	if result == nil || result.ContentID != filmID {
		t.Fatalf("result = %#v, want the write rebound onto %s", result, filmID)
	}
	got, err := f.items.GetByID(ctx, filmID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tagline != "Curated tagline" || got.Overview != "Curated overview" || !slices.Equal(got.LockedFields, locks) {
		t.Errorf("tagline %q overview %q locks %v, want the item's own edits and lock kept", got.Tagline, got.Overview, got.LockedFields)
	}
}

// An ebook's refresh through the metadata service stores the work id its
// provider's answer claims, beside a claim another source made; Reset to
// Provider clears every claim before writing the answer's own.
func TestEbookRefreshStoresWorkClaimsDB(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	f := newIdentifyLockFixture(pool)
	ctx := t.Context()
	suffix := strconv.FormatInt(time.Now().UnixNano()%1_000_000_000, 10)
	ebookID, isbn, work := "ebook-work-svc-"+suffix, "9791"+fmt.Sprintf("%09s", suffix), "OL"+suffix+"W"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, ebookID)
	})
	if err := f.items.Upsert(ctx, &models.MediaItem{ContentID: ebookID, Type: "ebook", Title: "Book", SortTitle: "book", Status: "matched"}); err != nil {
		t.Fatal(err)
	}
	if err := f.providerIDs.ReplaceByContentID(ctx, ebookID, map[string]string{"isbn": isbn}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_work_ids (content_id, provider, provider_id, evidence, source) VALUES ($1, 'hardcover', '7', 'unknown', 'other')`, ebookID); err != nil {
		t.Fatal(err)
	}
	remote := &remoteStubProvider{slug: "books-metadata", metadata: &MetadataResult{
		HasMetadata: true, Title: "Book", ProviderIDs: map[string]string{"isbn": isbn},
		WorkClaims: []bookid.WorkClaims{{Source: "books-metadata", Claims: []bookid.WorkClaim{
			{Provider: bookid.KeyOpenLibraryWork, ProviderID: work, Evidence: bookid.EvidenceISBN},
		}}},
	}}

	for _, reset := range []bool{false, true} {
		if _, err := f.service.ProcessWithProviders(ctx, ProcessRequest{
			ContentID: ebookID, Language: "en", Mode: ModeManualRefresh, resetToProvider: reset,
		}, []Provider{remote}); err != nil {
			t.Fatalf("refresh (reset %v): %v", reset, err)
		}
		want := []string{"hardcover=7/unknown/other", "openlibrary_work=" + work + "/isbn/books-metadata"}
		if reset {
			want = want[1:]
		}
		if got := workClaimRows(t, pool, ebookID); !slices.Equal(got, want) {
			t.Fatalf("work ids after the refresh (reset %v) = %q, want %q", reset, got, want)
		}
	}
}

// workClaimRows lists an item's work ids as key=value/evidence/source, sorted.
func workClaimRows(t *testing.T, pool *pgxpool.Pool, contentID string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT provider || '=' || provider_id || '/' || evidence || '/' || source
		FROM media_item_work_ids WHERE content_id = $1 ORDER BY 1`, contentID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// newWorkClaimEbook stores a matched ebook holding isbn and the given work id
// rows (key, value, source), and returns its content id.
func newWorkClaimEbook(t *testing.T, f *identifyLockFixture, pool *pgxpool.Pool, name, isbn string, rows ...[3]string) string {
	t.Helper()
	ctx := t.Context()
	contentID := name + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	if err := f.items.Upsert(ctx, &models.MediaItem{ContentID: contentID, Type: "ebook", Title: "Book", SortTitle: "book", Status: "matched"}); err != nil {
		t.Fatal(err)
	}
	if err := f.providerIDs.ReplaceByContentID(ctx, contentID, map[string]string{"isbn": isbn}); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_item_work_ids (content_id, provider, provider_id, evidence, source) VALUES ($1, $2, $3, 'unknown', $4)`,
			contentID, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
	return contentID
}

// An answer on the metadata service's path whose edition id contradicts the
// ebook's names another edition: its work id is not stored, and its
// capability's earlier claim stays. An agreeing answer replaces that claim.
func TestEbookRefreshDropsWorkClaimsOfAContradictingAnswerDB(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	f := newIdentifyLockFixture(pool)
	ebookID := newWorkClaimEbook(t, f, pool, "ebook-work-contradict", "9780306406157",
		[3]string{bookid.KeyOpenLibraryWork, "OL1W", "books-metadata"})

	for _, tc := range []struct {
		isbn string
		want []string
	}{
		{"9780140449136", []string{"openlibrary_work=OL1W/unknown/books-metadata"}},
		{"9780306406157", []string{"openlibrary_work=OL2W/isbn/books-metadata"}},
	} {
		remote := &remoteStubProvider{slug: "books-metadata", metadata: &MetadataResult{
			HasMetadata: true, Title: "Book", ProviderIDs: map[string]string{"isbn": tc.isbn},
			WorkClaims: []bookid.WorkClaims{{Source: "books-metadata", Claims: []bookid.WorkClaim{
				{Provider: bookid.KeyOpenLibraryWork, ProviderID: "OL2W", Evidence: bookid.EvidenceISBN},
			}}},
		}}
		if _, err := f.service.ProcessWithProviders(t.Context(), ProcessRequest{
			ContentID: ebookID, Language: "en", Mode: ModeManualRefresh,
		}, []Provider{remote}); err != nil {
			t.Fatalf("refresh with isbn %s: %v", tc.isbn, err)
		}
		if got := workClaimRows(t, pool, ebookID); !slices.Equal(got, tc.want) {
			t.Fatalf("work ids after an answer with isbn %s = %q, want %q", tc.isbn, got, tc.want)
		}
	}
}

// An Identify that names another book clears the claims of the old match,
// a migrated one (no source) and another capability's, before the answer's
// own claims are applied.
func TestEbookIdentifyClearsOldWorkClaimsDB(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	f := newIdentifyLockFixture(pool)
	ebookID := newWorkClaimEbook(t, f, pool, "ebook-work-identify", "9780306406157",
		[3]string{bookid.KeyOpenLibraryWork, "OL1W", ""},
		[3]string{bookid.KeyHardcover, "7", "other"})
	const isbn = "9780140449136"
	remote := &remoteStubProvider{slug: "books-metadata", metadata: &MetadataResult{
		HasMetadata: true, Title: "Other Book", ProviderIDs: map[string]string{"isbn": isbn},
		WorkClaims: []bookid.WorkClaims{{Source: "books-metadata", Claims: []bookid.WorkClaim{
			{Provider: bookid.KeyOpenLibraryWork, ProviderID: "OL2W", Evidence: bookid.EvidenceISBN},
		}}},
	}}
	if _, err := f.service.ProcessWithProviders(t.Context(), ProcessRequest{
		ContentID: ebookID, Language: "en", Mode: ModeIdentify, ProviderIDs: map[string]string{"isbn": isbn},
	}, []Provider{remote}); err != nil {
		t.Fatalf("identify: %v", err)
	}
	want := []string{"openlibrary_work=OL2W/isbn/books-metadata"}
	if got := workClaimRows(t, pool, ebookID); !slices.Equal(got, want) {
		t.Fatalf("work ids after the identify = %q, want %q", got, want)
	}
}
