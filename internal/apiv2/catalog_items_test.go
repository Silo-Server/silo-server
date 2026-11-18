package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeCatalog backs the catalog-items operations: it records the request
// the seam received and answers a fixed page.
type fakeCatalog struct {
	sourceOrder        bool
	unreadableEpisodes bool
	err                error
	lastReq            catalogpkg.CatalogRequest
	lastViewer         handlers.ItemViewer
	lastGroup          bool
	lastGroups         catalogpkg.AudiobookGroupsQuery
}

func (f *fakeCatalog) ContextAccessFilter(ctx context.Context, opts handlers.AccessFilterOptions) (catalogpkg.AccessFilter, error) {
	return catalogpkg.AccessFilter{UserID: claimsFrom(ctx).UserID, ProfileID: "p-owner", AllowedLibraryIDs: []int{1, 2}, PresentationLibraryID: opts.PresentationLibraryID, ScopeFilesToLibrary: opts.ScopeFilesToLibrary, SeriesLibraryCoversEpisodes: opts.SeriesLibraryCoversEpisodes, SelectedFileID: opts.SelectedFileID, DeviceID: opts.DeviceID}, nil
}

func fakeListingCard(id string) handlers.CollectionItemView {
	return handlers.CollectionItemView{ContentID: id, Type: "movie", Title: "Heat", Year: 1995, Genres: []string{"Crime"}, Status: "matched"}
}

func (f *fakeCatalog) Browse(_ context.Context, v handlers.ItemViewer, req catalogpkg.CatalogRequest, grouped bool) (handlers.CatalogBrowseView, error) {
	if f.err != nil {
		return handlers.CatalogBrowseView{}, f.err
	}
	f.lastReq, f.lastViewer, f.lastGroup = req, v, grouped
	view := handlers.CatalogBrowseView{Total: 3, TotalExact: true, Snapshot: "2026-01-02T03:04:05.678Z",
		EffectiveSort: &handlers.EffectiveSortView{Field: "title", Order: "asc"}}
	if f.sourceOrder {
		view.EffectiveSort = nil
		view.ResolvedSort = new(catalogpkg.QuerySort)
	}
	ids := []string{"movie:heat-1995", "movie:alien-1979", "movie:blade-runner-1982"}
	for i := req.Offset; i < len(ids) && len(view.Items) < req.Limit; i++ {
		view.Items = append(view.Items, fakeListingCard(ids[i]))
	}
	view.HasMore = req.Offset+len(view.Items) < len(ids)
	if req.SearchQuery != "" {
		view.SearchDiagnostics = &handlers.SearchDiagnosticsView{Provider: "postgres", Mode: "keyword"}
	}
	return view, nil
}

func (f *fakeCatalog) Filters(_ context.Context, _ handlers.ItemViewer, req catalogpkg.CatalogRequest, technical bool) (handlers.CatalogFiltersView, error) {
	if f.err != nil {
		return handlers.CatalogFiltersView{}, f.err
	}
	f.lastReq = req
	view := handlers.CatalogFiltersView{Genres: []string{"Crime"}, Authors: []string{"Frank Herbert"}}
	if technical {
		res := []string{"2160p"}
		view.Resolutions, view.AudioLanguages, view.SubtitleLanguages = &res, &[]string{}, &[]string{"en"}
	}
	return view, nil
}

func (f *fakeCatalog) SearchFacet(_ context.Context, _ handlers.ItemViewer, req catalogpkg.CatalogRequest, facet, q string, limit int) (handlers.CatalogFacetSearchView, error) {
	if f.err != nil {
		return handlers.CatalogFacetSearchView{}, f.err
	}
	f.lastReq = req
	if facet == "author" && strings.HasPrefix("frank herbert", q) { //nolint:gocritic // the fake matches a typed prefix of the name
		return handlers.CatalogFacetSearchView{
			Matches: []string{"Frank Herbert"}, HasMore: limit == 1,
			Values: []catalogpkg.FacetValue{{Value: "Frank Herbert", Count: 6}}, ValuesHasMore: limit == 1,
		}, nil
	}
	if facet == "genre" && q == "" {
		return handlers.CatalogFacetSearchView{Values: []catalogpkg.FacetValue{{Value: "Drama", Count: 9}}, ValuesHasMore: true}, nil
	}
	return handlers.CatalogFacetSearchView{}, nil
}

