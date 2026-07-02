package downloads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
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

// TestManifestBuilderDeniesRestrictedProfile is the Phase 2 acceptance criterion
// at the source: when the requesting profile is denied content access,
// GetItemDetail returns ErrItemNotFound and Build propagates it.
func TestManifestBuilderDeniesRestrictedProfile(t *testing.T) {
	b := NewManifestBuilder(fakeManifestSource{err: catalog.ErrItemNotFound}, nil, nil, nil)
	_, err := b.Build(context.Background(), &Download{ID: "dl1", ContentID: "c1"}, catalog.AccessFilter{})
	if !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatalf("Build err = %v, want catalog.ErrItemNotFound", err)
	}
}

func TestManifestBuilderAssembles(t *testing.T) {
	detail := &catalog.ItemDetail{
		Type:              "movie",
		Title:             "The Movie",
		Year:              2021,
		Overview:          "A film.",
		Runtime:           120,
		ContentRating:     "PG-13",
		Genres:            []string{"Drama"},
		PosterURL:         "https://s3.example.com/poster.jpg?sig=SECRET",
		PosterThumbhash:   "PHASH",
		BackdropURL:       "https://s3.example.com/backdrop.jpg?sig=SECRET",
		BackdropThumbhash: "BHASH",
		ImdbID:            "tt123",
		TmdbID:            "456",
		Intro:             &catalog.Marker{Start: 0, End: 60},
		Versions: []catalog.FileVersion{{
			FileID:     99,
			Container:  "mkv",
			CodecVideo: "h264",
			CodecAudio: "aac",
			Resolution: "1080p",
			Duration:   7200,
			Chapters: []catalog.VersionChapter{{
				Index: 1, Title: "Opening", StartSeconds: 0, EndSeconds: 600,
				ThumbnailURL: "https://s3.example.com/chap.jpg?sig=SECRET", ThumbnailThumbhash: "CHASH",
			}},
		}},
	}
	file := &models.MediaFile{ExternalSubtitles: []models.ExternalSubtitle{
		{Path: "/media/sub.en.srt", Language: "en", Format: "srt", Forced: true},
	}}
	subs := fakeSubtitleSource{downloaded: []subtitles.DownloadedSubtitle{
		{ID: 7, MediaFileID: 99, Language: "fr", Format: subtitles.SubtitleFormat("vtt")},
	}}
	b := NewManifestBuilder(fakeManifestSource{detail: detail}, subs, fakeFileResolver{file: file}, nil)

	dl := &Download{ID: "dl1", ContentID: "c1", MediaFileID: 99, FileSize: 1024, Format: FormatOriginal}
	m, err := b.Build(context.Background(), dl, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if m.Title != "The Movie" || m.Year != 2021 || m.Runtime != 120 {
		t.Fatalf("metadata mismatch: %+v", m)
	}
	if m.PosterThumbhash != "PHASH" || m.BackdropThumbhash != "BHASH" {
		t.Fatalf("thumbhashes not inlined: %+v", m)
	}
	if m.ArtworkURLs.Poster != "/api/v1/downloads/dl1/artwork/poster" {
		t.Fatalf("poster url = %q, want proxy path", m.ArtworkURLs.Poster)
	}
	if m.ArtworkURLs.Backdrop != "/api/v1/downloads/dl1/artwork/backdrop" {
		t.Fatalf("backdrop url = %q, want proxy path", m.ArtworkURLs.Backdrop)
	}
	if m.ArtworkURLs.Logo != "" {
		t.Fatalf("logo url = %q, want empty (no LogoURL)", m.ArtworkURLs.Logo)
	}
	if m.Container != "mkv" || m.CodecVideo != "h264" || m.Resolution != "1080p" || m.Duration != 7200 {
		t.Fatalf("playback metadata mismatch: %+v", m)
	}
	if m.Intro == nil || m.Intro.End != 60 {
		t.Fatalf("intro marker = %+v", m.Intro)
	}
	if len(m.Chapters) != 1 || m.Chapters[0].ThumbnailThumbhash != "CHASH" {
		t.Fatalf("chapters = %+v", m.Chapters)
	}
	if len(m.Subtitles) != 2 {
		t.Fatalf("subtitles = %+v, want 2", m.Subtitles)
	}
	if m.Subtitles[0].FetchURL != "/api/v1/downloads/dl1/subtitles/external:0" || !m.Subtitles[0].External {
		t.Fatalf("external subtitle = %+v", m.Subtitles[0])
	}
	if m.Subtitles[1].FetchURL != "/api/v1/downloads/dl1/subtitles/downloaded:7" || m.Subtitles[1].External {
		t.Fatalf("downloaded subtitle = %+v", m.Subtitles[1])
	}
	if m.StableIdentity.ProviderIDs["imdb"] != "tt123" || m.StableIdentity.ProviderIDs["tmdb"] != "456" {
		t.Fatalf("stable identity = %+v", m.StableIdentity)
	}
	if m.ManifestVersion != manifestVersion {
		t.Fatalf("manifest version = %d, want %d", m.ManifestVersion, manifestVersion)
	}

	// The whole point of the manifest: a stored copy must contain NO presigned
	// URL. Serialize and assert the upstream signature/host never leaks.
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range [][]byte{[]byte("sig=SECRET"), []byte("s3.example.com")} {
		if bytes.Contains(encoded, leak) {
			t.Fatalf("manifest leaks presigned URL fragment %q: %s", leak, encoded)
		}
	}
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
