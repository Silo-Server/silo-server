package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type recordingTrackPreferences struct {
	audio     []userstore.AudioPreference
	subtitles []userstore.SubtitlePreference
	attempts  int
	fail      error
	// beforeSubtitleWrite runs once inside the next subtitle write, before it
	// lands, to interleave another report.
	beforeSubtitleWrite func()
}

func (r *recordingTrackPreferences) SetAudioPreference(_ context.Context, userID int, pref userstore.AudioPreference) error {
	r.attempts++
	if userID != 1 {
		return fmt.Errorf("user %d, want 1", userID)
	}
	if r.fail != nil {
		return r.fail
	}
	r.audio = append(r.audio, pref)
	return nil
}

func (r *recordingTrackPreferences) SetSubtitlePreferenceCanonical(_ context.Context, userID int, pref userstore.SubtitlePreference) error {
	if before := r.beforeSubtitleWrite; before != nil {
		r.beforeSubtitleWrite = nil
		before()
	}
	r.attempts++
	if userID != 1 {
		return fmt.Errorf("user %d, want 1", userID)
	}
	if r.fail != nil {
		return r.fail
	}
	r.subtitles = append(r.subtitles, pref)
	return nil
}

// Stream indexes of trackMemoryVersion as PlaybackInfo advertises them.
const (
	trackMemoryJapaneseAudio  = 1
	trackMemoryEnglishAudio   = 2
	trackMemoryForcedSubtitle = 3
	trackMemorySDHSubtitle    = 5
)

func trackMemoryVersion() catalog.FileVersion {
	return catalog.FileVersion{
		FileID:      42,
		Duration:    1440,
		Container:   "mkv",
		VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}},
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Language: "ja", Title: "Japanese", Channels: 2, Layout: "stereo", Default: true},
			{Codec: "aac", Language: "en", Title: "English", Channels: 2, Layout: "stereo"},
		},
		SubtitleTracks: []catalog.VersionSubtitleTrack{
			{Language: "en", Codec: "subrip", Title: "Forced", Forced: true, Default: true},
			{Language: "en", Codec: "subrip", Title: "English"},
			{Language: "en", Codec: "subrip", Title: "SDH", HearingImpaired: true},
		},
	}
}

// newTrackMemoryPlay is an episode play that started on Japanese audio and
// the forced English subtitles, the defaults PlaybackInfo resolved.
func newTrackMemoryPlay(t *testing.T, preferenceKey string) (*PlaybackHandler, *recordingTrackPreferences, userstore.UserStore, string) {
	t.Helper()
	codec := NewResourceIDCodec()
	version := trackMemoryVersion()
	source := PlaybackMediaSource{
		ID:                          codec.EncodeIntID(EncodedIDMediaSource, int64(version.FileID)),
		FileID:                      version.FileID,
		Version:                     version,
		SupportsDirectPlay:          true,
		DefaultAudioStreamIndex:     intPtr(trackMemoryJapaneseAudio),
		SelectedAudioStreamIndex:    intPtr(trackMemoryJapaneseAudio),
		DefaultSubtitleStreamIndex:  intPtr(trackMemoryForcedSubtitle),
		SelectedSubtitleStreamIndex: intPtr(trackMemoryForcedSubtitle),
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID:                       "play-1",
		CompatToken:              "token-1",
		ItemID:                   "episode-1",
		RouteItemID:              codec.EncodeStringID(EncodedIDItem, "episode-1"),
		PreferenceKey:            preferenceKey,
		UpstreamSessionID:        "upstream-1",
		UpstreamPlayMethod:       "direct",
		ProgressPersistenceKnown: true,
		MediaSources:             []PlaybackMediaSource{source},
	})
	userStore := newJellycompatUserStore(t)
	prefs := &recordingTrackPreferences{}
	handler := &PlaybackHandler{
		codec:               codec,
		playbackStore:       store,
		sessionMgr:          &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1", PlayMethod: playback.PlayDirect, BasePlayMethod: playback.PlayDirect}}},
		tm:                  playback.NewTranscodeManager(),
		storeProvider:       compatTestUserStoreProvider{store: userStore},
		AudioPreferences:    prefs,
		SubtitlePreferences: prefs,
	}
	return handler, prefs, userStore, source.ID
}

