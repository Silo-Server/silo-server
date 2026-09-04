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

func TestSubtitleExtractionUsesConfiguredFFmpeg(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "configured-ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\ncase \"$*\" in *'-f srt'*) printf '1\\n00:00:01,000 --> 00:00:02,000\\nConfigured binary\\n';; *) printf 'WEBVTT\\n\\n00:00:01.000 --> 00:00:02.000\\nConfigured binary\\n';; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprintf("external=%v", external), func(t *testing.T) {
			file := &models.MediaFile{ID: 42, FilePath: "/synthetic/movie.mkv", SubtitleTracks: []models.SubtitleTrack{{Index: 1, Codec: "subrip"}}}
			if external {
				file.SubtitleTracks = nil
				file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/synthetic/movie.ass", Format: "ass"}}
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
