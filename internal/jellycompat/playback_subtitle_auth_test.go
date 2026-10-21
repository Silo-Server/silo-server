package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeSubtitleRepository struct {
	downloaded map[int][]subtitles.DownloadedSubtitle
}

func (r fakeSubtitleRepository) InsertDownloadedSubtitle(context.Context, *subtitles.DownloadedSubtitle) error {
	panic("unused")
}

func (r fakeSubtitleRepository) GetDownloadedSubtitle(context.Context, int) (*subtitles.DownloadedSubtitle, error) {
	panic("unused")
}

func (r fakeSubtitleRepository) ListDownloadedSubtitles(_ context.Context, mediaFileID int) ([]subtitles.DownloadedSubtitle, error) {
	return r.downloaded[mediaFileID], nil
}

func (r fakeSubtitleRepository) UpdateDownloadedSubtitle(context.Context, int, subtitles.SubtitleMetadataUpdate) (*subtitles.DownloadedSubtitle, error) {
	panic("unused")
}

func (r fakeSubtitleRepository) DeleteDownloadedSubtitle(context.Context, int) (*subtitles.DownloadedSubtitle, error) {
	panic("unused")
}

func (r fakeSubtitleRepository) GetDownloadedSubtitleByS3Key(context.Context, string) (*subtitles.DownloadedSubtitle, error) {
	panic("unused")
}

func (r fakeSubtitleRepository) ListProviderConfigs(context.Context) ([]subtitles.ProviderConfig, error) {
	panic("unused")
}

func (r fakeSubtitleRepository) GetProviderConfig(context.Context, string) (*subtitles.ProviderConfig, error) {
	panic("unused")
}

func (r fakeSubtitleRepository) UpsertProviderConfig(context.Context, *subtitles.ProviderConfig) error {
	panic("unused")
}

func TestHandlePlaybackInfo_AuthenticatesSubtitleDeliveryURLs(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	version := catalog.FileVersion{
		FileID:    42,
		Duration:  3600,
		Container: "mkv",
		Bitrate:   8000,
		VideoTracks: []models.VideoTrack{
			{Codec: "h264", Width: 1920, Height: 1080},
		},
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true, Title: "Main"},
		},
		SubtitleTracks: []catalog.VersionSubtitleTrack{
			{Index: 2, Codec: "subrip", Language: "eng", Title: "English"},
			{Codec: "srt", Language: "spa", Title: "Spanish", External: true},
		},
	}

	handler := &PlaybackHandler{
		content: &stubContentService{detail: &upstreamItemDetail{
			ContentID: contentID,
			Versions:  []catalog.FileVersion{version},
		}},
		codec:          codec,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		playbackStore:  NewPlaybackSessionStore(time.Hour, nil),
		SubtitleRepo: fakeSubtitleRepository{downloaded: map[int][]subtitles.DownloadedSubtitle{
			42: {
				{MediaFileID: 42, Language: "fre", Format: subtitles.FormatSRT, Provider: "opensubtitles"},
			},
		}},
	}

	req := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", strings.NewReader(`{}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))

	rr := httptest.NewRecorder()
	handler.HandlePlaybackInfo(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp playbackInfoResponseDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.MediaSources) != 1 {
		t.Fatalf("media sources = %d, want 1", len(resp.MediaSources))
	}

	subtitleURLs := make([]string, 0, 3)
	for _, stream := range resp.MediaSources[0].MediaStreams {
		if stream.Type == "Subtitle" {
			subtitleURLs = append(subtitleURLs, stream.DeliveryURL)
		}
	}
	if len(subtitleURLs) != 3 {
		t.Fatalf("subtitle URLs = %d, want 3: %#v", len(subtitleURLs), subtitleURLs)
	}

	for _, rawURL := range subtitleURLs {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse subtitle URL %q: %v", rawURL, err)
		}
		if parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/Videos/") {
			t.Fatalf("subtitle URL %q is not API-relative", rawURL)
		}
		query := parsed.Query()
		if got := query.Get("api_key"); got != "token-1" {
			t.Fatalf("api_key for %q = %q, want token-1", rawURL, got)
		}
		if got := query.Get("PlaySessionId"); got != resp.PlaySessionID {
			t.Fatalf("PlaySessionId for %q = %q, want %q", rawURL, got, resp.PlaySessionID)
		}
	}
}

