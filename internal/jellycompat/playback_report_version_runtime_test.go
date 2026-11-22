package jellycompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
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

func TestReportedSourceFallbacks(t *testing.T) {
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
		got := 0.0
		if source, _ := reportedSource(&tc.play, tc.mediaSourceID); source != nil {
			got = float64(source.Version.Duration)
		}
		if got != tc.want {
			t.Errorf("%s: duration = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A progress report inside the played version's end credits finishes the
// title below the % threshold. Another cut's credits don't count, nor do the
// credits of a version the play doesn't identify: the first listed one is
// only a guess at what plays. The credits are the ones PlaybackInfo stored on
// each source.
func TestProgressReportFinishesAtPlayedVersionCredits(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		theatricalEnd, extendedEnd float64
		upstreamFileID             int
		position                   int64
		wantCompleted              bool
	}{
		// 4600 s is past the extended cut's credits but only 85% of it.
		{name: "played cut's credits", extendedEnd: 4550, upstreamFileID: 43, position: 4600, wantCompleted: true},
		{name: "other cut's credits", theatricalEnd: 3000, upstreamFileID: 43, position: 4600},
		// 3100 s is past the theatrical cut's credits but only 86% of it.
		{name: "unidentified version's credits", theatricalEnd: 3000, position: 3100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			theatrical, extended := versionedReportVersions()
			handler, _, store := newVersionedReportHandler(t, true, []catalog.FileVersion{theatrical, extended}, tc.upstreamFileID)
			if err := handler.playbackStore.Update("play-1", func(play *PlaybackSession) error {
				play.MediaSources[0].EndCreditsStart = tc.theatricalEnd
				play.MediaSources[1].EndCreditsStart = tc.extendedEnd
				return nil
			}); err != nil {
				t.Fatal(err)
			}

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

// PlaybackInfo stores where each source's end credits start, using the item's
// own end-credits window and the last occurrence. A 2400 s file: the episode
// window starts at 1950 s and the movie window at 1800 s, so credits at
// 1850 s count only for a movie.
func TestPlaybackSourceStoresEndCreditsForTheItemKind(t *testing.T) {
	segments := func(starts ...float64) []models.MarkerSegment {
		out := make([]models.MarkerSegment, 0, len(starts))
		for _, start := range starts {
			out = append(out, models.MarkerSegment{Kind: models.MarkerSegmentCredits, StartSeconds: start, EndSeconds: start + 60})
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		episode bool
		starts  []float64
		want    float64
	}{
		{name: "episode ignores credits before its window", episode: true, starts: []float64{1850}, want: 0},
		{name: "movie counts the same credits", starts: []float64{1850}, want: 1850},
		{name: "last occurrence wins", episode: true, starts: []float64{1000, 2000}, want: 2000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version := testCompatVersion()
			version.Duration = 2400
			version.MarkerSegments = segments(tc.starts...)
			source := (&PlaybackHandler{codec: NewResourceIDCodec()}).buildPlaybackSource("item", "play", version, DeviceProfile{}, playbackInfoRequest{episode: tc.episode}, true)
			if source.EndCreditsStart != tc.want {
				t.Fatalf("EndCreditsStart = %v, want %v", source.EndCreditsStart, tc.want)
			}
		})
	}
}

// A stop handled after the native session was reaped, for a play that never
// identified its version, judges completion by the percentage alone: the first
// listed version is only a guess, so its credits don't finish the play. 3100 s
// is past the theatrical cut's credits but only 86% of it.
func TestStoppedScrobbleAfterReapIgnoresGuessedVersionCredits(t *testing.T) {
	theatrical, extended := versionedReportVersions()
	handler, _, _ := newVersionedReportHandler(t, false, []catalog.FileVersion{theatrical, extended}, 0)
	if err := handler.playbackStore.Update("play-1", func(play *PlaybackSession) error {
		play.MediaSources[0].EndCreditsStart = 3000
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	scrobbler := &recordingCompatWatchScrobbler{}
	handler.WatchScrobbler = scrobbler

	body := fmt.Sprintf(`{"PlaySessionId":"play-1","PositionTicks":%d}`, int64(3100)*10_000_000)
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(body))
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
	if event := scrobbler.calls[0].event; event.Completed {
		t.Fatalf("stop event = %+v, want not completed", event)
	}
}
