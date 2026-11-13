package apiv2

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// The delivery routes a Cast receiver fetches accept the plan's stream
// reference alone; every other operation, subtitle fonts included, still
// needs the account credential.
func TestPlaybackDeliveryAcceptsStreamReferenceAlone(t *testing.T) {
	const secret = "stream-reference-secret"
	deps, _ := catalogDeps(t)
	deps.Auth.SetStreamReferenceVerifier(func(reference string) (*streamtoken.Claims, error) {
		return streamtoken.Verify(reference, secret)
	}, func(_ context.Context, sessionID string) (apimw.StreamReferenceAttempt, error) {
		// s1 is the member's active login session in the shared test fakes.
		return apimw.StreamReferenceAttempt{UserID: 1, ProfileID: "p-owner", LoginSessionID: "s1", Live: sessionID == deliveryTestSession}, nil
	})
	var seenUser int
	var seenProfile string
	record := func(w http.ResponseWriter, r *http.Request, contentType string) {
		seenUser, seenProfile = apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context())
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
	}
	fontCalls := 0
	deps.PlaybackMedia = &PlaybackMediaHandlers{
		Original: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { record(w, r, "video/mp4") }),
		Manifest: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { record(w, r, "application/vnd.apple.mpegurl") }),
		Segment:  http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { record(w, r, "video/mp4") }),
		Subtitle: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { record(w, r, "text/vtt") }),
		SubtitleFonts: fakeSubtitleFontService(func(context.Context, handlers.SubtitleFontRequest) ([]playback.SubtitleFontBundleItem, error) {
			fontCalls++
			return nil, nil
		}),
	}
	h := newTestHandler(t, deps)
	reference, err := streamtoken.Sign(streamtoken.Claims{SessionID: deliveryTestSession, PlayMethod: "transcode", UserID: 1, ProfileID: "p-owner"}, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	stream := Prefix + "/stream/" + deliveryTestSession
	transcode := Prefix + "/playback/transcode/" + deliveryTestSession
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, stream},
		{http.MethodHead, stream},
		{http.MethodGet, transcode + "/master.m3u8"},
		{http.MethodGet, transcode + "/segment/seg_00001.m4s"},
		{http.MethodGet, stream + "/subtitles/0.vtt"},
		{http.MethodHead, stream + "/subtitles/0.vtt"},
	} {
		seenUser, seenProfile = 0, ""
		rec := do(t, h, route.method, route.path+"?st="+reference, "", nil)
		if rec.Code != http.StatusOK || seenUser != 1 || seenProfile != "p-owner" {
			t.Fatalf("%s %s: %d user %d profile %q %s", route.method, route.path, rec.Code, seenUser, seenProfile, rec.Body.String())
		}
	}

	requireProblem(t, do(t, h, http.MethodGet, transcode+"/master.m3u8?st=forged", "", nil), TypeInvalidToken)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/playback/transcode/22222222-2222-4222-8222-222222222222/master.m3u8?st="+reference, "", nil), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, stream+"/subtitles/0/fonts?st="+reference, "", nil), TypeAuthenticationRequired)
	// A stopped or expired attempt ends its references.
	ended, err := streamtoken.Sign(streamtoken.Claims{SessionID: "33333333-3333-4333-8333-333333333333", PlayMethod: "transcode", UserID: 1, ProfileID: "p-owner"}, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/playback/transcode/33333333-3333-4333-8333-333333333333/master.m3u8?st="+ended, "", nil), TypePlaybackSessionEnded)
	if fontCalls != 0 {
		t.Fatal("subtitle fonts accepted the stream reference")
	}
}
