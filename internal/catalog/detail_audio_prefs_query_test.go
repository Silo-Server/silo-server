package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// countingUserStore wraps a real UserStore and counts the per-file preference
// lookups that effectiveAudioSelection performs, so a test can assert they are
// resolved once per detail request rather than once per media file.
type countingUserStore struct {
	userstore.UserStore
	getProfile     int32
	getAudioPref   int32
	getLibraryPref int32
	resolveValues  int32
}

func (c *countingUserStore) GetProfile(ctx context.Context, id string) (*userstore.Profile, error) {
	atomic.AddInt32(&c.getProfile, 1)
	return c.UserStore.GetProfile(ctx, id)
}

func (c *countingUserStore) ListSettingValuesForResolution(
	ctx context.Context, query userstore.SettingResolutionQuery,
) ([]userstore.SettingValue, error) {
	atomic.AddInt32(&c.resolveValues, 1)
	return c.UserStore.ListSettingValuesForResolution(ctx, query)
}

func (c *countingUserStore) GetAudioPreference(ctx context.Context, profileID, seriesID string) (*userstore.AudioPreference, error) {
	atomic.AddInt32(&c.getAudioPref, 1)
	return c.UserStore.GetAudioPreference(ctx, profileID, seriesID)
}

func (c *countingUserStore) GetLibraryPlaybackPreference(ctx context.Context, profileID string, libraryID int) (*userstore.LibraryPlaybackPreference, error) {
	atomic.AddInt32(&c.getLibraryPref, 1)
	return c.UserStore.GetLibraryPlaybackPreference(ctx, profileID, libraryID)
}

// A multi-file audiobook must not re-issue the file-invariant preference
// queries once per track. Before the request-scoped memo this scaled O(files),
// which is why a many-track audiobook detail page was slow.
func TestBuildPlaybackInfo_AudioPrefLookupsDoNotScaleWithFileCount(t *testing.T) {
	counting := &countingUserStore{UserStore: newDetailTestStore(t)}

	service := &DetailService{}
	service.SetUserStoreProvider(testDetailUserStoreProvider{store: counting})

	const fileCount = 8
	files := make([]*models.MediaFile, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		files = append(files, &models.MediaFile{
			ID:            i + 1,
			MediaFolderID: 42,
			AudioTracks:   []models.AudioTrack{{Language: "eng", Default: true}},
		})
	}

	filter := AccessFilter{UserID: 1, ProfileID: "profile-1"}
	service.buildPlaybackInfo(context.Background(), files, filter, "book-1")

	// The audio language now resolves through the settings contract, which
	// memoizes per profile and per library exactly as the profile-column
	// lookups it replaced did. Two reads: one with no content context for the
	// profile-level answer, one naming the folder's library.
	if got := atomic.LoadInt32(&counting.resolveValues); got > 2 {
		t.Fatalf("resolved settings %d times for %d files; want at most 2 "+
			"(lookup must not scale with file count)", got, fileCount)
	}
	if got := atomic.LoadInt32(&counting.getAudioPref); got != 1 {
		t.Fatalf("GetAudioPreference called %d times for %d files; want 1", got, fileCount)
	}
}