func postTrackReport(t *testing.T, handler *PlaybackHandler, path, sourceID string, audio, subtitle int) {
	t.Helper()
	body := fmt.Sprintf(`{"PlaySessionId":"play-1","MediaSourceId":%q,"AudioStreamIndex":%d,"SubtitleStreamIndex":%d,"PositionTicks":600000000}`,
		sourceID, audio, subtitle)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	switch path {
	case "/Sessions/Playing":
		handler.HandleSessionPlaying(rec, req)
	case "/Sessions/Playing/Stopped":
		handler.HandleSessionPlayingStopped(rec, req)
	default:
		handler.HandleSessionPlayingProgress(rec, req)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("%s: status = %d, body = %s", path, rec.Code, rec.Body.String())
	}
}

func TestPlaybackReportOnDefaultTracksRemembersNothing(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "series-1")

	postTrackReport(t, handler, "/Sessions/Playing", sourceID, trackMemoryJapaneseAudio, trackMemoryForcedSubtitle)
	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryJapaneseAudio, trackMemoryForcedSubtitle)
	postTrackReport(t, handler, "/Sessions/Playing/Stopped", sourceID, trackMemoryJapaneseAudio, trackMemoryForcedSubtitle)

	if len(prefs.audio) != 0 || len(prefs.subtitles) != 0 {
		t.Fatalf("saved audio %+v, subtitles %+v; the default tracks are not a choice", prefs.audio, prefs.subtitles)
	}
}

func TestPlaybackReportRemembersChangedTracksForTheSeries(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "series-1")
	// The play already streams English audio, so only the preference changes.
	if err := handler.playbackStore.Update("play-1", func(s *PlaybackSession) error {
		s.MediaSources[0].SelectedAudioStreamIndex = intPtr(trackMemoryEnglishAudio)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryEnglishAudio, trackMemorySDHSubtitle)
	// Every later report repeats the same tracks; they are saved once.
	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryEnglishAudio, trackMemorySDHSubtitle)

	if len(prefs.audio) != 1 {
		t.Fatalf("audio saves = %d, want 1: %+v", len(prefs.audio), prefs.audio)
	}
	audio := prefs.audio[0]
	if audio.ProfileID != "profile-1" || audio.SeriesID != "series-1" || audio.AudioTrackIndex != 1 || audio.AudioLanguage != "en" {
		t.Fatalf("audio preference = %+v", audio)
	}
	want := playback.AudioTrackSignatureFromTrack(trackMemoryVersion().AudioTracks[1])
	if audio.TrackSignature == nil || *audio.TrackSignature != *want {
		t.Fatalf("audio signature = %+v, want %+v", audio.TrackSignature, want)
	}

	if len(prefs.subtitles) != 1 {
		t.Fatalf("subtitle saves = %d, want 1: %+v", len(prefs.subtitles), prefs.subtitles)
	}
	subtitle := prefs.subtitles[0]
	wantSig := userstore.SubtitleTrackSignature{Source: playback.SubtitleSourceEmbeddedV3, Language: "en", Codec: "subrip", Label: "SDH", HearingImpaired: true}
	if subtitle.SeriesID != "series-1" || subtitle.SubtitleTrackIndex != 2 || subtitle.SubtitleLanguage != "en" ||
		subtitle.SubtitleMode != profileSubtitleAlways || subtitle.TrackSignature == nil || *subtitle.TrackSignature != wantSig {
		t.Fatalf("subtitle preference = %+v (signature %+v)", subtitle, subtitle.TrackSignature)
	}
	// The saved signature is the one PlaybackInfo matches for the next episode.
	candidates := compatSubtitleCandidates(trackMemoryVersion(), nil)
	if index := compatSignatureSubtitleIndex(candidates, subtitle.TrackSignature); index == nil || *index != trackMemorySDHSubtitle {
		t.Fatalf("signature selects %v, want stream %d", index, trackMemorySDHSubtitle)
	}

	// Switching back to the default is a new choice too.
	postTrackReport(t, handler, "/Sessions/Playing/Stopped", sourceID, trackMemoryEnglishAudio, trackMemoryForcedSubtitle)
	if len(prefs.subtitles) != 2 || prefs.subtitles[1].TrackSignature == nil || !prefs.subtitles[1].TrackSignature.Forced {
		t.Fatalf("subtitle saves after switching back = %+v", prefs.subtitles)
	}
	if len(prefs.audio) != 1 {
		t.Fatalf("audio saves = %d after an unchanged audio report, want 1", len(prefs.audio))
	}
}

