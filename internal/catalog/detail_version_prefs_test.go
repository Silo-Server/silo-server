package catalog

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type testDetailUserStoreProvider struct {
	store userstore.UserStore
}

func (p testDetailUserStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p testDetailUserStoreProvider) Close() error { return nil }

func newDetailTestStore(t *testing.T) userstore.UserStore {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Main"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	return store
}

func TestEffectiveVersionDefaults_UsesSeriesPlaybackPreference(t *testing.T) {
	store := newDetailTestStore(t)
	if err := store.SetSeriesPlaybackPreference(context.Background(), userstore.SeriesPlaybackPreference{
		ProfileID:  "profile-1",
		SeriesID:   "series-1",
		Resolution: "1080p",
		HDR:        false,
		CodecVideo: "h264",
		UpdatedAt:  "2026-04-07T00:00:00Z",
	}); err != nil {
		t.Fatalf("SetSeriesPlaybackPreference: %v", err)
	}

	service := &DetailService{}
	service.SetUserStoreProvider(testDetailUserStoreProvider{store: store})

	defaults := service.effectiveVersionDefaults(context.Background(), AccessFilter{
		UserID:    1,
		ProfileID: "profile-1",
	}, "series-1")

	if !defaults.HasAny {
		t.Fatal("expected version defaults to be present")
	}
	if defaults.Resolution != "1080p" {
		t.Fatalf("Resolution = %q, want 1080p", defaults.Resolution)
	}
	if defaults.HDR {
		t.Fatalf("HDR = %v, want false", defaults.HDR)
	}
	if defaults.CodecVideo != "h264" {
		t.Fatalf("CodecVideo = %q, want h264", defaults.CodecVideo)
	}
}

func TestEffectiveVersionDefaults_RequiresSeriesContext(t *testing.T) {
	service := &DetailService{}

	defaults := service.effectiveVersionDefaults(context.Background(), AccessFilter{
		UserID:    1,
		ProfileID: "profile-1",
	}, "")

	if defaults.HasAny {
		t.Fatal("expected no defaults without a series id")
	}
}

func TestEffectiveSubtitleDefaults_UsesSeriesTrackSignature(t *testing.T) {
	store := newDetailTestStore(t)
	if err := store.SetSubtitlePreference(context.Background(), userstore.SubtitlePreference{
		ProfileID:        "profile-1",
		SeriesID:         "series-1",
		SubtitleLanguage: "en",
		SubtitleMode:     "always",
		TrackSignature: &userstore.SubtitleTrackSignature{
			Source:          "external",
			Language:        "en",
			Codec:           "srt",
			Label:           "English SDH",
			HearingImpaired: true,
		},
		UpdatedAt: "2026-04-07T00:00:00Z",
	}); err != nil {
		t.Fatalf("SetSubtitlePreference: %v", err)
	}

	service := &DetailService{}
	service.SetUserStoreProvider(testDetailUserStoreProvider{store: store})

	defaults := service.effectiveSubtitleDefaults(context.Background(), AccessFilter{
		UserID:    1,
		ProfileID: "profile-1",
	}, "series-1", nil)

	if defaults.TrackSignature == nil {
		t.Fatal("expected subtitle track signature to be present")
	}
	if defaults.TrackSignature.Label != "English SDH" {
		t.Fatalf("TrackSignature.Label = %q, want English SDH", defaults.TrackSignature.Label)
	}
	if !defaults.TrackSignature.HearingImpaired {
		t.Fatal("expected hearing impaired signature flag to be preserved")
	}
}

