package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratingsources"
	"github.com/Silo-Server/silo-server/internal/usercollections"
)

// --- stage B admin fakes on fakeLibraryAdmin ---

func (f *fakeLibraryAdmin) knownLibrary(id int) error {
	if f.err != nil {
		return f.err
	}
	for _, v := range f.libraries {
		if v.ID == id {
			return nil
		}
	}
	return notFoundLibrary()
}

func fakeQueueStatus(id int) handlers.MetadataMatchQueueStatusView {
	return handlers.MetadataMatchQueueStatusView{LibraryID: id, MovieCount: 2, SeriesCount: 1, RawFileCount: 1, TotalCount: 4, PendingCount: 3, ParkedCount: 1}
}

func (f *fakeLibraryAdmin) GetMetadataMatchQueue(_ context.Context, id, limit, offset int) (handlers.MetadataMatchQueueDetailView, error) {
	if err := f.knownLibrary(id); err != nil {
		return handlers.MetadataMatchQueueDetailView{}, err
	}
	f.lastLimit, f.lastOffset = limit, offset
	parked := fixedTime()
	view := handlers.MetadataMatchQueueDetailView{
		Limit: limit, Offset: offset,
		Movies: []handlers.MovieMatchQueueEntryView{{MediaFileID: 120, MediaFolderID: id, FilePath: "/media/movies/Heat (1995)/Heat.mkv", FirstQueuedAt: fixedTime(), AvailableAt: fixedTime(),
			AttemptCount: 1, State: "parked", FailureKind: "no_match", FailureDetail: json.RawMessage(`{"candidates":0}`), MatcherRevision: 3, ParkedAt: &parked, UpdatedAt: fixedTime()}},
		Series:     []handlers.SeriesMatchQueueEntryView{{MediaFolderID: id, ObservedRootPath: "/media/tv/Severance", FirstQueuedAt: fixedTime(), AvailableAt: fixedTime(), State: "pending", UpdatedAt: fixedTime()}},
		RawFiles:   []handlers.RawMatchBacklogEntryView{{MediaFileID: 121, MediaFolderID: id, FilePath: "/media/movies/unknown.mkv", BaseTitle: "unknown", CreatedAt: fixedTime(), UpdatedAt: fixedTime()}},
		MovieTotal: 2, SeriesTotal: 1, RawFileTotal: 1,
	}
	view.LibraryID, view.MovieCount, view.SeriesCount, view.RawFileCount, view.TotalCount, view.PendingCount, view.ParkedCount = id, 2, 1, 1, 4, 3, 1
	if offset > 0 {
		view.Movies, view.Series, view.RawFiles = nil, nil, nil
	}
	return view, nil
}

func (f *fakeLibraryAdmin) RetryMetadataMatchQueue(_ context.Context, id int) (handlers.MetadataMatchQueueActionView, error) {
	if err := f.knownLibrary(id); err != nil {
		return handlers.MetadataMatchQueueActionView{}, err
	}
	return handlers.MetadataMatchQueueActionView{Status: "queued", LibraryID: id, RawFileRetried: 1, Queue: fakeQueueStatus(id)}, nil
}

func (f *fakeLibraryAdmin) CancelMetadataMatchQueue(_ context.Context, id int) (handlers.MetadataMatchQueueActionView, error) {
	if err := f.knownLibrary(id); err != nil {
		return handlers.MetadataMatchQueueActionView{}, err
	}
	return handlers.MetadataMatchQueueActionView{Status: "cancelled", LibraryID: id, MovieCancelled: 2, SeriesCancelled: 1, RawFileCancelled: 1, TotalCancelled: 4, Queue: handlers.MetadataMatchQueueStatusView{LibraryID: id}}, nil //nolint:misspell // v1 wire value
}

