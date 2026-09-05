package jellycompat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/go-chi/chi/v5"
)

//nolint:misspell // ASS uses the literal Dialogue record type.
func TestAttachmentServesActualFontAndRejectsWrongRoute(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required for attachment extraction")
	}
	fontPath := filepath.Join("..", "..", "web", "public", "vendor", "pdfjs", "standard_fonts", "LiberationSans-Regular.ttf")
	font, err := os.ReadFile(fontPath)
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(t.TempDir(), "attached.mkv")
	assPath := filepath.Join(filepath.Dir(mediaPath), "movie.ass")
	if err := os.WriteFile(assPath, []byte("[Script Info]\nScriptType: v4.00+\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\nStyle: Default,Liberation Sans,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:00.00,0:00:00.10,Default,,0,0,0,,Hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=size=16x16:duration=0.1", "-i", assPath, "-map", "0:v", "-map", "1:s", "-c:s", "ass", "-c:v", "ffv1", "-attach", fontPath, "-metadata:s:t", "mimetype=application/x-truetype-font", mediaPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, output)
	}
	// Negotiate the fixture through the public handler, then follow its discovered URL.
	codec := NewResourceIDCodec()
	itemID := codec.EncodeStringID(EncodedIDItem, "movie-1")
	version := catalog.FileVersion{FileID: 42, FilePath: mediaPath, Container: "mkv", SubtitleTracks: []catalog.VersionSubtitleTrack{{Index: 1, Codec: "ass"}}}
	discovery := &PlaybackHandler{codec: codec, deviceProfiles: NewDeviceProfileStore(time.Hour, nil), playbackStore: NewPlaybackSessionStore(time.Hour, nil), fileResolver: testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: mediaPath}}, FFmpegPath: ffmpeg, content: &stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Versions: []catalog.FileVersion{version}}}}
	sessions := NewSessionStore(time.Hour, nil)
	if err := sessions.Put(Session{Token: "discovery-token", StreamAppUserID: 1}); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.With(PlaybackSessionAuth(sessions, discovery.playbackStore, nil)).Post("/Items/{id}/PlaybackInfo", discovery.HandlePlaybackInfo)
	router.With(PlaybackSessionAuth(sessions, discovery.playbackStore, nil)).Get("/Videos/{id}/{routeMediaSourceId}/Attachments/{routeIndex}", discovery.HandleAttachment)
	request := httptest.NewRequest("POST", "/Items/"+itemID+"/PlaybackInfo", strings.NewReader(`{}`))
	request.Header.Set("X-Emby-Token", "discovery-token")
	negotiated := httptest.NewRecorder()
	router.ServeHTTP(negotiated, request)
	if negotiated.Code != 200 {
		t.Fatalf("negotiation %d: %s", negotiated.Code, negotiated.Body.String())
	}
	var response playbackInfoResponseDTO
	if err := json.Unmarshal(negotiated.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.MediaSources) != 1 || len(response.MediaSources[0].MediaAttachments) != 1 {
		t.Fatalf("missing attachment: %s", negotiated.Body.String())
	}
	attachment := response.MediaSources[0].MediaAttachments[0]
	if attachment["Index"] != float64(2) || attachment["FileName"] != "LiberationSans-Regular.ttf" || attachment["Codec"] != "ttf" {
		t.Fatalf("metadata: %+v", attachment)
	}
	delivered := httptest.NewRecorder()
	router.ServeHTTP(delivered, httptest.NewRequest("GET", attachment["DeliveryUrl"].(string), nil))
	if delivered.Code != 200 || !bytes.Equal(delivered.Body.Bytes(), font) {
		t.Fatalf("discovered font: %d %d bytes", delivered.Code, delivered.Body.Len())
	}

	source := PlaybackMediaSource{ID: "source-42", FileID: 42}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1", ItemID: "movie-1", MediaSources: []PlaybackMediaSource{source}})
	h := &PlaybackHandler{playbackStore: store, fileResolver: testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: mediaPath}}, FFmpegPath: ffmpeg, content: &stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Versions: []catalog.FileVersion{{FileID: 42}}}}}
	for _, tc := range []struct {
		item, source, index string
		status              int
	}{{"item-1", "source-42", "2", 200}, {"wrong-item", "source-42", "2", 404}, {"item-1", "wrong-source", "2", 404}, {"item-1", "source-42", "0", 404}} {
		route := chi.NewRouteContext()
		route.URLParams.Add("id", tc.item)
		route.URLParams.Add("routeMediaSourceId", tc.source)
		route.URLParams.Add("routeIndex", tc.index)
		r := httptest.NewRequest("GET", "/attachment?PlaySessionId=play-1", nil)
		ctx := context.WithValue(t.Context(), chi.RouteCtxKey, route)
		ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1"})
		rr := httptest.NewRecorder()
		h.HandleAttachment(rr, r.WithContext(ctx))
		if rr.Code != tc.status || (tc.status == 200 && !bytes.Equal(rr.Body.Bytes(), font)) {
			t.Fatalf("%+v: status=%d bytes=%d", tc, rr.Code, rr.Body.Len())
		}
	}
}