func TestSeriesFolderPathsFromFiles_PrefersObservedRootsAndDedupes(t *testing.T) {
	paths := seriesFolderPathsFromFiles([]*models.MediaFile{
		{
			ObservedRootPath: "/media/shows/Example Show",
			FilePath:         "/media/shows/Example Show/Season 01/E01.mkv",
		},
		{
			ObservedRootPath: "/media/shows/Example Show",
			FilePath:         "/media/shows/Example Show/Season 01/E02.mkv",
		},
		{
			CanonicalRootPath: "/media/shows/Fallback Canonical Root",
			FilePath:          "/media/shows/Fallback Canonical Root/E01.mkv",
		},
		{
			FilePath: "/media/shows/Flat Layout/E01.mkv",
		},
	})

	want := []string{
		"/media/shows/Example Show",
		"/media/shows/Fallback Canonical Root",
		"/media/shows/Flat Layout",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestEffectiveAudioTrackIndex_PrefersSeriesAudioPreferenceOverLibraryAndProfile(t *testing.T) {
	store := newDetailTestStore(t)
	language := "en"
	if err := store.UpdateProfile(context.Background(), "profile-1", userstore.UpdateProfileInput{
		Language: &language,
	}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if err := store.UpsertLibraryPlaybackPreference(context.Background(), userstore.LibraryPlaybackPreference{
		ProfileID:     "profile-1",
		LibraryID:     12,
		AudioLanguage: "es",
	}); err != nil {
		t.Fatalf("UpsertLibraryPlaybackPreference: %v", err)
	}
	if err := store.SetAudioPreference(context.Background(), userstore.AudioPreference{
		ProfileID:     "profile-1",
		SeriesID:      "series-1",
		AudioLanguage: "fr",
	}); err != nil {
		t.Fatalf("SetAudioPreference: %v", err)
	}

	service := &DetailService{}
	service.SetUserStoreProvider(testDetailUserStoreProvider{store: store})

	index := service.effectiveAudioTrackIndex(context.Background(), AccessFilter{
		UserID:    1,
		ProfileID: "profile-1",
	}, "series-1", &models.MediaFile{
		MediaFolderID: 12,
		AudioTracks: []models.AudioTrack{
			{Language: "en", Default: true},
			{Language: "fr"},
			{Language: "es"},
		},
	})

	if index != 1 {
		t.Fatalf("effectiveAudioTrackIndex() = %d, want 1", index)
	}
}

func TestBuildPlaybackInfo_SetsEffectiveAudioLanguageFromOriginalWhenTrackLanguageMissing(t *testing.T) {
	store := newDetailTestStore(t)
	language := playback.OriginalLanguageSentinel
	if err := store.UpdateProfile(context.Background(), "profile-1", userstore.UpdateProfileInput{
		Language: &language,
	}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}

	service := &DetailService{
		originalLangFn: func(context.Context, string) string {
			return "ja"
		},
	}
	service.SetUserStoreProvider(testDetailUserStoreProvider{store: store})

	versions, _, _, _, _, _, _ := service.buildPlaybackInfo(context.Background(), []*models.MediaFile{
		{
			ID:            7,
			ContentID:     "movie-1",
			FilePath:      "/media/movie.mkv",
			MediaFolderID: 12,
			AudioTracks: []models.AudioTrack{
				{Language: "", Default: true},
				{Language: "en"},
			},
		},
	}, AccessFilter{
		UserID:    1,
		ProfileID: "profile-1",
	}, "movie-1")

	if len(versions) != 1 {
		t.Fatalf("len(versions) = %d, want 1", len(versions))
	}
	if versions[0].EffectiveAudioLanguage != "ja" {
		t.Fatalf("EffectiveAudioLanguage = %q, want ja", versions[0].EffectiveAudioLanguage)
	}
}

func TestBuildPlaybackInfo_GroupsMultipartVariants(t *testing.T) {
	service := &DetailService{}

	_, variants, _, _, _, _, _ := service.buildPlaybackInfo(context.Background(), []*models.MediaFile{
		{
			ID:                    11,
			ContentID:             "movie-1",
			FilePath:              "/media/Movie CD1 1080p.mkv",
			Resolution:            "1080p",
			FileSize:              2_000,
			Duration:              3600,
			EditionKey:            "final_cut",
			EditionRaw:            "Final Cut",
			PresentationKind:      "multipart_movie",
			PresentationGroupKey:  "Movie",
			PresentationPartIndex: 1,
		},
		{
			ID:                    12,
			ContentID:             "movie-1",
			FilePath:              "/media/Movie CD2 1080p.mkv",
			Resolution:            "1080p",
			FileSize:              2_100,
			Duration:              3500,
			EditionKey:            "final_cut",
			EditionRaw:            "Final Cut",
			PresentationKind:      "multipart_movie",
			PresentationGroupKey:  "Movie",
			PresentationPartIndex: 2,
		},
	}, AccessFilter{}, "movie-1")

	if len(variants) != 1 {
		t.Fatalf("len(variants) = %d, want 1", len(variants))
	}
	if variants[0].PartCount != 2 {
		t.Fatalf("PartCount = %d, want 2", variants[0].PartCount)
	}
	if variants[0].EditionKey != "final_cut" {
		t.Fatalf("EditionKey = %q, want final_cut", variants[0].EditionKey)
	}
	if len(variants[0].Parts) != 2 || variants[0].Parts[0].PartIndex != 1 || variants[0].Parts[1].PartIndex != 2 {
		t.Fatalf("parts = %#v, want ordered multipart parts", variants[0].Parts)
	}
}

func TestBuildPlaybackInfo_SelectedFileWithoutIntroDoesNotInheritAnotherVersionIntro(t *testing.T) {
	service := &DetailService{}
	introStart := 12.5
	introEnd := 42.75

	versions, _, _, intro, _, _, _ := service.buildPlaybackInfo(context.Background(), []*models.MediaFile{
		{
			ID:         11,
			ContentID:  "movie-1",
			FilePath:   "/media/Movie 1080p.mkv",
			Resolution: "1080p",
			IntroStart: &introStart,
			IntroEnd:   &introEnd,
		},
		{
			ID:         12,
			ContentID:  "movie-1",
			FilePath:   "/media/Movie 720p.mkv",
			Resolution: "720p",
		},
	}, AccessFilter{SelectedFileID: 12}, "movie-1")

	if intro != nil {
		t.Fatalf("intro = %#v, want nil for selected file without intro", intro)
	}
	if len(versions) != 2 {
		t.Fatalf("len(versions) = %d, want 2", len(versions))
	}
	if versions[1].FileID != 12 {
		t.Fatalf("versions[1].FileID = %d, want 12", versions[1].FileID)
	}
	if versions[1].Intro != nil {
		t.Fatalf("selected version intro = %#v, want nil", versions[1].Intro)
	}
}

func TestBuildPlaybackInfo_NoSelectedFileKeepsAvailableIntroFallback(t *testing.T) {
	service := &DetailService{}
	introStart := 12.5
	introEnd := 42.75

	_, _, _, intro, _, _, _ := service.buildPlaybackInfo(context.Background(), []*models.MediaFile{
		{
			ID:         11,
			ContentID:  "movie-1",
			FilePath:   "/media/Movie 1080p.mkv",
			Resolution: "1080p",
			IntroStart: &introStart,
			IntroEnd:   &introEnd,
		},
		{
			ID:         12,
			ContentID:  "movie-1",
			FilePath:   "/media/Movie 720p.mkv",
			Resolution: "720p",
		},
	}, AccessFilter{}, "movie-1")

	if intro == nil {
		t.Fatal("intro = nil, want available intro when no file is selected")
	}
	if intro.Start != introStart || intro.End != introEnd {
		t.Fatalf("intro = %#v, want start %v end %v", intro, introStart, introEnd)
	}
}

func TestSelectedPlaybackMarkerSkipsNilVariantDefaultMarkers(t *testing.T) {
	want := &Marker{Start: 30, End: 60}
	got := selectedPlaybackMarker(
		[]FileVersion{
			{FileID: 1},
			{FileID: 2, Intro: want},
			{FileID: 3, Intro: &Marker{Start: 5, End: 15}},
		},
		[]PlaybackVariant{
			{VariantID: "default", DefaultFileID: 1},
			{VariantID: "alternate", DefaultFileID: 2},
		},
		0,
		func(version FileVersion) *Marker {
			return version.Intro
		},
	)
	if got != want {
		t.Fatalf("selected marker = %#v, want later variant default %#v", got, want)
	}
}

func TestBuildPlaybackInfo_StoresCreditsOnFileVersion(t *testing.T) {
	service := &DetailService{}
	creditsStart := 1400.0
	creditsEnd := 1500.0

	versions, _, _, _, credits, _, _ := service.buildPlaybackInfo(context.Background(), []*models.MediaFile{
		{
			ID:           11,
			ContentID:    "movie-1",
			FilePath:     "/media/Movie 1080p.mkv",
			Resolution:   "1080p",
			CreditsStart: &creditsStart,
			CreditsEnd:   &creditsEnd,
		},
	}, AccessFilter{}, "movie-1")

	if len(versions) != 1 {
		t.Fatalf("len(versions) = %d, want 1", len(versions))
	}
	if versions[0].Credits == nil {
		t.Fatal("version credits = nil, want marker")
	}
	if versions[0].Credits.Start != creditsStart || versions[0].Credits.End != creditsEnd {
		t.Fatalf("version credits = %#v, want start %v end %v", versions[0].Credits, creditsStart, creditsEnd)
	}
	if credits == nil || credits.Start != creditsStart || credits.End != creditsEnd {
		t.Fatalf("top-level credits = %#v, want start %v end %v", credits, creditsStart, creditsEnd)
	}
}

func TestSortFileVersions_PrefersLargerFilesWithinQualityTier(t *testing.T) {
	versions := []FileVersion{
		{FileID: 1, Resolution: "1080p", FileSize: 1_000},
		{FileID: 2, Resolution: "1080p", FileSize: 2_000},
	}

	sortFileVersions(versions)

	if versions[0].FileID != 2 {
		t.Fatalf("versions[0].FileID = %d, want 2", versions[0].FileID)
	}
	if got := chooseDefaultVariantVersion(versions, 0); got == nil || got.FileID != 2 {
		t.Fatalf("default version = %#v, want file 2", got)
	}
}

func TestEffectiveAudioTrackIndex_ResolvesProfileOriginalWhenSeriesPreferenceFallsBack(t *testing.T) {
	store := newDetailTestStore(t)
	language := playback.OriginalLanguageSentinel
	if err := store.UpdateProfile(context.Background(), "profile-1", userstore.UpdateProfileInput{
		Language: &language,
	}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if err := store.SetAudioPreference(context.Background(), userstore.AudioPreference{
		ProfileID:       "profile-1",
		SeriesID:        "series-1",
		AudioTrackIndex: 1,
		AudioLanguage:   "fr",
	}); err != nil {
		t.Fatalf("SetAudioPreference: %v", err)
	}

	service := &DetailService{
		originalLangFn: func(context.Context, string) string {
			return "ja"
		},
	}
	service.SetUserStoreProvider(testDetailUserStoreProvider{store: store})

	index := service.effectiveAudioTrackIndex(context.Background(), AccessFilter{
		UserID:    1,
		ProfileID: "profile-1",
	}, "series-1", &models.MediaFile{
		MediaFolderID: 12,
		AudioTracks: []models.AudioTrack{
			{Language: "en", Default: true},
			{Language: "ja"},
		},
	})

	if index != 1 {
		t.Fatalf("effectiveAudioTrackIndex() = %d, want 1", index)
	}
}

func TestEffectiveAudioTrackIndex_KeepsProfileFallbackWhenLibraryOriginalIsUnresolved(t *testing.T) {
	store := newDetailTestStore(t)
	language := "en"
	if err := store.UpdateProfile(context.Background(), "profile-1", userstore.UpdateProfileInput{
		Language: &language,
	}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if err := store.UpsertLibraryPlaybackPreference(context.Background(), userstore.LibraryPlaybackPreference{
		ProfileID:     "profile-1",
		LibraryID:     12,
		AudioLanguage: playback.OriginalLanguageSentinel,
	}); err != nil {
		t.Fatalf("UpsertLibraryPlaybackPreference: %v", err)
	}

	service := &DetailService{
		originalLangFn: func(context.Context, string) string {
			return ""
		},
	}
	service.SetUserStoreProvider(testDetailUserStoreProvider{store: store})

	index := service.effectiveAudioTrackIndex(context.Background(), AccessFilter{
		UserID:    1,
		ProfileID: "profile-1",
	}, "series-1", &models.MediaFile{
		MediaFolderID: 12,
		AudioTracks: []models.AudioTrack{
			{Language: "fr", Default: true},
			{Language: "en"},
		},
	})

	if index != 1 {
		t.Fatalf("effectiveAudioTrackIndex() = %d, want 1", index)
	}
}
