package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
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

func TestBuildSubtitleURLs_IncludesAllBitmapTracks(t *testing.T) {
	file := &models.MediaFile{
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "en", Codec: "subrip"},
			{Index: 1, Language: "en", Codec: "hdmv_pgs_subtitle"},
			{Index: 2, Language: "fr", Codec: "dvd_subtitle"},
			{Index: 3, Language: "de", Codec: "dvb_subtitle"},
		},
	}

	urls := buildSubtitleURLs("sess-1", file, nil)

	// Every bitmap track is deliverable now that server-side burn-in supports
	// bitmap codecs; PGS additionally streams as .sup for client rendering.
	if len(urls) != 4 {
		t.Fatalf("expected 4 subtitle URLs (text + PGS + DVD + DVB), got %d: %+v", len(urls), urls)
	}

	srt := urls[0]
	if srt.Codec != "subrip" || srt.URL != "/stream/sess-1/subtitles/0.vtt" {
		t.Errorf("unexpected text track entry: %+v", srt)
	}

	pgs := urls[1]
	if pgs.Codec != "hdmv_pgs_subtitle" {
		t.Errorf("expected PGS track to be included, got %+v", pgs)
	}
	if pgs.URL != "/stream/sess-1/subtitles/1.sup" {
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

func TestBuildSubtitleURLs_PGSIndexAccountsForExternalOffset(t *testing.T) {
	file := &models.MediaFile{
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/media/movie.en.srt", Language: "en", Format: "srt"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "en", Codec: "pgs"},
		},
	}

	urls := buildSubtitleURLs("sess-2", file, nil)

	if len(urls) != 2 {
		t.Fatalf("expected 2 subtitle URLs, got %d: %+v", len(urls), urls)
	}
	pgs := urls[1]
	if pgs.Index != 1 || pgs.URL != "/stream/sess-2/subtitles/1.sup" {
		t.Errorf("PGS track index should include the external offset, got %+v", pgs)
	}
}
