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

func TestPlayedSourceVersionFallbacks(t *testing.T) {
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
		version, _ := playedSourceVersion(&tc.play, tc.mediaSourceID)
		if got := float64(version.Duration); got != tc.want {
			t.Errorf("%s: duration = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A progress report inside the played version's end credits finishes the
// title below the % threshold. Another cut's credits don't count, nor do the
// credits of a version the play doesn't identify: the first listed one is
// only a guess at what plays.
func TestProgressReportFinishesAtPlayedVersionCredits(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		theatrical, extended *catalog.Marker
		upstreamFileID       int
		position             int64
		wantCompleted        bool
	}{
		// 4600 s is past the extended cut's credits but only 85% of it.
		{name: "played cut's credits", extended: &catalog.Marker{Start: 4550, End: 5390}, upstreamFileID: 43, position: 4600, wantCompleted: true},
		{name: "other cut's credits", theatrical: &catalog.Marker{Start: 3000, End: 3590}, upstreamFileID: 43, position: 4600},
		// 3100 s is past the theatrical cut's credits but only 86% of it.
		{name: "unidentified version's credits", theatrical: &catalog.Marker{Start: 3000, End: 3590}, position: 3100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			theatrical, extended := versionedReportVersions()
			theatrical.Credits, extended.Credits = tc.theatrical, tc.extended
			handler, _, store := newVersionedReportHandler(t, true, []catalog.FileVersion{theatrical, extended}, tc.upstreamFileID)

			body := fmt.Sprintf(`{"PlaySessionId":"play-1","PositionTicks":%d}`, tc.position*10_000_000)
			req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", strings.NewReader(body))
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
				&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
			rec := httptest.NewRecorder()
			handler.HandleSessionPlayingProgress(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			progress, err := store.GetProgress(context.Background(), "profile-1", "movie-1")
			if err != nil {
				t.Fatal(err)
			}
			if progress == nil || progress.Completed != tc.wantCompleted {
				t.Fatalf("progress = %+v, want completed %v", progress, tc.wantCompleted)
			}
		})
	}
}