func (f *fakeCatalog) LetterIndex(_ context.Context, v handlers.ItemViewer, req catalogpkg.CatalogRequest) (catalogpkg.LetterIndex, error) {
	if f.err != nil {
		return catalogpkg.LetterIndex{}, f.err
	}
	f.lastReq, f.lastViewer = req, v
	// A digit title, three H titles, and a non-Latin one the collation sorts
	// after them, so # has two runs.
	return catalogpkg.LetterIndex{
		Letters: []catalogpkg.LetterBucket{{Letter: "#", Count: 2, Position: 0}, {Letter: "H", Count: 3, Position: 1}},
		Runs:    []catalogpkg.LetterRun{{Letter: "#", Position: 0}, {Letter: "H", Position: 1}, {Letter: "#", Position: 4}},
	}, nil
}

func (f *fakeCatalog) AudiobookGroups(_ context.Context, v handlers.ItemViewer, q catalogpkg.AudiobookGroupsQuery) (handlers.AudiobookGroupsView, error) {
	if f.err != nil {
		return handlers.AudiobookGroupsView{}, f.err
	}
	f.lastGroups, f.lastViewer = q, v
	offset := 0
	if q.After != nil {
		offset = 1
	}
	view := handlers.AudiobookGroupsView{Total: 2, TotalExact: q.IncludeTotal, HasMore: offset == 0 && q.Limit == 1}
	names := []string{"Frank Herbert", "Ursula K. Le Guin"}
	for i := offset; i < len(names) && len(view.Groups) < q.Limit; i++ {
		view.Groups = append(view.Groups, handlers.AudiobookGroupView{Name: names[i], ItemCount: 3, TotalDurationSeconds: 7200, PosterURLs: nil})
	}
	if view.HasMore {
		view.Next = &catalogpkg.AudiobookGroupCursor{GroupKey: "frank herbert", Value: 3}
	}
	return view, nil
}

// fakeDeviceAudioOverride is the device whose device-scoped audio preference
// is French; every other caller resolves the profile's English.
const fakeDeviceAudioOverride = "phone-1"

// fakeHeatVersion answers Heat's one version with the effective audio the
// viewer's device resolves, as the detail service does.
func fakeHeatVersion(v handlers.ItemViewer) catalogpkg.FileVersion {
	version := catalogpkg.FileVersion{FileID: 120, FilePath: "/media/movies/Heat.mkv", Resolution: "2160p", AddedAt: fixedTime(),
		EffectiveAudioTrackIndex: new(1), EffectiveAudioLanguage: "en"}
	if v.Access.DeviceID == fakeDeviceAudioOverride {
		version.EffectiveAudioTrackIndex, version.EffectiveAudioLanguage = new(2), "fr"
	}
	return version
}

func notFoundItem() error {
	return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
}

func (f *fakeCatalog) ItemDetail(_ context.Context, v handlers.ItemViewer, id string) (*catalogpkg.ItemDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastViewer = v
	if id != "movie:heat-1995" {
		return nil, notFoundItem()
	}
	fileID := 120
	imdbVotes := int64(673852)
	return &catalogpkg.ItemDetail{ContentID: id, Type: "movie", Title: "Heat", Year: 1995, Genres: []string{"Crime"}, Tagline: "A Los Angeles crime saga",
		RatingSources: []catalogpkg.ItemRatingSourceInfo{
			{Source: models.RatingSourceIMDB, Score: 83, Votes: &imdbVotes},
			{Source: "mdblist", Score: 86},
		},
		Cast:           []catalogpkg.CastCredit{{Name: "Al Pacino", Character: "Vincent Hanna", PersonID: "7"}},
		SeasonUserData: &catalogpkg.SeasonUserData{Played: true, WatchedCount: 1, LastFileID: &fileID},
		UserState:      &catalogpkg.ItemUserState{Played: true, IsFavorite: true},
		Versions:       []catalogpkg.FileVersion{fakeHeatVersion(v)},
		Subtitles:      []catalogpkg.SubtitleInfo{}, OverlaySummary: &models.OverlaySummary{Resolution: "4K"},
		WorkFormats: []catalogpkg.WorkFormatSummary{{Type: "ebook", ContentID: "ebook:heat", LibraryID: 2}}}, nil
}

