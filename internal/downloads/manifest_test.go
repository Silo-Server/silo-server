package downloads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
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
	if m.ArtworkURLs.Poster != "/api/v2/downloads/dl1/artwork/poster" {
		t.Fatalf("poster url = %q, want proxy path", m.ArtworkURLs.Poster)
	}
	if m.ArtworkURLs.Backdrop != "/api/v2/downloads/dl1/artwork/backdrop" {
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
	if m.Subtitles[0].FetchURL != "/api/v2/downloads/dl1/subtitles/external:0" || !m.Subtitles[0].External {
		t.Fatalf("external subtitle = %+v", m.Subtitles[0])
	}
	if m.Subtitles[1].FetchURL != "/api/v2/downloads/dl1/subtitles/downloaded:7" || m.Subtitles[1].External {
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

func preparedManifestFixture(artifact *Artifact) (*ManifestBuilder, *Download) {
	audio := []models.AudioTrack{
		{Codec: "truehd", Channels: 8, Language: "en", Layout: "7.1", Title: "TrueHD 7.1", Default: true},
		{Codec: "ac3", Channels: 2, Language: "ja", EmbeddedTitle: "Commentary", Title: "Commentary"},
	}
	selected := 1
	detail := &catalog.ItemDetail{Type: "movie", Title: "The Movie", Versions: []catalog.FileVersion{{
		FileID: 99, Container: "mkv", CodecVideo: "hevc", CodecAudio: "truehd", Resolution: "2160p",
		AudioTracks: audio, EffectiveAudioTrackIndex: &selected,
	}}}
	file := &models.MediaFile{
		ID: 99, CodecAudio: "truehd", AudioTracks: audio,
		ExternalSubtitles: []models.ExternalSubtitle{{Path: "/media/sub.en.srt", Language: "en", Format: "srt"}},
		SubtitleTracks: []models.SubtitleTrack{
			{Codec: "subrip", Language: "en"},
			{Codec: "hdmv_pgs_subtitle", Language: "fr", Forced: true},
			{Codec: "ass", Language: "ja", EmbeddedTitle: "Signs & Songs", HearingImpaired: true},
		},
	}
	lookup := func(context.Context, string) (*Artifact, error) { return artifact, nil }
	b := NewManifestBuilder(fakeManifestSource{detail: detail}, nil, fakeFileResolver{file: file}, lookup)
	return b, &Download{ID: "dl1", ContentID: "c1", MediaFileID: 99, Format: FormatTranscode, ArtifactID: artifact.ID}
}

// TestManifestDescribesMultiTrackArtifact verifies a multi-track prepared file
// is described by output position, with encoded tracks reporting their AAC
// layout and PGS offered as a .sup sidecar the MP4 cannot store.
func TestManifestDescribesMultiTrackArtifact(t *testing.T) {
	b, dl := preparedManifestFixture(&Artifact{
		ID: "a1", Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", Resolution: "1080p",
		AudioTrackIndex: -1, TrackRecipeVersion: playback.PreparedTracksRecipeVersion,
	})
	m, err := b.Build(context.Background(), dl, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// The viewer's catalog selection (the commentary track) survives because
	// every source track is present at its source position.
	if len(m.AudioTracks) != 2 || m.SelectedAudioTrackIndex == nil || *m.SelectedAudioTrackIndex != 1 {
		t.Fatalf("audio tracks = %+v selected %v, want both tracks with output 1 selected", m.AudioTracks, m.SelectedAudioTrackIndex)
	}
	if !m.AudioTracks[0].Default {
		t.Fatal("source default track lost its default flag")
	}
	for i, track := range m.AudioTracks {
		if track.Index != i || track.Codec != "aac" || track.Channels != 2 || track.Layout != "stereo" {
			t.Fatalf("audio track %d = %+v, want AAC stereo at output %d", i, track, i)
		}
	}
	if m.AudioTracks[1].Language != "ja" || m.AudioTracks[1].Title != "Commentary" || m.AudioTracks[1].Default {
		t.Fatalf("commentary track = %+v", m.AudioTracks[1])
	}
	if len(m.Subtitles) != 3 {
		t.Fatalf("subtitles = %+v, want external sidecar plus PGS and ASS sidecars", m.Subtitles)
	}
	pgs := m.Subtitles[1]
	if pgs.FetchURL != "/api/v2/downloads/dl1/subtitles/embedded:1" || pgs.Format != "sup" || pgs.Language != "fr" || !pgs.Forced || pgs.External {
		t.Fatalf("PGS sidecar = %+v", pgs)
	}
	ass := m.Subtitles[2]
	if ass.FetchURL != "/api/v2/downloads/dl1/subtitles/embedded:2" || ass.Format != "ass" || ass.Language != "ja" || ass.Title != "Signs & Songs" || !ass.HearingImpaired {
		t.Fatalf("ASS sidecar = %+v", ass)
	}
}

// TestManifestDescribesFrozenAudioAfterSourceReprobe verifies a ready
// multi-track file keeps the audio inventory it was prepared with after the
// source is replaced at the same path and re-probed.
func TestManifestDescribesFrozenAudioAfterSourceReprobe(t *testing.T) {
	prepared := []OfflineAudioTrack{
		{Index: 0, Language: "en", Codec: "aac", Channels: 2, Layout: "stereo", Bitrate: 192, Default: true},
		{Index: 1, Language: "ja", Codec: "aac", Channels: 2, Layout: "stereo", Bitrate: 192},
	}
	b, dl := preparedManifestFixture(&Artifact{
		ID: "a1", Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", Resolution: "1080p",
		AudioTrackIndex: -1, TrackRecipeVersion: playback.PreparedTracksRecipeVersion,
		PreparedAudioTracks: prepared,
	})
	reprobed := []models.AudioTrack{
		{Codec: "ac3", Channels: 6, Language: "ja", Default: true},
		{Codec: "truehd", Channels: 8, Language: "en"},
		{Codec: "dts", Channels: 6, Language: "fr"},
	}
	for _, selected := range []int{2, 0} {
		b.detail.(fakeManifestSource).detail.Versions[0].AudioTracks = reprobed
		b.detail.(fakeManifestSource).detail.Versions[0].EffectiveAudioTrackIndex = &selected
		b.fileRepo.(fakeFileResolver).file.AudioTracks = reprobed
		m, err := b.Build(context.Background(), dl, catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if !reflect.DeepEqual(m.AudioTracks, prepared) {
			t.Fatalf("audio tracks = %+v, want the frozen inventory %+v", m.AudioTracks, prepared)
		}
		// Neither the out-of-range position nor the position now holding a
		// different language describes a delivered track the viewer chose.
		if m.SelectedAudioTrackIndex == nil || *m.SelectedAudioTrackIndex != 0 {
			t.Fatalf("selection %d: selected = %v, want the delivered default 0", selected, m.SelectedAudioTrackIndex)
		}
	}
}

// TestManifestDescribesLegacySingleTrackArtifact keeps already-prepared files
// described as the one audio stream they contain, without PGS sidecars.
func TestManifestDescribesLegacySingleTrackArtifact(t *testing.T) {
	b, dl := preparedManifestFixture(&Artifact{
		ID: "a1", Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", Resolution: "1080p", AudioTrackIndex: -1,
	})
	m, err := b.Build(context.Background(), dl, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(m.AudioTracks) != 1 || m.AudioTracks[0].Codec != "aac" || *m.SelectedAudioTrackIndex != 0 {
		t.Fatalf("legacy audio tracks = %+v", m.AudioTracks)
	}
	if len(m.Subtitles) != 1 || m.Subtitles[0].FetchURL != "/api/v2/downloads/dl1/subtitles/external:0" {
		t.Fatalf("legacy subtitles = %+v, want only the external sidecar", m.Subtitles)
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
	// A failed PGS extract after the shared cache committed its 200 must abort
	// the response rather than end it as a complete track.
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
			t.Fatalf("failed committed extract recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	_ = s.serveEmbeddedSubtitle(httptest.NewRecorder(), req, dl, 1)
}

// itemDetailsByID serves one catalog detail per content id; an unknown id is
// denied like an out-of-scope item.
type itemDetailsByID map[string]*catalog.ItemDetail

func (f itemDetailsByID) GetItemDetail(_ context.Context, id string, _ catalog.AccessFilter) (*catalog.ItemDetail, error) {
	if detail, ok := f[id]; ok {
		return detail, nil
	}
	return nil, catalog.ErrItemNotFound
}

func TestManifestNamesTheSeriesPosterForEpisodes(t *testing.T) {
	season, episode := 1, 2
	details := itemDetailsByID{
		"ep1": {
			Type: "episode", Title: "Pilot", SeriesID: "show", SeasonNumber: &season, EpisodeNumber: &episode,
			PosterURL: "https://s3.example.com/still.jpg?sig=SECRET", PosterThumbhash: "STILL",
		},
		"show": {Type: "series", Title: "Show", PosterURL: "https://s3.example.com/series.jpg?sig=SECRET", PosterThumbhash: "SERIES"},
		"movie": {
			Type: "movie", Title: "Film", PosterURL: "https://s3.example.com/poster.jpg?sig=SECRET", PosterThumbhash: "POSTER",
		},
	}
	b := NewManifestBuilder(details, fakeSubtitleSource{}, fakeFileResolver{}, nil)

	m, err := b.Build(context.Background(), &Download{ID: "dl1", ContentID: "show", EpisodeID: "ep1"}, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("Build episode: %v", err)
	}
	if m.PosterThumbhash != "STILL" || m.ArtworkURLs.Poster != "/api/v2/downloads/dl1/artwork/poster" {
		t.Fatalf("episode poster = %q %q, want the still", m.PosterThumbhash, m.ArtworkURLs.Poster)
	}
	if m.SeriesPosterThumbhash != "SERIES" || m.SeriesPosterURL != "/api/v2/downloads/dl1/artwork/series_poster" {
		t.Fatalf("series poster = %q %q", m.SeriesPosterThumbhash, m.SeriesPosterURL)
	}
	// The frozen v1 manifest serializes this struct directly and must not grow.
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("series_poster")) {
		t.Fatalf("v1 manifest carries the series poster: %s", raw)
	}

	m, err = b.Build(context.Background(), &Download{ID: "dl2", ContentID: "movie"}, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("Build movie: %v", err)
	}
	if m.SeriesPosterThumbhash != "" || m.SeriesPosterURL != "" {
		t.Fatalf("movie names a series poster: %q %q", m.SeriesPosterThumbhash, m.SeriesPosterURL)
	}

	// A series without a poster keeps its thumbhash but advertises no image.
	details["show"] = &catalog.ItemDetail{Type: "series", PosterThumbhash: "SERIES"}
	m, err = b.Build(context.Background(), &Download{ID: "dl1", ContentID: "show", EpisodeID: "ep1"}, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("Build episode without series poster: %v", err)
	}
	if m.SeriesPosterURL != "" {
		t.Fatalf("series poster url = %q, want empty", m.SeriesPosterURL)
	}
}

func TestArtworkImageURLResolvesTheSeriesPoster(t *testing.T) {
	details := itemDetailsByID{
		"ep1":   {Type: "episode", SeriesID: "show", PosterURL: "still", BackdropURL: "backdrop"},
		"show":  {Type: "series", PosterURL: "series"},
		"movie": {Type: "movie", PosterURL: "poster"},
	}
	s := &Service{artworkSource: details}
	episode := &Download{ID: "dl1", ContentID: "show", EpisodeID: "ep1"}
	ctx := context.Background()

	for kind, want := range map[string]string{"poster": "still", "backdrop": "backdrop", "series_poster": "series"} {
		if got, err := s.artworkImageURL(ctx, episode, kind, catalog.AccessFilter{}); err != nil || got != want {
			t.Errorf("episode %s = %q, %v; want %q", kind, got, err, want)
		}
	}
	if _, err := s.artworkImageURL(ctx, &Download{ID: "dl2", ContentID: "movie"}, "series_poster", catalog.AccessFilter{}); !errors.Is(err, ErrAssetNotFound) {
		t.Errorf("movie series_poster err = %v, want ErrAssetNotFound", err)
	}
	if _, err := s.artworkImageURL(ctx, episode, "logo", catalog.AccessFilter{}); !errors.Is(err, ErrAssetNotFound) {
		t.Errorf("missing logo err = %v, want ErrAssetNotFound", err)
	}

	// A profile that can no longer see the series gets the not-found answer.
	delete(details, "show")
	if _, err := s.artworkImageURL(ctx, episode, "series_poster", catalog.AccessFilter{}); !errors.Is(err, catalog.ErrItemNotFound) {
		t.Errorf("hidden series err = %v, want ErrItemNotFound", err)
	}
}
