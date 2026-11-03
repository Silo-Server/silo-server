package jellycompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// A play negotiated without a MediaSourceId (PlaybackInfo without one, or
// Static=true direct play) lists every version of the title. These tests play
// one of two cuts: theatrical (file 42, 3600 s) and extended (file 43, 5400 s).
// A report at 3300 s is past the default 90% threshold for the theatrical cut
// and about 61% of the extended one.
const versionReportPosition = 3300

func versionedReportVersions() (theatrical, extended catalog.FileVersion) {
	theatrical = testCompatVersion()
	extended = testCompatVersion()
	extended.FileID, extended.Duration = 43, 5400
	return theatrical, extended
}

// newVersionedReportHandler lists the given versions on play-1 and records
// upstreamFileID as the file its upstream session streams.
func newVersionedReportHandler(t *testing.T, registerUpstream bool, listed []catalog.FileVersion, upstreamFileID int) (*PlaybackHandler, *testCompatSessionManager, userstore.UserStore) {
	t.Helper()
	handler, mgr, _, _ := newReportLivenessHandler("upstream-1", registerUpstream)
	store := newJellycompatUserStore(t)
	handler.storeProvider = compatTestUserStoreProvider{store: store}
	sources := make([]PlaybackMediaSource, 0, len(listed))
	for _, version := range listed {
		sources = append(sources, testCompatSource(handler.codec, version))
	}
	if err := handler.playbackStore.Update("play-1", func(play *PlaybackSession) error {
		play.MediaSources = sources
		play.UpstreamMediaFileID = upstreamFileID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return handler, mgr, store
}

func versionReportBody(mediaSourceID string) string {
	ticks := int64(versionReportPosition) * 10_000_000
	if mediaSourceID == "" {
		return fmt.Sprintf(`{"PlaySessionId":"play-1","PositionTicks":%d}`, ticks)
	}
	return fmt.Sprintf(`{"PlaySessionId":"play-1","MediaSourceId":%q,"PositionTicks":%d}`, mediaSourceID, ticks)
}

func requireStoredProgress(t *testing.T, store userstore.UserStore, wantWatched bool, wantDuration float64) {
	t.Helper()
	progress, err := store.GetProgress(context.Background(), "profile-1", "movie-1")
	if err != nil || progress == nil {
		t.Fatalf("progress = %+v, %v", progress, err)
	}
	if progress.Completed != wantWatched || progress.DurationSeconds != wantDuration {
		t.Fatalf("completed = %v with duration %v, want %v with %v", progress.Completed, progress.DurationSeconds, wantWatched, wantDuration)
	}
}

// Completion follows the version being played, whether the report names it or
// only the upstream session's file does, not the first one listed.
func TestPlaybackReportCompletionUsesPlayedVersionRuntime(t *testing.T) {
	theatrical, extended := versionedReportVersions()
	for _, tc := range []struct {
		name        string
		listed      []catalog.FileVersion
		playing     catalog.FileVersion
		wantWatched bool
	}{
		{"extended cut listed second", []catalog.FileVersion{theatrical, extended}, extended, false},
		{"theatrical cut listed second", []catalog.FileVersion{extended, theatrical}, theatrical, true},
	} {
		t.Run(tc.name+"/report names the version", func(t *testing.T) {
			// A play stored before UpstreamMediaFileID existed has none, so only
			// the report's MediaSourceId identifies the version.
			handler, _, store := newVersionedReportHandler(t, true, tc.listed, 0)
			playingID := testCompatSource(handler.codec, tc.playing).ID
			if rec := postProgressReport(handler, versionReportBody(playingID)); rec.Code != http.StatusNoContent {
				t.Fatalf("report: status %d, body %s", rec.Code, rec.Body.String())
			}
			requireStoredProgress(t, store, tc.wantWatched, float64(tc.playing.Duration))
		})
		t.Run(tc.name+"/upstream session names the version", func(t *testing.T) {
			handler, _, store := newVersionedReportHandler(t, true, tc.listed, tc.playing.FileID)
			if rec := postProgressReport(handler, versionReportBody("")); rec.Code != http.StatusNoContent {
				t.Fatalf("report: status %d, body %s", rec.Code, rec.Body.String())
			}
			requireStoredProgress(t, store, tc.wantWatched, float64(tc.playing.Duration))
		})
	}
}

// A report that revives a reaped upstream session keeps the play on the version
// it was streaming instead of restarting it on the first one listed.
func TestPlaybackReportReviveKeepsPlayedVersion(t *testing.T) {
	theatrical, extended := versionedReportVersions()
	handler, mgr, store := newVersionedReportHandler(t, false, []catalog.FileVersion{theatrical, extended}, extended.FileID)
	if rec := postProgressReport(handler, versionReportBody("")); rec.Code != http.StatusNoContent {
		t.Fatalf("report: status %d, body %s", rec.Code, rec.Body.String())
	}
	revived := mgr.sessions["upstream-started"]
	if mgr.startCalls != 1 || revived == nil || revived.MediaFileID != extended.FileID {
		t.Fatalf("revive started %d sessions, upstream %+v; want one on file %d", mgr.startCalls, revived, extended.FileID)
	}
	if play, ok := handler.playbackStore.Get("play-1"); !ok || play.UpstreamMediaFileID != extended.FileID {
		t.Fatalf("play after revive = %+v", play)
	}
	requireStoredProgress(t, store, false, float64(extended.Duration))
}

// The watch-provider stop sent after the upstream session was reaped judges
// completion against the version that was playing.
func TestStoppedScrobbleAfterReapUsesPlayedVersion(t *testing.T) {
	theatrical, extended := versionedReportVersions()
	handler, _, _ := newVersionedReportHandler(t, false, []catalog.FileVersion{theatrical, extended}, extended.FileID)
	scrobbler := &recordingCompatWatchScrobbler{}
	handler.WatchScrobbler = scrobbler

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(versionReportBody("")))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingStopped(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(scrobbler.calls) != 1 || scrobbler.calls[0].action != "stop" {
		t.Fatalf("scrobble calls = %+v, want one stop", scrobbler.calls)
	}
	if event := scrobbler.calls[0].event; event.DurationSeconds != float64(extended.Duration) || event.Completed {
		t.Fatalf("stop event = %+v, want duration %d and not completed", event, extended.Duration)
	}
}

func TestPlayedSourceDurationFallbacks(t *testing.T) {
	codec := NewResourceIDCodec()
	theatrical, extended := versionedReportVersions()
	unknownRuntime := extended
	unknownRuntime.Duration = 0
	sources := func(versions ...catalog.FileVersion) []PlaybackMediaSource {
		out := make([]PlaybackMediaSource, 0, len(versions))
		for _, version := range versions {
			out = append(out, testCompatSource(codec, version))
		}
		return out
	}
	for _, tc := range []struct {
		name          string
		play          PlaybackSession
		mediaSourceID string
		want          float64
	}{
		{"unknown MediaSourceId uses the upstream file", PlaybackSession{MediaSources: sources(theatrical, extended), UpstreamMediaFileID: 43}, "not-a-source", 5400},
		{"nothing identifies the version", PlaybackSession{MediaSources: sources(theatrical, extended)}, "", 3600},
		{"played version without a runtime", PlaybackSession{MediaSources: sources(theatrical, unknownRuntime), UpstreamMediaFileID: 43}, "", 0},
	} {
		if got := playedSourceDuration(&tc.play, tc.mediaSourceID); got != tc.want {
			t.Errorf("%s: duration = %v, want %v", tc.name, got, tc.want)
		}
	}
}