func TestHandleSubtitleStreamAllowsAPIAuxiliaryResourceForProxyRoutedSession(t *testing.T) {
	const subtitleBody = "1\n00:00:00,000 --> 00:00:01,000\nHello\n"
	subtitlePath := filepath.Join(t.TempDir(), "movie.en.srt")
	if err := os.WriteFile(subtitlePath, []byte(subtitleBody), 0o600); err != nil {
		t.Fatal(err)
	}

	file := &models.MediaFile{
		ID:                42,
		ContentID:         "movie-1",
		FilePath:          "/media/movie.mkv",
		ExternalSubtitles: []models.ExternalSubtitle{{Path: subtitlePath, Language: "eng", Format: "srt"}},
	}
	source := PlaybackMediaSource{ID: "source-42", FileID: file.ID}

	for _, route := range []struct {
		name      string
		method    string
		workload  noderouting.Workload
		execution noderouting.Execution
	}{
		{name: "direct play", method: "direct", workload: noderouting.WorkloadDirectPlay, execution: noderouting.ExecutionNone},
		{name: "progressive remux", method: "remux", workload: noderouting.WorkloadRemux, execution: noderouting.ExecutionProxy},
	} {
		t.Run(route.name, func(t *testing.T) {
			assignment := playback.NodeRoutingAssignment{
				Workload:  string(route.workload),
				Execution: string(route.execution),
				Egress:    string(noderouting.EgressProxy),
			}
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1",
				UpstreamSessionID: "upstream-1", UpstreamPlayMethod: route.method,
				MediaSources: []PlaybackMediaSource{source}, RoutingAssignment: &assignment,
			})
			handler := &PlaybackHandler{
				playbackStore: store,
				fileResolver:  testCompatFileResolver{file: file},
			}

			request := httptest.NewRequest(
				http.MethodGet,
				"/Videos/item-1/source-42/Subtitles/1/stream.srt?PlaySessionId=play-1&api_key=token-1",
				nil,
			)
			routeCtx := chi.NewRouteContext()
			routeCtx.URLParams.Add("routeItemId", "item-1")
			routeCtx.URLParams.Add("routeMediaSourceId", source.ID)
			routeCtx.URLParams.Add("routeIndex", "1")
			routeCtx.URLParams.Add("routeFormat", "srt")
			ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
			ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 7})
			recorder := httptest.NewRecorder()

			handler.HandleSubtitleStream(recorder, request.WithContext(ctx))

			if recorder.Code != http.StatusOK || recorder.Body.String() != subtitleBody {
				t.Fatalf("response = %d %q, want API-origin subtitle", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func (r fakeSubtitleRepository) GetDownloadedSubtitleByContent(context.Context, *subtitles.DownloadedSubtitle) (*subtitles.DownloadedSubtitle, error) {
	panic("unused")
}

func TestHandleSubtitleStreamUsesConfiguredFFmpegForEmbeddedText(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	counterPath := filepath.Join(t.TempDir(), "ffmpeg-calls")
	t.Setenv("FFMPEG_COUNTER", counterPath)
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg")
	// A batch extract names its outputs as file: arguments; a single-track
	// extract writes to stdout.
	script := "#!/bin/sh\nprintf x >> \"$FFMPEG_COUNTER\"\nwrote=\n" +
		"for arg in \"$@\"; do case \"$arg\" in file:*) printf '1\\n00:00:01,000 --> 00:00:02,000\\nHello\\n' > \"${arg#file:}\"; wrote=1;; esac; done\n" +
		"[ -n \"$wrote\" ] || printf '1\\n00:00:01,000 --> 00:00:02,000\\nHello\\n'\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}

	file := &models.MediaFile{
		ID:       42,
		FilePath: mediaPath,
		VideoTracks: []models.VideoTrack{
			{Codec: "h264"},
		},
		AudioTracks: []models.AudioTrack{
			{Codec: "aac"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 2, Codec: "subrip", Language: "eng"},
		},
	}
	source := PlaybackMediaSource{ID: "source-42", FileID: file.ID}
	cacheRoot := t.TempDir()
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1",
		MediaSources: []PlaybackMediaSource{source},
	})
	handler := &PlaybackHandler{
		playbackStore: store,
		fileResolver:  testCompatFileResolver{file: file},
		FFmpegPath:    ffmpegPath,
		SubtitleCache: playback.NewSubtitleCache(func() string { return cacheRoot }),
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/Videos/item-1/source-42/Subtitles/2/stream.js?PlaySessionId=play-1&api_key=token-1",
		nil,
	)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("routeItemId", "item-1")
	routeCtx.URLParams.Add("routeMediaSourceId", source.ID)
	routeCtx.URLParams.Add("routeIndex", "2")
	routeCtx.URLParams.Add("routeFormat", "js")
	ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 7})
	recorder := httptest.NewRecorder()

	handler.HandleSubtitleStream(recorder, request.WithContext(ctx))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		TrackEvents []struct {
			ID                 string `json:"Id"`
			Text               string `json:"Text"`
			StartPositionTicks int64  `json:"StartPositionTicks"`
			EndPositionTicks   int64  `json:"EndPositionTicks"`
		} `json:"TrackEvents"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode JSON subtitles: %v; body = %q", err, recorder.Body.String())
	}
	if len(body.TrackEvents) != 1 {
		t.Fatalf("TrackEvents = %#v, want one event", body.TrackEvents)
	}
	event := body.TrackEvents[0]
	if event.ID != "1" || event.Text != "Hello" || event.StartPositionTicks != 10_000_000 || event.EndPositionTicks != 20_000_000 {
		t.Fatalf("TrackEvents[0] = %#v, want Jellyfin JSON cue", event)
	}

	secondRecorder := httptest.NewRecorder()
	handler.HandleSubtitleStream(secondRecorder, request.Clone(ctx))
	if secondRecorder.Code != http.StatusOK || secondRecorder.Body.String() != recorder.Body.String() {
		t.Fatalf("cached response = %d %q, want first response %d %q", secondRecorder.Code, secondRecorder.Body.String(), recorder.Code, recorder.Body.String())
	}
	calls, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read ffmpeg call counter: %v", err)
	}
	if got := len(calls); got != 1 {
		t.Fatalf("ffmpeg calls = %d, want 1 after cached request", got)
	}
}

func TestHandleSubtitleStreamReturnsJellyfinJSONForExternalSRT(t *testing.T) {
	const subtitleBody = "1\n00:00:01,250 --> 00:00:02,500\nFirst line\nSecond line\n"
	subtitlePath := filepath.Join(t.TempDir(), "movie.en.srt")
	if err := os.WriteFile(subtitlePath, []byte(subtitleBody), 0o600); err != nil {
		t.Fatal(err)
	}

	file := &models.MediaFile{
		ID:                42,
		FilePath:          "/media/movie.mkv",
		ExternalSubtitles: []models.ExternalSubtitle{{Path: subtitlePath, Language: "eng", Format: "srt"}},
	}
	source := PlaybackMediaSource{ID: "source-42", FileID: file.ID}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1",
		MediaSources: []PlaybackMediaSource{source},
	})
	handler := &PlaybackHandler{playbackStore: store, fileResolver: testCompatFileResolver{file: file}}

	request := httptest.NewRequest(
		http.MethodGet,
		"/Videos/item-1/source-42/Subtitles/1/stream.js?PlaySessionId=play-1&api_key=token-1",
		nil,
	)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("routeItemId", "item-1")
	routeCtx.URLParams.Add("routeMediaSourceId", source.ID)
	routeCtx.URLParams.Add("routeIndex", "1")
	routeCtx.URLParams.Add("routeFormat", "js")
	ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 7})
	recorder := httptest.NewRecorder()

	handler.HandleSubtitleStream(recorder, request.WithContext(ctx))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	var body struct {
		TrackEvents []struct {
			Text               string `json:"Text"`
			StartPositionTicks int64  `json:"StartPositionTicks"`
			EndPositionTicks   int64  `json:"EndPositionTicks"`
		} `json:"TrackEvents"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode JSON subtitles: %v; content type = %q; body = %q", err, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	if len(body.TrackEvents) != 1 || body.TrackEvents[0].Text != "First line\nSecond line" ||
		body.TrackEvents[0].StartPositionTicks != 12_500_000 || body.TrackEvents[0].EndPositionTicks != 25_000_000 {
		t.Fatalf("TrackEvents = %#v, want parsed external cue", body.TrackEvents)
	}
}