func (f *fakeLibraryAdmin) RefreshLibraryMetadata(_ context.Context, id, userID int, mode adminjob.LibraryRefreshMode) (*models.AdminJob, error) {
	if err := f.knownLibrary(id); err != nil {
		return nil, err
	}
	f.lastUserID = userID
	f.lastRefreshMode = mode
	for _, v := range f.libraries {
		if v.ID == id && v.Name == "Busy" {
			return nil, &handlers.APIError{Status: http.StatusConflict, Code: "conflict", Message: "A metadata refresh is already queued or running for this library"}
		}
	}
	return &models.AdminJob{ID: "job-2", JobType: "library_refresh", Status: "queued", CreatedByUserID: userID,
		RequestPayload: json.RawMessage(`{"library_id":1,"mode":"` + string(mode) + `"}`), Message: "Queued " + string(mode) + " library metadata refresh", RequestedAt: fixedTime()}, nil
}

func (f *fakeLibraryAdmin) UploadLibraryPoster(_ context.Context, id int, contentType string, data []byte) (handlers.LibraryView, error) {
	if err := f.knownLibrary(id); err != nil {
		return handlers.LibraryView{}, err
	}
	f.lastPosterType, f.lastPosterSize = contentType, len(data)
	view := libraryFixture(id, "Movies")
	view.PosterURL = "https://s3.example.test/library-posters/1.png"
	return view, nil
}

func (f *fakeLibraryAdmin) DeleteLibraryPoster(_ context.Context, id int) error {
	return f.knownLibrary(id)
}

func (f *fakeLibraryAdmin) LibraryProviders(_ context.Context, id int) (map[string][]handlers.ChainLevelEntryView, error) {
	if err := f.knownLibrary(id); err != nil {
		return nil, err
	}
	if f.providers != nil {
		return f.providers, nil
	}
	return map[string][]handlers.ChainLevelEntryView{
		"movie": {{PluginInstallationID: 3, CapabilityID: "tmdb", ProviderSlug: "tmdb", Priority: 0, Enabled: true}},
	}, nil
}

func (f *fakeLibraryAdmin) SetLibraryProviders(_ context.Context, id int, levels map[string][]handlers.ProviderChainEntryInput) error {
	if err := f.knownLibrary(id); err != nil {
		return err
	}
	f.lastChain = levels
	return nil
}

// --- viewer fakes ---

type fakeLibraryViews struct {
	err error
	// lastViewer is the viewer LibrarySections last saw.
	lastViewer handlers.SectionViewer
}

func fakeCard() handlers.SectionItemView {
	imdb := 8.3
	stamp := "2026-01-02T03:04:05Z"
	pos, dur := 1200.5, 10200.0
	return handlers.SectionItemView{ContentID: "movie:heat-1995", Type: "movie", Title: "Heat", Year: 1995, Runtime: 170, Genres: []string{"Crime"}, Keywords: nil,
		ContentRating: "R", Status: "matched", RatingIMDB: &imdb, PosterURL: "https://s3.example.test/poster.jpg", PositionSeconds: &pos, DurationSeconds: &dur, ProgressUpdatedAt: &stamp,
		OverlaySummary: &models.OverlaySummary{Resolution: "4K", HDR: "Dolby Vision"}, UserState: &handlers.ItemUserStateView{InWatchlist: true}, ItemSource: "in_progress"}
}

func (f *fakeLibraryViews) LibraryLayout(_ context.Context, libraryID int, viewer handlers.SectionViewer) (handlers.SectionLayoutView, error) {
	if f.err != nil {
		return handlers.SectionLayoutView{}, f.err
	}
	f.lastViewer = viewer
	return handlers.SectionLayoutView{Sections: []handlers.SectionLayoutEntryView{{ID: "continue_watching", SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20}, {ID: "recently_added", SectionType: "recently_added", Title: "Recently Added", ItemLimit: 20, Customized: true}}}, nil
}

func (f *fakeLibraryViews) LibrarySections(_ context.Context, libraryID int, viewer handlers.SectionViewer) (handlers.SectionsView, error) {
	if f.err != nil {
		return handlers.SectionsView{}, f.err
	}
	f.lastViewer = viewer
	return handlers.SectionsView{Sections: []handlers.SectionView{{ID: "continue_watching", SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20, TotalCount: 1, Items: []handlers.SectionItemView{fakeCard()}}}}, nil
}

