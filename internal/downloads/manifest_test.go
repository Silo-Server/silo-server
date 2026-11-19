package downloads

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeManifestSource struct {
	detail *catalog.ItemDetail
	err    error
}

func (f fakeManifestSource) GetItemDetail(context.Context, string, catalog.AccessFilter) (*catalog.ItemDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.detail, nil
}

// mapManifestSource serves per-content-id details and counts lookups, so tests
// can assert both access-denial handling and the series lookup.
type mapManifestSource struct {
	details map[string]*catalog.ItemDetail
	calls   map[string]int
}

func (m *mapManifestSource) GetItemDetail(_ context.Context, contentID string, _ catalog.AccessFilter) (*catalog.ItemDetail, error) {
	m.calls[contentID]++
	if d, ok := m.details[contentID]; ok {
		return d, nil
	}
	return nil, catalog.ErrItemNotFound
}

type fakeSubtitleSource struct {
	downloaded []subtitles.DownloadedSubtitle
}

func (f fakeSubtitleSource) ListDownloadedSubtitles(context.Context, int) ([]subtitles.DownloadedSubtitle, error) {
	return f.downloaded, nil
}

func (f fakeSubtitleSource) GetSubtitleContent(context.Context, int) (*subtitles.DownloadedSubtitle, []byte, error) {
	return nil, nil, ErrAssetNotFound
}

type fakeFileResolver struct {
	file *models.MediaFile
}

func (f fakeFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	return f.file, nil
}
func (f fakeFileResolver) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}
func (f fakeFileResolver) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}
func (f fakeFileResolver) ListByEpisodeIDs(context.Context, []string) (map[string][]*models.MediaFile, error) {
	return nil, nil
}

func TestParseSubtitleRef(t *testing.T) {
	cases := []struct {
		ref       string
		wantKind  string
		wantValue int
		wantErr   bool
	}{
		{"external:0", "external", 0, false},
		{"external:12", "external", 12, false},
		{"downloaded:7", "downloaded", 7, false},
		{"embedded:3", "embedded", 3, false},
		{"bogus", "", 0, true},
		{"external:x", "", 0, true},
		{"weird:1", "", 0, true},
		{"", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			kind, value, err := parseSubtitleRef(tc.ref)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidSubtitleRef) {
					t.Fatalf("parseSubtitleRef(%q) err = %v, want ErrInvalidSubtitleRef", tc.ref, err)
				}
				return
			}
			if err != nil || kind != tc.wantKind || value != tc.wantValue {
				t.Fatalf("parseSubtitleRef(%q) = (%q, %d, %v)", tc.ref, kind, value, err)
			}
		})
	}
}

func TestServeEmbeddedSubtitleOnlyServesSidecarTracks(t *testing.T) {
	file := &models.MediaFile{
		ID: 99, FilePath: t.TempDir() + "/missing.mkv",
		SubtitleTracks: []models.SubtitleTrack{
			{Codec: "subrip"},
			{Codec: "hdmv_pgs_subtitle"},
		},
	}
	s := &Service{fileRepo: fakeFileResolver{file: file}}
	dl := &Download{ID: "dl1", MediaFileID: 99}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, ordinal := range []int{-1, 0, 2} {
		if err := s.serveEmbeddedSubtitle(httptest.NewRecorder(), req, dl, ordinal); !errors.Is(err, ErrAssetNotFound) {
			t.Fatalf("ordinal %d err = %v, want ErrAssetNotFound", ordinal, err)
		}
	}
	for _, payload := range []string{"", "PGS"} {
		t.Run(map[bool]string{false: "before output", true: "after output"}[payload != ""], func(t *testing.T) {
			ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
			if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nprintf '"+payload+"'\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			s.artifacts = &ArtifactManager{liveCfg: func() *config.Config {
				return &config.Config{Playback: config.PlaybackConfig{FFmpegPath: ffmpeg}}
			}}
			w := httptest.NewRecorder()
			w.Code = 0 // Distinguish an uncommitted response from an implicit 200.
			var recovered any
			var err error
			func() {
				defer func() { recovered = recover() }()
				err = s.serveEmbeddedSubtitle(w, req, dl, 1)
			}()
			if payload == "" {
				if recovered != nil || err == nil || w.Code != 0 || w.Body.Len() != 0 {
					t.Fatalf("before output: recovered %v, err %v, status %d, bytes %d", recovered, err, w.Code, w.Body.Len())
				}
			} else if recovered != http.ErrAbortHandler || w.Code != http.StatusOK || w.Body.String() != payload { //nolint:errorlint // sentinel compared by identity, as net/http does
				t.Fatalf("after output: recovered %v, status %d, body %q", recovered, w.Code, w.Body.String())
			}
		})
	}
}

