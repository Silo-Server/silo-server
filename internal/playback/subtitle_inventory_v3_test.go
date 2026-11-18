package playback

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestScopeSubtitleInventoryV3PreservesPinnedIdentity(t *testing.T) {
	file := &models.MediaFile{ID: 42,
		ExternalSubtitles: []models.ExternalSubtitle{{Path: "/media/selected.srt", Format: "srt"}},
		SubtitleTracks:    []models.SubtitleTrack{{Index: 4, Codec: "ass"}},
	}
	inventory := SubtitleInventoryV3("original-session", file, nil)
	wantKey := ExternalSubtitlePathKeyV3(file.ExternalSubtitles[0].Path)
	if !strings.Contains(inventory[0].URL, "external_subtitle_key="+wantKey) || strings.Contains(inventory[0].URL, "selected.srt") {
		t.Fatalf("external URL must carry an opaque path identity: %q", inventory[0].URL)
	}
	// Restoring a frozen inventory must not rebind its pins after a scan.
	file.ExternalSubtitles = append([]models.ExternalSubtitle{{Path: "/media/new.srt", Format: "srt"}}, file.ExternalSubtitles...)
	scoped := ScopeSubtitleInventoryV3("new-session", file, inventory, nil)
	if !strings.Contains(scoped[0].URL, "external_subtitle_key="+wantKey) ||
		!strings.Contains(scoped[1].URL, "embedded_stream_index=4") ||
		!strings.Contains(scoped[1].FontBundleURL, "embedded_stream_index=4") {
		t.Fatalf("rescoping changed subtitle identities: %#v", scoped)
	}
}

func TestScopeSubtitleInventoryV3_EncodesEmptyInventoryAsArray(t *testing.T) {
	items := ScopeSubtitleInventoryV3("sess-empty", &models.MediaFile{ID: 4}, []SubtitleInventoryItemV3{}, nil)
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

func TestScopeSubtitleInventoryV3PublishesOriginalSubRipToOptedInClients(t *testing.T) {
	file := &models.MediaFile{
		ID:                21,
		ExternalSubtitles: []models.ExternalSubtitle{{Path: "/media/movie.ar.srt", Language: "ar", Format: "srt"}},
		SubtitleTracks:    []models.SubtitleTrack{{Index: 3, Codec: "subrip"}},
	}
	inventory := BuildSubtitleInventoryV3(file, []SubtitleInventoryEntryV3{{CombinedIndex: 2, Codec: "srt", Source: SubtitleSourceDownloadedV3, DownloadedSubtitleID: 9}})

	opted := ScopeSubtitleInventoryV3("sess", file, inventory, []string{FeatureSubripSidecarV3})
	original := SubtitleOriginalParamV3 + "=1"
	if !strings.Contains(opted[0].URL, "/subtitles/0.srt?") || !strings.Contains(opted[0].URL, original) ||
		!strings.Contains(opted[1].URL, "/subtitles/1.vtt?") || strings.Contains(opted[1].URL, original) ||
		!strings.Contains(opted[2].URL, "/subtitles/2.srt?") || !strings.Contains(opted[2].URL, original) ||
		!strings.Contains(opted[2].URL, DownloadedSubtitleIDParamV3+"=9") {
		t.Fatalf("opted-in inventory URLs: %q, %q, %q", opted[0].URL, opted[1].URL, opted[2].URL)
	}
	legacy := ScopeSubtitleInventoryV3("sess", file, inventory, nil)
	for _, item := range legacy {
		if !strings.Contains(item.URL, ".vtt?") || strings.Contains(item.URL, original) {
			t.Fatalf("a client without subrip_sidecar_v1 must keep WebVTT URLs: %q", item.URL)
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