func TestPlaybackReportRemembersSubtitlesOff(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "series-1")

	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryJapaneseAudio, -1)

	if len(prefs.audio) != 0 {
		t.Fatalf("audio saves = %+v, want none", prefs.audio)
	}
	if len(prefs.subtitles) != 1 {
		t.Fatalf("subtitle saves = %+v, want one", prefs.subtitles)
	}
	off := prefs.subtitles[0]
	if off.SubtitleTrackIndex != -1 || off.SubtitleMode != profileSubtitleOff || off.SubtitleLanguage != "" || off.TrackSignature != nil {
		t.Fatalf("subtitles-off preference = %+v", off)
	}
}

func TestPlaybackReportHonorsRememberSelectionSettings(t *testing.T) {
	handler, prefs, userStore, sourceID := newTrackMemoryPlay(t, "series-1")
	if err := userStore.SetSetting(context.Background(), configurationKey("profile-1"), `{"RememberAudioSelections":true,"RememberSubtitleSelections":false}`); err != nil {
		t.Fatal(err)
	}

	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryEnglishAudio, trackMemorySDHSubtitle)
	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryEnglishAudio, trackMemorySDHSubtitle)

	if len(prefs.audio) != 1 || len(prefs.subtitles) != 0 {
		t.Fatalf("saved audio %+v, subtitles %+v; want audio only", prefs.audio, prefs.subtitles)
	}
}

// TestPlaybackReportWritesEachChangeOnce covers a write that fails, for
// example on a value the canonical settings refuse: reports arrive every few
// seconds, and retrying each one would repeat the failing write for the rest
// of the play. The next change is written again.
func TestPlaybackReportWritesEachChangeOnce(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "series-1")
	prefs.fail = errors.New("store unavailable")

	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryJapaneseAudio, -1)
	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryJapaneseAudio, -1)
	if prefs.attempts != 1 {
		t.Fatalf("write attempts = %d for one change, want 1", prefs.attempts)
	}

	prefs.fail = nil
	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryJapaneseAudio, trackMemorySDHSubtitle)
	if len(prefs.subtitles) != 1 || prefs.subtitles[0].TrackSignature == nil || prefs.subtitles[0].TrackSignature.Label != "SDH" {
		t.Fatalf("subtitle saves = %+v, want the next change", prefs.subtitles)
	}
}

// TestPlaybackReportIgnoresTerminalPlays covers a retried or late Stopped
// report for a play whose stop was already handled: the viewer may have
// picked other tracks in a later play since.
func TestPlaybackReportIgnoresTerminalPlays(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "series-1")
	current, ok := handler.playbackStore.Get("play-1")
	if !ok {
		t.Fatal("play-1 missing")
	}
	current.Terminal = true
	handler.playbackStore.Put(*current)

	postTrackReport(t, handler, "/Sessions/Playing/Stopped", sourceID, trackMemoryEnglishAudio, -1)

	if prefs.attempts != 0 {
		t.Fatalf("write attempts = %d for a terminal play, want 0", prefs.attempts)
	}
}

func TestPlaybackReportIgnoresUnknownNegativeSubtitleIndexes(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "series-1")

	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryJapaneseAudio, -2)

	if prefs.attempts != 0 {
		t.Fatalf("write attempts = %d for SubtitleStreamIndex -2, want 0", prefs.attempts)
	}
}