func TestSubtitleExtractionUsesConfiguredFFmpeg(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "configured-ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\ncase \"$*\" in *'-f srt'*) printf '1\\n00:00:01,000 --> 00:00:02,000\\nConfigured binary\\n';; *) printf 'WEBVTT\\n\\n00:00:01.000 --> 00:00:02.000\\nConfigured binary\\n';; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprintf("external=%v", external), func(t *testing.T) {
			file := &models.MediaFile{ID: 42, FilePath: "/synthetic/movie.mkv", SubtitleTracks: []models.SubtitleTrack{{Index: 1, Codec: "subrip"}}}
			if external {
				sidecar := filepath.Join(t.TempDir(), "movie.ass")
				if err := os.WriteFile(sidecar, []byte("[Script Info]\nScriptType: v4.00+\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				file.SubtitleTracks = nil
				file.ExternalSubtitles = []models.ExternalSubtitle{{Path: sidecar, Format: "ass"}}
			}
			source := PlaybackMediaSource{ID: "source-42", FileID: 42}
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1", MediaSources: []PlaybackMediaSource{source}})
			handler := &PlaybackHandler{playbackStore: store, fileResolver: testCompatFileResolver{file: file}, FFmpegPath: ffmpeg}
			route := chi.NewRouteContext()
			route.URLParams.Add("routeItemId", "item-1")
			route.URLParams.Add("routeMediaSourceId", source.ID)
			index := "1"

			route.URLParams.Add("routeIndex", index)
			route.URLParams.Add("routeFormat", "vtt")
			ctx := context.WithValue(t.Context(), chi.RouteCtxKey, route)
			ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 7})
			request := httptest.NewRequest(http.MethodGet, "/Videos/item-1/source-42/Subtitles/"+index+"/stream.vtt?PlaySessionId=play-1&api_key=token-1", nil).WithContext(ctx)
			recorder := httptest.NewRecorder()
			handler.HandleSubtitleStream(recorder, request)
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Configured binary") {
				t.Fatalf("configured extraction = %d %q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

type fakeSubtitleBlobs map[string][]byte

func (fakeSubtitleBlobs) Put(context.Context, string, []byte) error { return nil }
func (b fakeSubtitleBlobs) Get(_ context.Context, key string) ([]byte, error) {
	return append([]byte(nil), b[key]...), nil
}
func (fakeSubtitleBlobs) Delete(context.Context, string) error { return nil }

// A downloaded subtitle's stored timing correction is applied before the
// Jellyfin conversion, so every representation carries the corrected cues.
func TestHandleSubtitleStreamAppliesDownloadedSubtitleTiming(t *testing.T) {
	file := &models.MediaFile{
		ID:          42,
		VideoTracks: []models.VideoTrack{{Codec: "h264"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac"}},
	}
	source := PlaybackMediaSource{ID: "source-42", FileID: file.ID}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1",
		MediaSources: []PlaybackMediaSource{source},
	})
	handler := &PlaybackHandler{
		playbackStore: store,
		fileResolver:  testCompatFileResolver{file: file},
		SubtitleRepo: fakeSubtitleRepository{downloaded: map[int][]subtitles.DownloadedSubtitle{42: {{
			ID: 9, MediaFileID: 42, Format: subtitles.FormatSRT, S3Key: "timed.srt",
			Timing: subtitles.Timing{OffsetMS: 2500, Scale: 1},
		}}}},
		SubtitleBlobs: fakeSubtitleBlobs{"timed.srt": []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")},
	}
	index := strconv.Itoa(computeDownloadedSubBaseIndex(file))
	serve := func(format string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet,
			"/Videos/item-1/source-42/Subtitles/"+index+"/stream."+format+"?PlaySessionId=play-1&api_key=token-1", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("routeItemId", "item-1")
		routeCtx.URLParams.Add("routeMediaSourceId", source.ID)
		routeCtx.URLParams.Add("routeIndex", index)
		routeCtx.URLParams.Add("routeFormat", format)
		ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
		ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 7})
		recorder := httptest.NewRecorder()
		handler.HandleSubtitleStream(recorder, request.WithContext(ctx))
		return recorder
	}
	if rr := serve("srt"); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "00:00:03,500 --> 00:00:04,500") {
		t.Fatalf("srt = %d %q", rr.Code, rr.Body.String())
	}
	if rr := serve("vtt"); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "00:00:03.500 --> 00:00:04.500") {
		t.Fatalf("vtt = %d %q", rr.Code, rr.Body.String())
	}
}