func (f *fakeCatalog) ItemVersions(_ context.Context, v handlers.ItemViewer, id string) ([]catalogpkg.FileVersion, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastViewer = v
	if id != "movie:heat-1995" {
		return nil, notFoundItem()
	}
	version := fakeHeatVersion(v)
	version.FilePath = ""
	return []catalogpkg.FileVersion{version}, nil
}

func (f *fakeCatalog) MangaFiles(_ context.Context, _ handlers.ItemViewer, id string) (*catalogpkg.MangaSeriesFiles, error) {
	if f.err != nil {
		return nil, f.err
	}
	if id != "series:berserk" {
		return nil, notFoundItem()
	}
	return &catalogpkg.MangaSeriesFiles{FolderPaths: []string{"/media/manga/Berserk"}, Files: []catalogpkg.MangaChapterFile{{ContentID: "manga:berserk-c001", Title: "Chapter 1", FileName: "c001.cbz", FileSize: 42}}}, nil
}

func fakeEpisode(n int) handlers.EpisodeView {
	ep := handlers.EpisodeView{ContentID: "episode:severance-s01e0" + string(rune('0'+n)), SeasonNumber: 1, EpisodeNumber: n, Title: "Good News About Hell", AirDate: "2022-02-18", Runtime: 57,
		Files: []handlers.EpisodeFileView{{FileID: 200 + n, Resolution: "1080p", FileSize: 1}}, UserData: &catalogpkg.SeasonUserData{Played: n == 1, WatchedCount: 1}}
	if n == 1 {
		ep.RatingIMDB, ep.RatingTMDB = new(8.7), new(8.25)
	}
	return ep
}

func (f *fakeCatalog) ItemEpisodes(_ context.Context, _ handlers.ItemViewer, id string) ([]handlers.EpisodeView, error) {
	if f.err != nil {
		return nil, f.err
	}
	if id != "series:severance-S01" {
		return nil, notFoundItem()
	}
	episodes := []handlers.EpisodeView{fakeEpisode(1), fakeEpisode(2)}
	if f.unreadableEpisodes {
		episodes[0].Files[0].Unreadable = true
		episodes[0].Files = append(episodes[0].Files, handlers.EpisodeFileView{FileID: 203, Resolution: "1080p"})
	}
	return episodes, nil
}

func fakeSeason() handlers.SeasonView {
	return handlers.SeasonView{ContentID: "series:severance-S01", PlayContentID: "episode:severance-s01e02", SeasonNumber: 1, Title: "Season 1", AirDate: "2022-02-18", EpisodeCount: 9,
		UserData: &catalogpkg.SeasonUserData{WatchedCount: 1, UnplayedCount: 8}}
}

func (f *fakeCatalog) SeriesSeasons(_ context.Context, _ handlers.ItemViewer, id string, includeArtwork bool) ([]handlers.SeasonView, error) {
	if f.err != nil {
		return nil, f.err
	}
	if id != "series:severance" {
		return nil, notFoundItem()
	}
	season := fakeSeason()
	if includeArtwork {
		season.PosterURL = "https://images.example/season.webp"
		season.PosterThumbhash = "placeholder"
	}
	return []handlers.SeasonView{season}, nil
}

func (f *fakeCatalog) SeriesSeason(_ context.Context, _ handlers.ItemViewer, id string, num int) (handlers.SeasonView, error) {
	if f.err != nil {
		return handlers.SeasonView{}, f.err
	}
	if id != "series:severance" || num != 1 {
		return handlers.SeasonView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Season not found"}
	}
	return fakeSeason(), nil
}

func (f *fakeCatalog) SeasonEpisodes(_ context.Context, _ handlers.ItemViewer, id string, num int) ([]handlers.EpisodeView, error) {
	if f.err != nil {
		return nil, f.err
	}
	if id != "series:severance" || num != 1 {
		return nil, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Season not found"}
	}
	return []handlers.EpisodeView{fakeEpisode(1), fakeEpisode(2)}, nil
}

func catalogDeps(t *testing.T) (Dependencies, *fakeCatalog) {
	t.Helper()
	deps := libraryViewDeps(t)
	deps.CursorSecret = []byte("catalog-cursor-key")
	fake := &fakeCatalog{}
	deps.CatalogAccess, deps.CatalogBrowse, deps.CatalogItems = fake, fake, fake
	return deps, fake
}

type fakeCatalogSettings map[string]string

func (f fakeCatalogSettings) Get(_ context.Context, key string) (string, error) { return f[key], nil }
