package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeSubtitleRepository struct {
	downloaded map[int][]subtitles.DownloadedSubtitle
}

func (r fakeSubtitleRepository) InsertDownloadedSubtitle(context.Context, *subtitles.DownloadedSubtitle) error {
	panic("unused")
}

func (r fakeSubtitleRepository) GetDownloadedSubtitle(_ context.Context, id int) (*subtitles.DownloadedSubtitle, error) {
	for _, stored := range r.downloaded {
		for i := range stored {
			if stored[i].ID == id {
				return &stored[i], nil
			}
		}
	}
	return nil, nil
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

// The media URLs PlaybackInfo builds are authorized by PlaySessionId alone and
// never carry the caller's token, which grants the whole account for as long
// as the login lasts.
func TestHandlePlaybackInfoKeepsTokenOutOfMediaURLs(t *testing.T) {
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
		if got := parsed.Query().Get("PlaySessionId"); got != resp.PlaySessionID {
			t.Fatalf("PlaySessionId for %q = %q, want %q", rawURL, got, resp.PlaySessionID)
		}
	}
	source := resp.MediaSources[0]
	if source.DirectStreamURL == "" {
		t.Fatal("no DirectStreamUrl to check")
	}
	for _, rawURL := range append(subtitleURLs, source.DirectStreamURL, source.TranscodingURL) {
		if strings.Contains(rawURL, "token-1") {
			t.Fatalf("media URL %q carries the caller's token", rawURL)
		}
	}
}

func (r fakeSubtitleRepository) GetDownloadedSubtitleByContent(context.Context, *subtitles.DownloadedSubtitle) (*subtitles.DownloadedSubtitle, error) {
	panic("unused")
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

type fakeSubtitleBlobs map[string][]byte

func (fakeSubtitleBlobs) Put(context.Context, string, []byte) error { return nil }
func (b fakeSubtitleBlobs) Get(_ context.Context, key string) ([]byte, error) {
	return append([]byte(nil), b[key]...), nil
}
func (fakeSubtitleBlobs) Delete(context.Context, string) error { return nil }

type fakeSidecarTimings map[string]*subtitles.ExternalTiming

func (f fakeSidecarTimings) ExternalTiming(_ context.Context, _ int, sha string) (*subtitles.ExternalTiming, error) {
	return f[sha], nil
}

// recordedPlays stands in for the sync service and records played subtitles.
type recordedPlays struct{ targets []subtitles.SyncTarget }

func (p *recordedPlays) SubtitlePlayed(_ context.Context, target subtitles.SyncTarget) {
	p.targets = append(p.targets, target)
}
