package playback

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestBuildSubtitleInventoryV3_OrdinalsAreDenseAcrossAllThreeRanges(t *testing.T) {
	file := &models.MediaFile{
		ID: 42,
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/media/movie.en.srt", Language: "en", Format: "srt"},
			{Path: "/media/movie.fr.srt", Language: "fr", Format: "srt"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "en", Codec: "subrip"},
			{Index: 1, Language: "ja", Codec: "hdmv_pgs_subtitle"},
			// A bitmap track with no sidecar shape. It is the reason this test
			// exists: it must still occupy its ordinal.
			{Index: 2, Language: "de", Codec: "dvd_subtitle"},
		},
	}
	additional := []SubtitleInventoryEntryV3{
		{CombinedIndex: 5, Codec: "srt", Source: SubtitleSourceDownloadedV3, Language: "es"},
	}

	items := BuildSubtitleInventoryV3(file, additional)

	if len(items) != 6 {
		t.Fatalf("expected 6 inventory items (2 external + 3 embedded + 1 downloaded), got %d: %+v", len(items), items)
	}
	for i, item := range items {
		if item.CombinedIndex != i {
			t.Errorf("item %d has combined_index %d; the ordinal space must be dense", i, item.CombinedIndex)
		}
		if want := TrackIDV3(file.ID, "subtitle", i); item.TrackID != want {
			t.Errorf("item %d track_id = %q, want %q", i, item.TrackID, want)
		}
	}

	wantSources := []string{
		SubtitleSourceExternalV3, SubtitleSourceExternalV3,
		SubtitleSourceEmbeddedV3, SubtitleSourceEmbeddedV3, SubtitleSourceEmbeddedV3,
		SubtitleSourceDownloadedV3,
	}
	for i, want := range wantSources {
		if items[i].Source != want {
			t.Errorf("item %d source = %q, want %q", i, items[i].Source, want)
		}
	}

	// The downloaded track's ordinal must follow every embedded track,
	// including the burn-in-only one.
	if got := items[5]; got.CombinedIndex != 5 || got.Language != "es" {
		t.Errorf("downloaded track landed at the wrong ordinal: %+v", got)
	}
}

func TestBuildSubtitleInventoryV3_ClassifiesDelivery(t *testing.T) {
	file := &models.MediaFile{
		ID: 7,
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/media/movie.en.srt", Language: "en", Format: "srt"},
			// An external bitmap file has no sidecar route either.
			{Path: "/media/movie.en.sup", Language: "en", Format: "pgs"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Codec: "ass"},
			{Index: 1, Codec: "hdmv_pgs_subtitle"},
			{Index: 2, Codec: "dvd_subtitle"},
			{Index: 3, Codec: "dvb_subtitle"},
		},
	}

	items := BuildSubtitleInventoryV3(file, nil)

	want := []string{
		SubtitleDeliverySidecarV3,    // external srt
		SubtitleDeliveryBurnInOnlyV3, // external pgs: no extraction route
		SubtitleDeliverySidecarV3,    // embedded ass
		SubtitleDeliverySidecarV3,    // embedded pgs extracts to .sup
		SubtitleDeliveryBurnInOnlyV3, // embedded dvd
		SubtitleDeliveryBurnInOnlyV3, // embedded dvb
	}
	if len(items) != len(want) {
		t.Fatalf("expected %d items, got %d", len(want), len(items))
	}
	for i, expected := range want {
		if items[i].Delivery != expected {
			t.Errorf("item %d (%s/%s) delivery = %q, want %q", i, items[i].Source, items[i].Codec, items[i].Delivery, expected)
		}
	}
}

func TestSubtitleInventoryV3_AttachesURLsOnlyToSidecarTracks(t *testing.T) {
	file := &models.MediaFile{
		ID: 44,
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Codec: "subrip"},
			{Index: 1, Codec: "ass"},
			{Index: 2, Codec: "hdmv_pgs_subtitle"},
			{Index: 3, Codec: "dvd_subtitle"},
		},
	}

	items := SubtitleInventoryV3("sess-1", file, nil)

	cases := []struct {
		index         int
		url           string
		fontBundleURL string
	}{
		{0, "/stream/sess-1/subtitles/0.vtt?file_id=44", ""},
		{1, "/stream/sess-1/subtitles/1.ass?file_id=44", "/stream/sess-1/subtitles/1/fonts?file_id=44"},
		{2, "/stream/sess-1/subtitles/2.sup?file_id=44", ""},
		{3, "", ""},
	}
	for _, tc := range cases {
		got := items[tc.index]
		if got.URL != tc.url {
			t.Errorf("item %d url = %q, want %q", tc.index, got.URL, tc.url)
		}
		if got.FontBundleURL != tc.fontBundleURL {
			t.Errorf("item %d font_bundle_url = %q, want %q", tc.index, got.FontBundleURL, tc.fontBundleURL)
		}
	}
}

