package catalog

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
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