// A downloaded subtitle is served with its timing correction, revalidated on
// every use, and answers a matching If-None-Match with 304.
func TestServeDownloadedSubtitleTimingAndRevalidation(t *testing.T) {
	const stored = "1\n00:00:01,000 --> 00:00:02,000\nHello\n"
	sub := &subtitles.DownloadedSubtitle{ID: 7, MediaFileID: 99, Format: subtitles.FormatSRT, Revision: 3,
		Timing: subtitles.Timing{OffsetMS: 500}}

	rr := httptest.NewRecorder()
	if err := serveDownloadedSubtitle(rr, httptest.NewRequest(http.MethodGet, "/", nil), sub, []byte(stored)); err != nil {
		t.Fatal(err)
	}
	etag := rr.Header().Get("ETag")
	if rr.Code != http.StatusOK || rr.Body.String() != "1\n00:00:01,500 --> 00:00:02,500\nHello\n" {
		t.Fatalf("GET = %d %q", rr.Code, rr.Body.String())
	}
	if etag != `"downloaded-7-3-utf8"` || rr.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("headers = %v", rr.Header())
	}

	for _, header := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("If-None-Match", header)
		rr = httptest.NewRecorder()
		if err := serveDownloadedSubtitle(rr, req, sub, []byte(stored)); err != nil {
			t.Fatal(err)
		}
		if rr.Code != http.StatusNotModified || rr.Body.Len() != 0 || rr.Header().Get("ETag") != etag {
			t.Fatalf("If-None-Match %s = %d %q", header, rr.Code, rr.Body.String())
		}
	}

	// A timing change bumps the revision, so the old validator no longer matches.
	changed := *sub
	changed.Revision, changed.Timing = 4, subtitles.Timing{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	rr = httptest.NewRecorder()
	if err := serveDownloadedSubtitle(rr, req, &changed, []byte(stored)); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusOK || rr.Body.String() != stored || rr.Header().Get("ETag") != `"downloaded-7-4-utf8"` {
		t.Fatalf("stale validator = %d %q %q", rr.Code, rr.Header().Get("ETag"), rr.Body.String())
	}
}

type sidecarTimings map[string]*subtitles.ExternalTiming

func (s sidecarTimings) ExternalTiming(_ context.Context, _ int, sha string) (*subtitles.ExternalTiming, error) {
	return s[sha], nil
}

// A sidecar is offered and served with its timing correction under a
// revision that follows both the file on disk and the correction.
func TestSidecarSubtitleTimingAndRevision(t *testing.T) {
	const onDisk = "1\n00:00:01,000 --> 00:00:02,000\nHello\n"
	path := filepath.Join(t.TempDir(), "movie.en.srt")
	if err := os.WriteFile(path, []byte(onDisk), 0o600); err != nil {
		t.Fatal(err)
	}
	sha := subtitles.ContentSHA256([]byte(onDisk))
	timings := sidecarTimings{sha: {Timing: subtitles.Timing{OffsetMS: 500, Scale: 1}, Revision: 2}}
	file := &models.MediaFile{ID: 99, ExternalSubtitles: []models.ExternalSubtitle{{Path: path, Language: "en", Format: "srt"}}}
	b := NewManifestBuilder(nil, nil, fakeFileResolver{file: file}, nil)
	b.externalTimings = timings
	got := b.buildSubtitles(context.Background(), &Download{ID: "dl1", MediaFileID: 99}, file, nil)
	timed := "1\n00:00:01,500 --> 00:00:02,500\nHello\n"
	// The revision follows the delivered (corrected) bytes and the correction's revision.
	if len(got) != 1 || got[0].Revision != subtitles.ContentSHA256([]byte(timed))[:16]+"-2" || got[0].FileSize != int64(len(timed)) {
		t.Fatalf("manifest sidecar = %+v", got)
	}

	s := &Service{externalTimings: timings}
	rr := httptest.NewRecorder()
	if err := s.serveExternalSubtitle(context.Background(), rr, httptest.NewRequest(http.MethodGet, "/", nil), 99, "srt", "", []byte(onDisk)); err != nil {
		t.Fatal(err)
	}
	etag := rr.Header().Get("ETag")
	if rr.Code != http.StatusOK || rr.Body.String() != timed || etag != `"external-`+got[0].Revision+`"` || rr.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("GET = %d %q %v", rr.Code, rr.Body.String(), rr.Header())
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	rr = httptest.NewRecorder()
	if err := s.serveExternalSubtitle(context.Background(), rr, req, 99, "srt", "", []byte(onDisk)); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusNotModified || rr.Body.Len() != 0 {
		t.Fatalf("revalidation = %d %q", rr.Code, rr.Body.String())
	}

	// A new correction changes the revision, so the old validator misses.
	timings[sha] = &subtitles.ExternalTiming{Timing: subtitles.Timing{Scale: 1}, Revision: 3}
	rr = httptest.NewRecorder()
	if err := s.serveExternalSubtitle(context.Background(), rr, req, 99, "srt", "", []byte(onDisk)); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusOK || rr.Body.String() != onDisk {
		t.Fatalf("after reset = %d %q", rr.Code, rr.Body.String())
	}
}