// TestCompatCombinedSubtitleIndexMatchesPlaybackV3 checks the stored track
// index uses the order Silo clients store: external files, then embedded
// tracks, then downloaded subtitles.
func TestCompatCombinedSubtitleIndexMatchesPlaybackV3(t *testing.T) {
	version := trackMemoryVersion()
	version.SubtitleTracks = append(version.SubtitleTracks, catalog.VersionSubtitleTrack{
		Language: "fr", Codec: "subrip", External: true, FileName: "Show.S01E01.fr.srt",
	})
	candidates := compatSubtitleCandidates(version, []subtitles.DownloadedSubtitle{{Language: "de", Format: "srt"}})
	want := map[string]int{"Show.S01E01.fr.srt": 0, "Forced": 1, "English": 2, "SDH": 3, "de": 4}
	for position, candidate := range candidates {
		if got := compatCombinedSubtitleIndex(candidates, position); got != want[candidate.Label] {
			t.Errorf("%s: combined index = %d, want %d", candidate.Label, got, want[candidate.Label])
		}
	}
}

func TestPlaybackReportWithoutPreferenceKeyRemembersNothing(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "")

	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryJapaneseAudio, trackMemorySDHSubtitle)

	if len(prefs.audio) != 0 || len(prefs.subtitles) != 0 {
		t.Fatalf("saved audio %+v, subtitles %+v for a play with no series", prefs.audio, prefs.subtitles)
	}
}

func TestCompatTrackPreferenceKey(t *testing.T) {
	for _, tc := range []struct {
		detail upstreamItemDetail
		want   string
	}{
		{upstreamItemDetail{Type: "episode", ContentID: "episode-1", SeriesID: "series-1"}, "series-1"},
		{upstreamItemDetail{Type: "movie", ContentID: "movie-1"}, "movie-1"},
		{upstreamItemDetail{Type: "audiobook", ContentID: "book-1"}, ""},
	} {
		if got := compatTrackPreferenceKey(&tc.detail); got != tc.want {
			t.Errorf("%s: key = %q, want %q", tc.detail.Type, got, tc.want)
		}
	}
}

func TestCompatPreferenceLanguageDropsUndefinedTags(t *testing.T) {
	for language, want := range map[string]string{"en": "en", "jpn": "jpn", "und": "", "": "", "not a tag": ""} {
		if got := compatPreferenceLanguage(language); got != want {
			t.Errorf("compatPreferenceLanguage(%q) = %q, want %q", language, got, want)
		}
	}
}

// TestPlaybackReportOnTheDeviceFallbackRemembersNothing covers a client whose
// profile cannot play the track the viewer's preferences select, so
// PlaybackInfo offered a fallback in its place: playing either default is no
// choice.
func TestPlaybackReportOnTheDeviceFallbackRemembersNothing(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "series-1")
	if err := handler.playbackStore.Update("play-1", func(s *PlaybackSession) error {
		s.MediaSources[0].DefaultAudioStreamIndex = intPtr(trackMemoryEnglishAudio)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryJapaneseAudio, trackMemoryForcedSubtitle)
	if err := handler.playbackStore.Update("play-1", func(s *PlaybackSession) error {
		s.MediaSources[0].SelectedAudioStreamIndex = intPtr(trackMemoryEnglishAudio)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryEnglishAudio, trackMemoryForcedSubtitle)

	if prefs.attempts != 0 {
		t.Fatalf("write attempts = %d for the preferred track and its fallback, want 0: %+v", prefs.attempts, prefs.audio)
	}
}

// TestPlaybackReportSkipsARefusedAudioSwitch covers an audio switch the
// server refused: the play keeps the old track, so the viewer never heard the
// requested one and it is not remembered.
func TestPlaybackReportSkipsARefusedAudioSwitch(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "series-1")
	if err := handler.playbackStore.Update("play-1", func(s *PlaybackSession) error {
		s.MediaSources[0].HLSRemux = true
		s.MediaSources[0].HLSRemuxAudioStreamIndexes = []int{trackMemoryJapaneseAudio}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryEnglishAudio, trackMemoryForcedSubtitle)

	if prefs.attempts != 0 {
		t.Fatalf("saved audio %+v for a refused switch", prefs.audio)
	}
}

// TestPlaybackReportDoesNotAdoptAnUnknownSubtitleIndex covers an index that
// names no track: it saves nothing and does not become the play's baseline,
// so reporting the default afterwards still saves nothing.
func TestPlaybackReportDoesNotAdoptAnUnknownSubtitleIndex(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "series-1")

	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryJapaneseAudio, 99)
	postTrackReport(t, handler, "/Sessions/Playing/Progress", sourceID, trackMemoryJapaneseAudio, trackMemoryForcedSubtitle)

	if prefs.attempts != 0 {
		t.Fatalf("subtitle saves = %+v, want none", prefs.subtitles)
	}
}