type fakeSidecarTimings map[string]*subtitles.ExternalTiming

func (f fakeSidecarTimings) ExternalTiming(_ context.Context, _ int, sha string) (*subtitles.ExternalTiming, error) {
	return f[sha], nil
}

// recordedPlays stands in for the sync service and records played subtitles.
type recordedPlays struct{ targets []subtitles.SyncTarget }

func (p *recordedPlays) SubtitlePlayed(_ context.Context, target subtitles.SyncTarget) {
	p.targets = append(p.targets, target)
}

func TestHandleSubtitleStreamAppliesSidecarTiming(t *testing.T) {
	const onDisk = "1\n00:00:01,000 --> 00:00:02,000\nHello\n"
	path := filepath.Join(t.TempDir(), "movie.en.srt")
	if err := os.WriteFile(path, []byte(onDisk), 0o600); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{
		ID:                42,
		VideoTracks:       []models.VideoTrack{{Codec: "h264"}},
		AudioTracks:       []models.AudioTrack{{Codec: "aac"}},
		ExternalSubtitles: []models.ExternalSubtitle{{Path: path, Language: "en", Format: "srt"}},
	}
	source := PlaybackMediaSource{ID: "source-42", FileID: file.ID}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1",
		MediaSources: []PlaybackMediaSource{source},
	})
	handler := &PlaybackHandler{
		playbackStore:   store,
		fileResolver:    testCompatFileResolver{file: file},
		ExternalTimings: fakeSidecarTimings{subtitles.ContentSHA256([]byte(onDisk)): {Timing: subtitles.Timing{OffsetMS: 2500, Scale: 1}, Revision: 2}},
	}
	plays := &recordedPlays{}
	handler.PlaySync = plays
	index := strconv.Itoa(externalSubtitleRouteIndex(file, 0))
	for format, want := range map[string]string{"srt": "00:00:03,500 --> 00:00:04,500", "vtt": "00:00:03.500 --> 00:00:04.500"} {
		request := httptest.NewRequest(http.MethodGet,
			"/Videos/item-1/source-42/Subtitles/"+index+"/stream."+format+"?PlaySessionId=play-1&api_key=token-1", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("routeItemId", "item-1")
		routeCtx.URLParams.Add("routeMediaSourceId", source.ID)
		routeCtx.URLParams.Add("routeIndex", index)
		routeCtx.URLParams.Add("routeFormat", format)
		ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
		ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 7})
		rr := httptest.NewRecorder()
		handler.HandleSubtitleStream(rr, request.WithContext(ctx))
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), want) || rr.Header().Get("Cache-Control") != "private, no-cache" {
			t.Fatalf("%s = %d %q %v", format, rr.Code, rr.Body.String(), rr.Header())
		}
	}
	// Each delivery tells the sync service the sidecar is being played.
	played := subtitles.SyncTarget{MediaFileID: 42, ExternalPath: path}
	if len(plays.targets) != 2 || plays.targets[0] != played || plays.targets[1] != played {
		t.Fatalf("played %+v", plays.targets)
	}
}