func (f *fakeLibraryViews) LibrarySectionItems(_ context.Context, libraryID int, sectionID string, viewer handlers.SectionViewer) (handlers.SectionView, error) {
	if f.err != nil {
		return handlers.SectionView{}, f.err
	}
	f.lastViewer = viewer
	if sectionID != "continue_watching" {
		return handlers.SectionView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Section not found"}
	}
	return handlers.SectionView{ID: sectionID, SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20, TotalCount: 1, Items: []handlers.SectionItemView{fakeCard()}}, nil
}

func (f *fakeLibraryViews) LibraryCollectionsTab(_ context.Context, libraryID, userID int, profileID string) (handlers.LibraryCollectionTabView, error) {
	if f.err != nil {
		return handlers.LibraryCollectionTabView{}, f.err
	}
	creator := profileID
	return handlers.LibraryCollectionTabView{
		LibraryID: libraryID,
		Collections: []handlers.LibraryCollectionView{{ID: "c1", LibraryID: libraryID, LibraryIDs: []int{libraryID}, Slug: "oscar-winners", Title: "Oscar Winners", CollectionType: "manual", Visibility: "visible",
			QueryDefinition: json.RawMessage(`{"filters":[]}`), ManagementMode: "manual", ItemCount: 12, CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:05Z"}},
		Groups:    []handlers.LibraryCollectionTabGroupView{{ID: "g1", Name: "Mine", Kind: models.GroupKindUserCollections, SortMode: models.GroupSortNameAsc, Collections: []handlers.LibraryCollectionTabEntryView{{ID: "u1", Title: "Rainy days", ItemCount: 4, CreatorProfileID: &creator}}}},
		Ungrouped: &handlers.LibraryCollectionTabUngroupedView{SortOrder: 9999, Collections: []handlers.LibraryCollectionTabEntryView{{ID: "c1", Title: "Oscar Winners", ItemCount: 12, Featured: true}}},
	}, nil
}

func (f *fakeLibraryViews) LibraryUserCollections(_ context.Context, libraryID, userID int, profileID string) ([]usercollections.ServerVisibleCollection, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []usercollections.ServerVisibleCollection{{ID: "u1", CreatorProfileID: profileID, Name: "Rainy days", CollectionType: "manual", ItemCount: 4, CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:05Z"}}, nil
}

func libraryViewDeps(t *testing.T) Dependencies {
	t.Helper()
	deps, _ := libraryDeps(t)
	deps.LibrarySections = &fakeLibraryViews{}
	deps.LibraryCollections = &fakeLibraryViews{}
	return deps
}

func viewerHeaders() map[string]string { return with(bearer(memberToken), "X-Profile-Id", "p-owner") }

// --- stage B admin tests ---

// --- viewer tests ---

// A game of a date-labeled series (Sportarr's S2026E296) carries the label and
// its air date on the card; a numbered episode carries neither, which clients
// read as "show the numbers".
func TestSectionCardEpisodeLabels(t *testing.T) {
	season, episode := 2026, 296
	game := handlers.SectionItemView{ContentID: "ep-1", Type: "episode", Title: "Chicago Bulls vs Washington Wizards",
		SeriesTitle: "NBA", SeasonNumber: &season, EpisodeNumber: &episode, Status: "matched",
		EpisodeLabels: "air_date", AirDate: "2026-11-20"}
	numbered := game
	numbered.EpisodeLabels, numbered.AirDate = "", ""

	for _, tt := range []struct {
		name string
		card handlers.SectionItemView
		want map[string]any
	}{
		{"date-labeled", game, map[string]any{"episode_labels": "air_date", "air_date": "2026-11-20"}},
		{"numbered", numbered, map[string]any{"episode_labels": nil, "air_date": nil}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(catalogItemOfSection(tt.card, ratingsources.Selection{}))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Errorf("%s = %v, want %v", key, got[key], want)
				}
			}
		})
	}
}