// TestRememberCompatTracksClaimsAgainstTheCurrentPlay covers a report judged
// from a stale copy of the play, as a second replica or an overlapping retry
// sees it: the change another report already claimed is not written again.
func TestRememberCompatTracksClaimsAgainstTheCurrentPlay(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "series-1")
	stale, ok := handler.playbackStore.Get("play-1")
	if !ok {
		t.Fatal("play-1 missing")
	}
	session := &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}
	off := compatIntValue(-1)
	req := sessionReportRequest{PlaySessionID: "play-1", MediaSourceID: sourceID, SubtitleStreamIndex: &off}

	handler.rememberCompatTracks(t.Context(), session, stale, req)
	handler.rememberCompatTracks(t.Context(), session, stale, req)

	if prefs.attempts != 1 {
		t.Fatalf("write attempts = %d for one change seen twice, want 1", prefs.attempts)
	}
}

// TestCompatReportedSourceFollowsTheStreamedVersion covers a multi-version
// play whose reports omit MediaSourceId: the tracks belong to the version the
// play streams, and without one known nothing is matched.
func TestCompatReportedSourceFollowsTheStreamedVersion(t *testing.T) {
	play := &PlaybackSession{MediaSources: []PlaybackMediaSource{{ID: "a", FileID: 1}, {ID: "b", FileID: 2}}}
	if source := compatReportedSource(play, ""); source != nil {
		t.Fatalf("source = %q before the play streams a version, want none", source.ID)
	}
	play.UpstreamMediaFileID = 2
	if source := compatReportedSource(play, ""); source == nil || source.ID != "b" {
		t.Fatalf("source = %+v, want the streamed version b", source)
	}
	if source := compatReportedSource(play, "a"); source == nil || source.ID != "a" {
		t.Fatalf("source = %+v, want the named version a", source)
	}
}

// TestRememberCompatTracksLeavesTheLatestClaimWhenWritesInterleave covers a
// later report that claims and saves another track while an earlier report is
// still writing, so the earlier write lands last: the earlier report reads the
// play's latest claim back and saves it again.
func TestRememberCompatTracksLeavesTheLatestClaimWhenWritesInterleave(t *testing.T) {
	handler, prefs, _, sourceID := newTrackMemoryPlay(t, "series-1")
	session := &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}
	report := func(index int) {
		current, ok := handler.playbackStore.Get("play-1")
		if !ok {
			t.Fatal("play-1 missing")
		}
		value := compatIntValue(index)
		handler.rememberCompatTracks(t.Context(), session, current,
			sessionReportRequest{PlaySessionID: "play-1", MediaSourceID: sourceID, SubtitleStreamIndex: &value})
	}
	prefs.beforeSubtitleWrite = func() { report(-1) }

	report(trackMemorySDHSubtitle)

	last := prefs.subtitles[len(prefs.subtitles)-1]
	if last.SubtitleMode != profileSubtitleOff {
		t.Fatalf("last subtitle write = %+v (all: %d), want the later off choice", last, len(prefs.subtitles))
	}
}
