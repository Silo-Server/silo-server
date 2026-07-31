package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func TestSubtitleURLExt(t *testing.T) {
	cases := []struct {
		codec string
		want  string
	}{
		{"ass", ".ass"},
		{"ssa", ".ass"},
		{"pgs", ".sup"},
		{"hdmv_pgs_subtitle", ".sup"},
		{"subrip", ".vtt"},
		{"srt", ".vtt"},
		{"", ".vtt"},
	}
	for _, tc := range cases {
		if got := subtitleURLExt(tc.codec); got != tc.want {
			t.Errorf("subtitleURLExt(%q) = %q, want %q", tc.codec, got, tc.want)
		}
	}
}

func TestBuildSubtitleURLsBindsDownloadedSubtitleIdentity(t *testing.T) {
	file := &models.MediaFile{ID: 42}
	urls := buildSubtitleURLs("sess-downloaded", file, []subtitles.DownloadedSubtitle{{ID: 71, MediaFileID: 42, Format: subtitles.FormatVTT}}, true)
	if len(urls) != 1 {
		t.Fatalf("downloaded subtitle URLs = %#v", urls)
	}
	if want := "/stream/sess-downloaded/subtitles/0.vtt?file_id=42&downloaded_subtitle_id=71"; urls[0].URL != want {
		t.Fatalf("downloaded subtitle URL = %q, want %q", urls[0].URL, want)
	}
}

func TestBuildSubtitleURLs_IncludesAllBitmapTracksForBurnInClients(t *testing.T) {
	file := &models.MediaFile{
		ID: 42,
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "en", Codec: "subrip"},
			{Index: 1, Language: "en", Codec: "hdmv_pgs_subtitle"},
			{Index: 2, Language: "fr", Codec: "dvd_subtitle"},
			{Index: 3, Language: "de", Codec: "dvb_subtitle"},
		},
	}

	urls := buildSubtitleURLs("sess-1", file, nil, true)

	// Every bitmap track is deliverable now that server-side burn-in supports
	// bitmap codecs; PGS additionally streams as .sup for client rendering.
	if len(urls) != 4 {
		t.Fatalf("expected 4 subtitle URLs (text + PGS + DVD + DVB), got %d: %+v", len(urls), urls)
	}

	srt := urls[0]
	if srt.MediaFileID != file.ID || srt.Codec != "subrip" || srt.URL != "/stream/sess-1/subtitles/0.vtt?file_id=42" {
		t.Errorf("unexpected text track entry: %+v", srt)
	}

	pgs := urls[1]
	if pgs.MediaFileID != file.ID || pgs.Codec != "hdmv_pgs_subtitle" {
		t.Errorf("expected PGS track to be included, got %+v", pgs)
	}
	if pgs.URL != "/stream/sess-1/subtitles/1.sup?file_id=42" {
		t.Errorf("PGS track should get a .sup URL, got %q", pgs.URL)
	}
	if pgs.FontBundleURL != "" {
		t.Errorf("PGS track must not advertise a font bundle, got %q", pgs.FontBundleURL)
	}

	if dvd := urls[2]; dvd.Codec != "dvd_subtitle" || dvd.Index != 2 {
		t.Errorf("expected DVD bitmap track to be listed for burn-in selection, got %+v", dvd)
	}
	if dvb := urls[3]; dvb.Codec != "dvb_subtitle" || dvb.Index != 3 {
		t.Errorf("expected DVB bitmap track to be listed for burn-in selection, got %+v", dvb)
	}
}

func TestBuildSubtitleURLs_HidesBurnInOnlyTracksByDefault(t *testing.T) {
	file := &models.MediaFile{
		ID: 43,
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "en", Codec: "subrip"},
			{Index: 1, Language: "en", Codec: "hdmv_pgs_subtitle"},
			{Index: 2, Language: "fr", Codec: "dvd_subtitle"},
			{Index: 3, Language: "de", Codec: "dvb_subtitle"},
		},
	}

	urls := buildSubtitleURLs("sess-native", file, nil, false)
	if len(urls) != 2 {
		t.Fatalf("expected text and PGS only, got %d: %+v", len(urls), urls)
	}
	if urls[0].Codec != "subrip" || urls[1].Codec != "hdmv_pgs_subtitle" {
		t.Fatalf("unexpected default subtitle URLs: %+v", urls)
	}
}

func TestBuildSubtitleURLs_PGSIndexAccountsForExternalOffset(t *testing.T) {
	file := &models.MediaFile{
		ID: 44,
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/media/movie.en.srt", Language: "en", Format: "srt"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "en", Codec: "pgs"},
		},
	}

	urls := buildSubtitleURLs("sess-2", file, nil, false)

	if len(urls) != 2 {
		t.Fatalf("expected 2 subtitle URLs, got %d: %+v", len(urls), urls)
	}
	pgs := urls[1]
	if pgs.Index != 1 || pgs.URL != "/stream/sess-2/subtitles/1.sup?file_id=44" {
		t.Errorf("PGS track index should include the external offset, got %+v", pgs)
	}
}