// A movie's detail resolves the audio language of every library its files are
// in and its subtitle settings from one settings read. Each context must rank
// as it does alone: library, series and device rows for other contexts share
// the candidate set.
func TestPrefetchMoviePlaybackDefaultsMatchesOneReadPerContext(t *testing.T) {
	store := newDetailTestStore(t)
	const movie = "movie-1"
	setProfileAudioLanguage(t, store, "en")
	setScopedAudioLanguageForDevice(t, store, settingscontract.ScopeProfileDevice, "", 0, "apple-tv", "ja")
	setScopedAudioLanguage(t, store, settingscontract.ScopeProfileLibrary, "", 12, "es")
	setScopedAudioLanguage(t, store, settingscontract.ScopeProfileSeries, "movie-2", 0, "fr")
	seed := func(key string, identity userstore.SettingIdentity, value any) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		identity.Key, identity.ProfileID = key, "profile-1"
		if _, err := store.UpsertSettingValue(context.Background(), identity, encoded); err != nil {
			t.Fatalf("seeding %s: %v", key, err)
		}
	}
	seed(settingskeys.PlaybackSubtitleMode, userstore.SettingIdentity{Scope: settingscontract.ScopeProfile}, "auto")
	seed(settingskeys.PlaybackSubtitleMode, userstore.SettingIdentity{Scope: settingscontract.ScopeProfileLibrary, LibraryID: 13}, "always")
	seed(settingskeys.PlaybackSubtitleLanguage, userstore.SettingIdentity{Scope: settingscontract.ScopeProfileLibrary, LibraryID: 12}, "de")
	seed(settingskeys.PlaybackSubtitleLanguage, userstore.SettingIdentity{Scope: settingscontract.ScopeProfileSeries, SeriesID: movie}, "nl")
	seed(settingskeys.PlaybackShowForcedSubtitles, userstore.SettingIdentity{Scope: settingscontract.ScopeProfileDevice, DeviceID: "apple-tv"}, false)
	if err := store.SetAudioPreference(context.Background(), userstore.AudioPreference{
		ProfileID: "profile-1", SeriesID: movie, AudioTrackIndex: 2,
		// Matches no track, so each library's language decides.
		TrackSignature: &userstore.AudioTrackSignature{Language: "ko", Codec: "ac3", Channels: 6},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSubtitlePreference(context.Background(), userstore.SubtitlePreference{
		ProfileID: "profile-1", SeriesID: movie,
		TrackSignature: &userstore.SubtitleTrackSignature{Source: "embedded", Language: "nl", Codec: "subrip"},
	}); err != nil {
		t.Fatal(err)
	}

	tracks := []models.AudioTrack{
		{Language: "en", Codec: "aac", Default: true}, {Language: "ja", Codec: "aac"},
		{Language: "es", Codec: "ac3", Channels: 6}, {Language: "fr", Codec: "aac"},
	}
	files := []*models.MediaFile{
		{ID: 1, MediaFolderID: 12, Resolution: "1080p", AudioTracks: tracks},
		{ID: 2, MediaFolderID: 13, Resolution: "2160p", AudioTracks: tracks},
		{ID: 3, MediaFolderID: 99, Resolution: "720p", AudioTracks: tracks},
	}
	item := &models.MediaItem{ContentID: movie, Type: playableTypeMovie}
	for _, filter := range []AccessFilter{
		{UserID: 1, ProfileID: "profile-1"},
		{UserID: 1, ProfileID: "profile-1", DeviceID: "apple-tv"},
		{UserID: 1, ProfileID: "profile-1", DeviceID: "apple-tv", SelectedFileID: 1},
	} {
		counting := &countingUserStore{UserStore: store}
		service := &DetailService{}
		service.SetUserStoreProvider(testDetailUserStoreProvider{store: counting})

		lazy := service.newAudioPrefResolver(context.Background(), filter, movie)
		wantVersions, _, _, _, _, _, _ := service.buildPlaybackInfoWith(context.Background(), files, filter, lazy, nil, false)
		wantSubtitles := service.effectiveSubtitleDefaults(context.Background(), filter, movie, files)
		lazyReads := atomic.LoadInt32(&counting.resolveValues)

		atomic.StoreInt32(&counting.resolveValues, 0)
		prefetched := service.newAudioPrefResolver(context.Background(), filter, movie)
		gotSubtitles, ok := service.prefetchMoviePlaybackDefaults(context.Background(), filter, item, files, prefetched)
		if !ok {
			t.Fatal("prefetchMoviePlaybackDefaults reported failure")
		}
		gotVersions, _, _, _, _, _, _ := service.buildPlaybackInfoWith(context.Background(), files, filter, prefetched, nil, false)
		if !reflect.DeepEqual(gotVersions, wantVersions) || !reflect.DeepEqual(gotSubtitles, wantSubtitles) {
			t.Errorf("device %q file %d:\n got %+v %+v\nwant %+v %+v", filter.DeviceID, filter.SelectedFileID,
				audioSelections(gotVersions), gotSubtitles, audioSelections(wantVersions), wantSubtitles)
		}
		if got := atomic.LoadInt32(&counting.resolveValues); got != 1 || lazyReads != 4 {
			t.Errorf("settings reads: prefetched %d, one per context %d; want 1 and 4", got, lazyReads)
		}
	}
}

func audioSelections(versions []FileVersion) []string {
	out := make([]string, 0, len(versions))
	for _, v := range versions {
		out = append(out, fmt.Sprintf("%d:%d/%s", v.FileID, *v.EffectiveAudioTrackIndex, v.EffectiveAudioLanguage))
	}
	return out
}