func TestSubtitleInventoryV3_OmitsURLsWithoutASession(t *testing.T) {
	file := &models.MediaFile{ID: 3, SubtitleTracks: []models.SubtitleTrack{{Index: 0, Codec: "subrip"}}}

	items := SubtitleInventoryV3("", file, nil)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].URL != "" {
		t.Errorf("expected no URL without a session, got %q", items[0].URL)
	}
}

func TestScopeSubtitleInventoryV3_EncodesEmptyInventoryAsArray(t *testing.T) {
	items := ScopeSubtitleInventoryV3("sess-empty", &models.MediaFile{ID: 4}, []SubtitleInventoryItemV3{})
	if items == nil {
		t.Fatal("expected a non-nil empty inventory")
	}

	payload, err := json.Marshal(SubtitleDecisionV3{Mode: SubtitleOffV3, Inventory: items})
	if err != nil {
		t.Fatalf("marshal subtitle decision: %v", err)
	}
	if got := string(payload); !strings.Contains(got, `"inventory":[]`) {
		t.Fatalf("empty inventory encoded as %s, want inventory:[]", got)
	}
}

func TestSubtitleInventoryItemAtV3(t *testing.T) {
	file := &models.MediaFile{
		ID: 9,
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Codec: "subrip", Language: "en"},
			{Index: 1, Codec: "dvd_subtitle", Language: "de"},
		},
	}
	items := BuildSubtitleInventoryV3(file, nil)

	if item, ok := SubtitleInventoryItemAtV3(items, 1); !ok || item.Language != "de" {
		t.Errorf("SubtitleInventoryItemAtV3(1) = (%+v, %v), want the German bitmap track", item, ok)
	}
	if _, ok := SubtitleInventoryItemAtV3(items, 2); ok {
		t.Error("SubtitleInventoryItemAtV3 must not resolve an ordinal past the end of the inventory")
	}
	if _, ok := SubtitleInventoryItemAtV3(items, -1); ok {
		t.Error("SubtitleInventoryItemAtV3 must not resolve a negative ordinal")
	}
}

func TestBuildSubtitleInventoryV3_NilFile(t *testing.T) {
	if items := BuildSubtitleInventoryV3(nil, nil); items != nil {
		t.Errorf("expected nil inventory for a nil file, got %+v", items)
	}
}

func TestSubtitleURLExtV3(t *testing.T) {
	cases := map[string]string{
		"ass":               ".ass",
		"ssa":               ".ass",
		"pgs":               ".sup",
		"hdmv_pgs_subtitle": ".sup",
		"subrip":            ".vtt",
		"srt":               ".vtt",
		"":                  ".vtt",
	}
	for codec, want := range cases {
		if got := SubtitleURLExtV3(codec); got != want {
			t.Errorf("SubtitleURLExtV3(%q) = %q, want %q", codec, got, want)
		}
	}
}

// The combined ordinal a plan advertises must be the one the selection path
// resolves, or a client echoing an inventory entry addresses a different track
// than the one it picked.
func TestSubtitleEntryAtCombinedIndexV3_AgreesWithPublishedInventory(t *testing.T) {
	file := &models.MediaFile{
		ID: 11,
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/media/movie.en.srt", Language: "en", Format: "srt"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Codec: "dvd_subtitle", Language: "de"},
			{Index: 1, Codec: "hdmv_pgs_subtitle", Language: "ja"},
		},
	}
	additional := []SubtitleInventoryEntryV3{
		{CombinedIndex: 3, Codec: "srt", Source: SubtitleSourceDownloadedV3, Language: "es"},
	}

	for _, item := range BuildSubtitleInventoryV3(file, additional) {
		entry, ok := subtitleEntryAtCombinedIndexV3(file, item.CombinedIndex, additional)
		if !ok {
			t.Fatalf("ordinal %d is published but does not resolve", item.CombinedIndex)
		}
		if entry.Source != item.Source {
			t.Errorf("ordinal %d resolves to source %q, inventory says %q", item.CombinedIndex, entry.Source, item.Source)
		}
		if entry.Codec != normalizeCodecV3(item.Codec) {
			t.Errorf("ordinal %d resolves to codec %q, inventory says %q", item.CombinedIndex, entry.Codec, item.Codec)
		}
	}
}