// A downloaded track's ordinal follows every embedded track, including the
// bitmap ones that buildSubtitleURLs hides from native clients. This is the
// case the old client-side `max(visible index)+1` reconstruction got wrong.
func TestBuildSubtitleURLs_DownloadedOffsetCountsHiddenBitmapTracks(t *testing.T) {
	file := &models.MediaFile{
		ID: 45,
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/media/movie.en.srt", Language: "en", Format: "srt"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "ja", Codec: "hdmv_pgs_subtitle"},
			// Last embedded track is burn-in only, so it never appears in the
			// native listing — but it still consumes ordinal 2.
			{Index: 1, Language: "de", Codec: "dvd_subtitle"},
		},
	}
	downloaded := []subtitles.DownloadedSubtitle{
		{ID: 1, MediaFileID: file.ID, Language: "es", Format: subtitles.SubtitleFormat("srt"), Provider: "opensubtitles", ReleaseName: "Movie.2020"},
	}

	urls := buildSubtitleURLs("sess-3", file, downloaded, false)

	if len(urls) != 3 {
		t.Fatalf("expected external + PGS + downloaded, got %d: %+v", len(urls), urls)
	}
	got := urls[len(urls)-1]
	if got.Index != 3 {
		t.Errorf("downloaded track index = %d, want 3 (1 external + 2 embedded); a hidden bitmap track must still consume its ordinal", got.Index)
	}
	if got.URL != "/stream/sess-3/subtitles/3.vtt?file_id=45" {
		t.Errorf("downloaded track URL = %q, want the ordinal-3 sidecar URL", got.URL)
	}
	if got.Source != playback.SubtitleSourceDownloadedV3 {
		t.Errorf("downloaded track source = %q, want %q", got.Source, playback.SubtitleSourceDownloadedV3)
	}
	// The listing is intentionally sparse for native clients; the ordinals it
	// does publish must still be the plan's ordinals.
	if urls[0].Index != 0 || urls[1].Index != 1 {
		t.Errorf("visible track ordinals drifted: %+v", urls)
	}
}

// buildSubtitleURLs projects the same inventory the v3 plan publishes, so every
// entry it emits must appear at the same ordinal in the plan's dense list.
func TestBuildSubtitleURLs_MatchesPlanInventoryOrdinals(t *testing.T) {
	file := &models.MediaFile{
		ID: 46,
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/media/movie.fr.srt", Language: "fr", Format: "srt"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "en", Codec: "subrip"},
			{Index: 1, Language: "de", Codec: "dvb_subtitle"},
			{Index: 2, Language: "ja", Codec: "hdmv_pgs_subtitle"},
		},
	}
	downloaded := []subtitles.DownloadedSubtitle{
		{ID: 1, MediaFileID: file.ID, Language: "es", Format: subtitles.SubtitleFormat("srt")},
	}

	inventory := playback.SubtitleInventoryV3("sess-4", file, downloadedSubtitleEntriesV3(file, downloaded))
	if len(inventory) != 5 {
		t.Fatalf("expected a dense 5-track inventory, got %d: %+v", len(inventory), inventory)
	}

	for _, url := range buildSubtitleURLs("sess-4", file, downloaded, true) {
		item, ok := playback.SubtitleInventoryItemAtV3(inventory, url.Index)
		if !ok {
			t.Fatalf("subtitle_urls advertises ordinal %d, which the plan inventory does not contain", url.Index)
		}
		if item.Codec != url.Codec || item.Language != url.Language {
			t.Errorf("ordinal %d disagrees: listing %+v, inventory %+v", url.Index, url, item)
		}
	}
}

func TestResolveBurnInSubtitle_RemapsAlternateVersionByStableTraits(t *testing.T) {
	requested := &models.MediaFile{
		ID: 1,
		SubtitleTracks: []models.SubtitleTrack{
			{Language: "en", Codec: "subrip", Title: "English"},
			{Language: "ja", Codec: "hdmv_pgs_subtitle", Title: "Japanese", Forced: true},
		},
	}
	effective := &models.MediaFile{
		ID: 2,
		SubtitleTracks: []models.SubtitleTrack{
			{Language: "ja", Codec: "hdmv_pgs_subtitle", Title: "Japanese", Forced: true},
			{Language: "en", Codec: "subrip", Title: "English"},
		},
	}

	index, codec, ok := resolveBurnInSubtitle(requested, effective, 1)
	if !ok || index != 0 || codec != "hdmv_pgs_subtitle" {
		t.Fatalf("resolveBurnInSubtitle() = (%d, %q, %v), want (0, hdmv_pgs_subtitle, true)", index, codec, ok)
	}
}

func TestResolveBurnInSubtitle_RejectsMissingAlternateTrack(t *testing.T) {
	requested := &models.MediaFile{
		ID: 1,
		SubtitleTracks: []models.SubtitleTrack{
			{Language: "ja", Codec: "hdmv_pgs_subtitle", Title: "Japanese"},
		},
	}
	effective := &models.MediaFile{ID: 2}

	if index, codec, ok := resolveBurnInSubtitle(requested, effective, 0); ok || index != -1 || codec != "" {
		t.Fatalf("resolveBurnInSubtitle() = (%d, %q, %v), want unavailable", index, codec, ok)
	}
}
