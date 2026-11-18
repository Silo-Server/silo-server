package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const deliveryTestSession = "11111111-1111-4111-8111-111111111111"

func TestPlaybackDeliveryV2BytesErrorsAndAuthorization(t *testing.T) {
	deps, _ := catalogDeps(t)
	calls := 0
	deps.PlaybackMedia = &PlaybackMediaHandlers{Original: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("st") != "opaque" {
			t.Error("signed reference changed")
		}
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "fixture.mp4", time.Time{}, strings.NewReader("0123456789"))
	})}
	h := newTestHandler(t, deps)
	path := Prefix + "/stream/" + deliveryTestSession + "?st=opaque"
	full := do(t, h, http.MethodGet, path, "", viewerHeaders())
	if full.Code != 200 || full.Body.String() != "0123456789" {
		t.Fatalf("full bytes: %d %q", full.Code, full.Body.String())
	}
	rangeResponse := do(t, h, http.MethodGet, path, "", with(viewerHeaders(), "Range", "bytes=2-4"))
	if rangeResponse.Code != 206 || rangeResponse.Body.String() != "234" || rangeResponse.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Fatalf("range: %d %s %v", rangeResponse.Code, rangeResponse.Body.String(), rangeResponse.Header())
	}
	head := do(t, h, http.MethodHead, path, "", viewerHeaders())
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "10" {
		t.Fatalf("HEAD: %d %s %v", head.Code, head.Body.String(), head.Header())
	}
	invalidRange := do(t, h, http.MethodGet, path, "", with(viewerHeaders(), "Range", "bytes=30-40"))
	requireProblem(t, invalidRange, TypeRangeNotSatisfiable)
	if invalidRange.Header().Get("Content-Range") != "bytes */10" || invalidRange.Header().Get("Content-Length") != "" {
		t.Fatalf("range problem metadata: %v", invalidRange.Header())
	}
	queryAuth := do(t, h, http.MethodGet, path+"&token="+memberToken, "", nil)
	if queryAuth.Code != 200 || queryAuth.Body.String() != "0123456789" {
		t.Fatalf("media query auth: %d %s", queryAuth.Code, queryAuth.Body.String())
	}
	before := calls
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(bearer(memberToken), "X-Profile-Id", "p-other")), TypeNotFound)
	if calls != before {
		t.Fatal("rejected viewer reached media")
	}
	deps.PlaybackMedia = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, path, "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestPlaybackDeliveryProblemDropsLegacyBodyAndHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, Prefix+"/stream/"+deliveryTestSession, nil)
	writer := &playbackDeliveryWriter{ResponseWriter: w, request: r}
	writer.Header().Set("Content-Length", "10000")
	writer.Header().Set("Location", "PRIVATE_LOCATION")
	writer.Header().Set("Content-Encoding", "gzip")
	http.Error(writer, "PRIVATE_ERROR", http.StatusServiceUnavailable)
	if w.Code != 503 || strings.Contains(w.Body.String(), "PRIVATE") || w.Header().Get("Content-Length") != "" || w.Header().Get("Location") != "" || w.Header().Get("Content-Encoding") != "" {
		t.Fatalf("unsafe error: %d %s %v", w.Code, w.Body.String(), w.Header())
	}
}

// A v1 media handler signals a retryable upstream/dependency failure with 502
// (virtual source resolve/relay, transcode node). The catalog has no 502 type,
// so without the delivery-layer mapping the status default reports
// internal_error/500 and tells the client it hit a server bug. It must surface
// as 503 dependency_unavailable instead.
func TestPlaybackDeliveryMapsUpstreamFailureToDependencyUnavailable(t *testing.T) {
	deps, _ := catalogDeps(t)
	deps.PlaybackMedia = &PlaybackMediaHandlers{Original: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "PRIVATE_UPSTREAM_DETAIL", http.StatusBadGateway)
	})}
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"?st=opaque", "", viewerHeaders())
	requireProblem(t, rec, TypeDependencyUnavailable)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "PRIVATE_UPSTREAM_DETAIL") {
		t.Fatalf("upstream failure problem: %d %s", rec.Code, rec.Body.String())
	}

	// The same mapping covers the writer-level fast path (a local refusal
	// before any streaming work) independently of the route.
	w := httptest.NewRecorder()
	writer := &playbackDeliveryWriter{ResponseWriter: w, request: httptest.NewRequest(http.MethodGet, Prefix+"/stream/"+deliveryTestSession, nil)}
	writer.WriteHeader(http.StatusBadGateway)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("writer fast-path status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

// A remux start failure is a 502 plus the resolve route's Retry-After. The
// adapter must keep that delay on the 503 dependency_unavailable it maps to:
// production saw the delivery 503 with no Retry-After while the manifest and
// segment routes sent one.
func TestPlaybackDeliveryKeepsRetryAfterOnMappedDependencyFailure(t *testing.T) {
	deps, _ := catalogDeps(t)
	deps.PlaybackMedia = &PlaybackMediaHandlers{Original: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "failed to start remux", http.StatusBadGateway)
	})}
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"?st=opaque", "", viewerHeaders())
	requireProblem(t, rec, TypeDependencyUnavailable)
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want the handler's 5", got)
	}
}

// TestPlaybackDeliveryProviderUnavailableProblem proves the code-carrying
// adapter: a v1 media handler that answers 503 with the provider_unavailable
// code surfaces the distinct v2 provider_unavailable problem (retry this
// release) rather than the generic dependency_unavailable, while the same
// writer's 502 mapping is unchanged.
func TestPlaybackDeliveryProviderUnavailableProblem(t *testing.T) {
	deps, _ := catalogDeps(t)
	deps.PlaybackMedia = &PlaybackMediaHandlers{Original: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The v1 handler records the code through writeError's optional seam;
		// simulate it directly since the test writer is the v2 adapter.
		if recorder, ok := w.(interface{ SetPlaybackProblemCode(string) }); ok {
			recorder.SetPlaybackProblemCode("provider_unavailable")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"provider_unavailable","message":"PRIVATE_PROVIDER_DETAIL"}`))
	})}
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"?st=opaque", "", viewerHeaders())
	requireProblem(t, rec, TypeProviderUnavailable)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "PRIVATE_PROVIDER_DETAIL") {
		t.Fatalf("provider outage problem: %d %s", rec.Code, rec.Body.String())
	}
}

// A subtitle sidecar whose live layout could not be verified answers 503 with
// its own machine-readable code, and that code must survive the byte-delivery
// adapter: production logged status=503 error_code=dependency_unavailable for
// it, and the client saw the generic dependency problem instead of the
// retryable subtitle one plus its Retry-After. The v1 body is still not
// forwarded (the problem envelope replaces it), but it is now debug detail
// rather than an ERROR, because the code reached the client and the request log.
func TestPlaybackDeliverySubtitleSourceUnavailableProblem(t *testing.T) {
	logs := captureLogsAt(t, slog.LevelDebug)
	deps, _ := catalogDeps(t)
	deps.PlaybackMedia = &PlaybackMediaHandlers{Original: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if recorder, ok := w.(interface{ SetPlaybackProblemCode(string) }); ok {
			recorder.SetPlaybackProblemCode("subtitle_source_unavailable")
		}
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"subtitle_source_unavailable","message":"Unable to verify the subtitle source; retry"}`))
	})}
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"?st=opaque", "", viewerHeaders())
	requireProblem(t, rec, TypeSubtitleSourceUnavailable)
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want the handler's 5", got)
	}
	// The v1 body is still not forwarded, but with a semantic problem type it is
	// debug detail: the request log already carries the code, and an expected
	// retryable 503 must not add an ERROR line for a discarded body.
	got := logs.String()
	if !strings.Contains(got, `"level":"DEBUG","msg":"apiv2 raw stream error body discarded"`) {
		t.Fatalf("subtitle 503 discard is not debug detail: %s", got)
	}
	if strings.Contains(got, `"level":"ERROR","msg":"apiv2 raw stream error body discarded"`) {
		t.Fatalf("an expected retryable subtitle 503 logged the discard at ERROR: %s", got)
	}
}

// TestPlaybackDeliveryNamesStreamFailureCause pins the cause seam: a v1 stream
// handler that records the underlying failure through SetPlaybackProblemCause
// has it named in the request log and the discarded-body log, while the client
// only sees the generic problem envelope. Without the seam the 502 reaches the
// log as the bare virtual_stream_unavailable code.
func TestPlaybackDeliveryNamesStreamFailureCause(t *testing.T) {
	buf := captureLogs(t)
	deps, _ := catalogDeps(t)
	deps.PlaybackMedia = &PlaybackMediaHandlers{Original: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if recorder, ok := w.(interface{ SetPlaybackProblemCause(error) }); ok {
			recorder.SetPlaybackProblemCause(errors.New("PRIVATE_STREAM_CAUSE"))
		}
		if recorder, ok := w.(interface{ SetPlaybackProblemCode(string) }); ok {
			recorder.SetPlaybackProblemCode("virtual_stream_unavailable")
		}
		http.Error(w, `{"error":"virtual_stream_unavailable","message":"Failed to stream virtual media source"}`, http.StatusBadGateway)
	})}
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"?st=opaque", "", viewerHeaders())
	requireProblem(t, rec, TypeDependencyUnavailable)
	if strings.Contains(rec.Body.String(), "PRIVATE_STREAM_CAUSE") {
		t.Fatalf("cause leaked to the response body: %s", rec.Body.String())
	}
	if line := buf.String(); !strings.Contains(line, "PRIVATE_STREAM_CAUSE") || !strings.Contains(line, "virtual_stream_unavailable") {
		t.Fatalf("log does not name the stream failure: %s", line)
	}
}

func TestPlaybackDeliveryFailureAfterBytesAborts(t *testing.T) {
	w := httptest.NewRecorder()
	writer := &playbackDeliveryWriter{ResponseWriter: w, request: httptest.NewRequest(http.MethodGet, "/", nil)}
	_, _ = io.WriteString(writer, "first")
	defer func() {
		got := recover()
		err, ok := got.(error)
		if !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("late failure did not abort: %v", got)
		}
		if w.Body.String() != "first" {
			t.Fatalf("appended error bytes: %q", w.Body.String())
		}
	}()
	http.Error(writer, "PRIVATE_ERROR", 500)
}

// A file served on the v2 stream route must reach the connection's ReadFrom,
// which is net/http's sendfile path, with the file still visible under at most
// one limiter. io.Copy finds ReadFrom only by direct assertion, so every writer
// on the route has to forward it. The handler is the real direct-play path,
// with its rolling-deadline writer on top.
func TestPlaybackDeliveryKeepsZeroCopy(t *testing.T) {
	deps, _ := catalogDeps(t)
	body := strings.Repeat("x", 64<<10)
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	deps.PlaybackMedia = &PlaybackMediaHandlers{Original: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := playback.ServeDirectPlay(w, r, path); err != nil {
			t.Errorf("direct play: %v", err)
		}
	})}
	h := newTestHandler(t, deps)
	for _, tc := range []struct {
		name, rangeHeader string
		status            int
		want              int64
	}{
		{"full", "", http.StatusOK, int64(len(body))},
		{"range", "bytes=10-1033", http.StatusPartialContent, 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"?st=opaque", nil)
			for k, v := range viewerHeaders() {
				r.Header.Set(k, v)
			}
			if tc.rangeHeader != "" {
				r.Header.Set("Range", tc.rangeHeader)
			}
			inner := &zeroCopyWriter{ResponseRecorder: httptest.NewRecorder()}
			h.ServeHTTP(inner, r)
			if inner.Code != tc.status || int64(inner.Body.Len()) != tc.want || inner.fromFile != tc.want {
				t.Fatalf("status %d, body %d bytes, %d of them read from the file by ReadFrom; want %d and %d", inner.Code, inner.Body.Len(), inner.fromFile, tc.status, tc.want)
			}
		})
	}
}

// A copy that starts before any status must not commit one until a byte
// arrives: a source that fails at once still gets its problem response, and
// one that fails after bytes ends the stream instead of appending JSON.
func TestPlaybackDeliveryCopyBeforeStatus(t *testing.T) {
	t.Run("fails before bytes", func(t *testing.T) {
		inner := &zeroCopyWriter{ResponseRecorder: httptest.NewRecorder()}
		writer := &playbackDeliveryWriter{ResponseWriter: inner, request: httptest.NewRequest(http.MethodGet, "/", nil)}
		if _, err := io.Copy(writer, iotest.ErrReader(io.ErrUnexpectedEOF)); err == nil {
			t.Fatal("copy succeeded")
		}
		http.Error(writer, "PRIVATE_ERROR", http.StatusServiceUnavailable)
		if inner.Code != http.StatusServiceUnavailable || inner.Header().Get("Content-Type") != problemContentType || strings.Contains(inner.Body.String(), "PRIVATE") {
			t.Fatalf("problem response: %d %v %s", inner.Code, inner.Header(), inner.Body.String())
		}
	})
	t.Run("fails after bytes", func(t *testing.T) {
		inner := &zeroCopyWriter{ResponseRecorder: httptest.NewRecorder()}
		writer := &playbackDeliveryWriter{ResponseWriter: inner, request: httptest.NewRequest(http.MethodGet, "/", nil)}
		// Hide MultiReader's WriteTo so io.Copy has to use the writer's ReadFrom.
		src := struct{ io.Reader }{io.MultiReader(strings.NewReader("first"), iotest.ErrReader(io.ErrUnexpectedEOF))}
		if _, err := io.Copy(writer, src); err == nil {
			t.Fatal("copy succeeded")
		}
		defer func() {
			if err, ok := recover().(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
				t.Fatalf("late failure did not abort: %v", err)
			}
			if inner.Code != http.StatusOK || inner.Body.String() != "first" {
				t.Fatalf("appended error bytes: %d %q", inner.Code, inner.Body.String())
			}
		}()
		http.Error(writer, "PRIVATE_ERROR", http.StatusBadGateway)
	})
}

func TestPlaybackDecisionV2ProjectsOnlyLocalMediaURLs(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/api/v1/stream/session?st=opaque%2Btoken", "/api/v2/stream/session?st=opaque%2Btoken"},
		{"/api/v1/playback/transcode/session/master.m3u8?st=opaque%2Btoken", "/api/v2/playback/transcode/session/master.m3u8?st=opaque%2Btoken"},
		{"https://silo.example.test/opaque", "https://silo.example.test/opaque"},
		{"/stream/s/subtitles/0.vtt?st=a%2Fb&file_id=7", "/api/v2/stream/s/subtitles/0.vtt?st=a%2Fb&file_id=7"},
		{"/stream/s/subtitles/0/fonts?st=a%2Fb", "/api/v2/stream/s/subtitles/0/fonts?st=a%2Fb"},
		{"/playback/transcode/s/master.m3u8?st=a%2Fb", "/api/v2/playback/transcode/s/master.m3u8?st=a%2Fb"},
		{"https://stream.example/api/v1/stream/s?st=a%2Fb", "https://stream.example/api/v1/stream/s?st=a%2Fb"},
		{"/api/v2/stream/s/subtitles/0.vtt?st=a%2Fb", "/api/v2/stream/s/subtitles/0.vtt?st=a%2Fb"},
	} {
		in := playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{Stream: playback.StreamV3{URL: tc.path}}}
		out := playbackDecision(in)
		if out.PlaybackPlan.Stream.URL != tc.want || in.PlaybackPlan.Stream.URL != tc.path {
			t.Fatalf("projection of %q = %q, want %q; source = %q", tc.path, out.PlaybackPlan.Stream.URL, tc.want, in.PlaybackPlan.Stream.URL)
		}
	}
}

// The virtual source revision is part of the client-visible plan shape: the web
// player keys subtitle source-change recovery on it, so the v2 projection must
// carry it and must omit it when the plan has no resolved virtual candidate.
func TestPlaybackDecisionV2ProjectsVirtualSourceRevision(t *testing.T) {
	const revision = "0123456789abcdef01234567"
	in := playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{VirtualSourceRevision: revision}}
	out := playbackDecision(in)
	if out.PlaybackPlan.VirtualSourceRevision != revision {
		t.Fatalf("virtual source revision = %q, want %q", out.PlaybackPlan.VirtualSourceRevision, revision)
	}

	empty := playbackDecision(playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{}})
	if empty.PlaybackPlan.VirtualSourceRevision != "" {
		t.Fatalf("empty plan projected a revision: %q", empty.PlaybackPlan.VirtualSourceRevision)
	}
	encoded, err := json.Marshal(empty.PlaybackPlan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "virtual_source_revision") {
		t.Fatalf("empty plan serialized the virtual source revision: %s", encoded)
	}
}

// The effective virtual URI is part of the client-visible plan shape: the v2
// player needs it to adopt a substituted candidate in its version menu, so the
// projection must carry it and must omit it when the plan has no resolved
// virtual candidate.
func TestPlaybackDecisionV2ProjectsEffectiveVirtualURI(t *testing.T) {
	const uri = "virtual://movie/tt1234567?result=working"
	in := playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{EffectiveVirtualURI: uri}}
	out := playbackDecision(in)
	if out.PlaybackPlan.EffectiveVirtualURI != uri {
		t.Fatalf("effective virtual URI = %q, want %q", out.PlaybackPlan.EffectiveVirtualURI, uri)
	}
	encodedPlan, err := json.Marshal(out.PlaybackPlan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encodedPlan), `"effective_virtual_uri":"`+uri+`"`) {
		t.Fatalf("projected plan did not serialize effective_virtual_uri: %s", encodedPlan)
	}

	empty := playbackDecision(playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{}})
	if empty.PlaybackPlan.EffectiveVirtualURI != "" {
		t.Fatalf("empty plan projected an effective virtual URI: %q", empty.PlaybackPlan.EffectiveVirtualURI)
	}
	encoded, err := json.Marshal(empty.PlaybackPlan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "effective_virtual_uri") {
		t.Fatalf("empty plan serialized an effective virtual URI: %s", encoded)
	}
}

// The audio inventory's canonical selection ordinal is part of the plan shape:
// the v2 projection must carry each track's selection_index/track_id (and the
// raw container stream index alongside it) so a client selects a track by the
// canonical ordinal rather than the raw index.
func TestPlaybackDecisionV2ProjectsAudioInventorySelectionOrdinal(t *testing.T) {
	in := playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{AudioTracks: []playback.AudioInventoryItemV3{
		{Index: 1, Language: "eng", Codec: "eac3", Channels: 6, Default: true, TrackID: playback.TrackIDV3(42, "audio", 0), SelectionIndex: 0},
		{Index: 3, Language: "fra", Codec: "aac", Channels: 2, TrackID: playback.TrackIDV3(42, "audio", 1), SelectionIndex: 1},
	}}}
	out := playbackDecision(in)
	if out.PlaybackPlan == nil || len(out.PlaybackPlan.AudioTracks) != 2 {
		t.Fatalf("projected audio tracks = %#v, want 2", out.PlaybackPlan)
	}
	for i, track := range out.PlaybackPlan.AudioTracks {
		if track.SelectionIndex != i {
			t.Fatalf("audio[%d].selection_index = %d, want %d", i, track.SelectionIndex, i)
		}
		if want := playback.TrackIDV3(42, "audio", i); track.TrackID != want {
			t.Fatalf("audio[%d].track_id = %q, want %q", i, track.TrackID, want)
		}
	}
	if out.PlaybackPlan.AudioTracks[0].Index != 1 || out.PlaybackPlan.AudioTracks[1].Index != 3 {
		t.Fatalf("raw stream indexes changed in projection: %+v", out.PlaybackPlan.AudioTracks)
	}

	empty := playbackDecision(playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{}})
	if len(empty.PlaybackPlan.AudioTracks) != 0 {
		t.Fatalf("empty plan projected audio tracks: %#v", empty.PlaybackPlan.AudioTracks)
	}
}

type fakeSubtitleFontService func(context.Context, handlers.SubtitleFontRequest) ([]playback.SubtitleFontBundleItem, bool, error)

func (f fakeSubtitleFontService) SubtitleFonts(ctx context.Context, in handlers.SubtitleFontRequest) ([]playback.SubtitleFontBundleItem, bool, error) {
	return f(ctx, in)
}

func TestPlaybackSubtitleDeliveryV2(t *testing.T) {
	deps, _ := catalogDeps(t)
	subtitleCalls, fontCalls := 0, 0
	var fontError error
	var emptyFonts bool
	deps.PlaybackMedia = &PlaybackMediaHandlers{
		Subtitle: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			subtitleCalls++
			if r.URL.Query().Get("st") != "opaque" || r.URL.Query().Get("file_id") != "42" {
				t.Error("signed reference or source file changed")
			}
			w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\ncue\n"))
		}),
		SubtitleFonts: fakeSubtitleFontService(func(ctx context.Context, in handlers.SubtitleFontRequest) ([]playback.SubtitleFontBundleItem, bool, error) {
			fontCalls++
			if fontError != nil {
				return nil, false, fontError
			}
			if in.SessionID != deliveryTestSession || in.Track != "1" || in.Query.Get("st") != "opaque" || in.Query.Get("file_id") != "42" || profileFrom(ctx) != "p-owner" {
				t.Errorf("font service lost admission or inventory identity: %+v profile=%q", in, profileFrom(ctx))
			}
			if emptyFonts {
				return nil, false, nil
			}
			return []playback.SubtitleFontBundleItem{{Name: "synthetic.ttf", Data: "AAEC"}}, false, nil
		}),
	}
	h := newTestHandler(t, deps)
	path := Prefix + "/stream/" + deliveryTestSession + "/subtitles/0.vtt?file_id=42&st=opaque"
	get := do(t, h, http.MethodGet, path, "", viewerHeaders())
	if get.Code != 200 || !strings.Contains(get.Body.String(), "WEBVTT") || get.Header().Get("Content-Type") != "text/vtt; charset=utf-8" {
		t.Fatalf("subtitle: %d %q %v", get.Code, get.Body.String(), get.Header())
	}
	head := do(t, h, http.MethodHead, path, "", viewerHeaders())
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", head.Code, head.Body.String())
	}
	fonts := do(t, h, http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"/subtitles/1/fonts?file_id=42&st=opaque", "", viewerHeaders())
	var bundle Collection[PlaybackSubtitleFont]
	decodeBody(t, fonts.Body, &bundle)
	if fonts.Code != 200 || len(bundle.Items) != 1 || bundle.Items[0].Name != "synthetic.ttf" || bundle.Items[0].Data != "AAEC" || fonts.Header().Get("Cache-Control") != "private, max-age=600" || fonts.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("fonts: %d %+v %v", fonts.Code, bundle, fonts.Header())
	}
	emptyFonts = true
	empty := do(t, h, http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"/subtitles/1/fonts?file_id=42&st=opaque", "", viewerHeaders())
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Fatalf("empty fonts: %d %s", empty.Code, empty.Body.String())
	}
	// Service refusals become v2 problems; extraction details remain private.
	for _, refusal := range []struct {
		err  error
		want ProblemType
	}{
		{&handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Subtitle font bundles are only available for ASS/SSA tracks"}, TypeValidationFailed},
		{&handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Embedded subtitle track not found"}, TypeNotFound},
		{&handlers.APIError{Status: http.StatusForbidden, Code: "forbidden", Message: "Session belongs to another user"}, TypePermissionDenied},
		{&handlers.APIError{Status: http.StatusGone, Code: "playback_session_ended", Message: "Playback session ended"}, TypePlaybackSessionEnded},
		{&handlers.APIError{Status: http.StatusInternalServerError, Code: "font_extract_failed", Message: "PRIVATE_DETAIL"}, TypeInternalError},
		{errors.New("PRIVATE_DETAIL"), TypeInternalError},
	} {
		fontError = refusal.err
		rec := do(t, h, http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"/subtitles/1/fonts?st=opaque", "", viewerHeaders())
		requireProblem(t, rec, refusal.want)
		if strings.Contains(rec.Body.String(), "PRIVATE_DETAIL") {
			t.Fatalf("5xx message leaked: %s", rec.Body.String())
		}
	}
	fontError = nil
	// Two sidecar calls, two accepted font calls, and six refusals.
	if subtitleCalls != 2 || fontCalls != 8 {
		t.Fatalf("calls = %d/%d", subtitleCalls, fontCalls)
	}
	// Same gates as media bytes: a non-UUID session is a validation problem, a
	// rejected viewer never reaches the producer, an unconfigured producer is 503.
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/stream/not-a-uuid/subtitles/0.vtt?st=opaque", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/stream/not-a-uuid/subtitles/0/fonts?st=opaque", "", viewerHeaders()), TypeValidationFailed)
	before, fontsBefore := subtitleCalls, fontCalls
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(bearer(memberToken), "X-Profile-Id", "p-other")), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"/subtitles/1/fonts?st=opaque", "", with(bearer(memberToken), "X-Profile-Id", "p-other")), TypeNotFound)
	if subtitleCalls != before || fontCalls != fontsBefore {
		t.Fatal("rejected viewer reached the subtitle producer")
	}
	deps.PlaybackMedia = &PlaybackMediaHandlers{}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, path, "", viewerHeaders()), TypeDependencyUnavailable)
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"/subtitles/1/fonts?st=opaque", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestPlaybackDecisionV2ProjectsSubtitleURLsWithoutMutatingSource(t *testing.T) {
	in := playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{Stream: playback.StreamV3{URL: "/api/v1/stream/s?st=x"}, Subtitle: playback.SubtitleDecisionV3{
		Artifact: &playback.SubtitleArtifactV3{URL: "/api/v1/stream/s/subtitles/1.ass?file_id=42&st=x"},
		Inventory: []playback.SubtitleInventoryItemV3{
			{CombinedIndex: 0, URL: "/api/v1/stream/s/subtitles/0.vtt?file_id=42&st=x"},
			{CombinedIndex: 1, URL: "/api/v1/stream/s/subtitles/1.ass?file_id=42&st=x", FontBundleURL: "/api/v1/stream/s/subtitles/1/fonts?file_id=42&st=x"},
			{CombinedIndex: 2, URL: "/stream/legacy/subtitles/2.vtt?file_id=42"},
		}}}}
	out := playbackDecision(in)
	sub := out.PlaybackPlan.Subtitle
	if sub.Artifact.URL != Prefix+"/stream/s/subtitles/1.ass?file_id=42&st=x" || sub.Inventory[0].URL != Prefix+"/stream/s/subtitles/0.vtt?file_id=42&st=x" || sub.Inventory[1].FontBundleURL != Prefix+"/stream/s/subtitles/1/fonts?file_id=42&st=x" || sub.Inventory[2].URL != Prefix+"/stream/legacy/subtitles/2.vtt?file_id=42" {
		t.Fatalf("projection: %+v %+v", sub.Artifact, sub.Inventory)
	}
	if in.PlaybackPlan.Subtitle.Artifact.URL != "/api/v1/stream/s/subtitles/1.ass?file_id=42&st=x" || in.PlaybackPlan.Subtitle.Inventory[0].URL != "/api/v1/stream/s/subtitles/0.vtt?file_id=42&st=x" {
		t.Fatal("projection mutated the persisted decision")
	}
}

func TestPlaybackSubtitleFontsV2CarriesSystemFontFallbackMarker(t *testing.T) {
	deps, _ := catalogDeps(t)
	deps.PlaybackMedia = &PlaybackMediaHandlers{
		SubtitleFonts: fakeSubtitleFontFallbackService{
			fakeSubtitleFontService: func(context.Context, handlers.SubtitleFontRequest) ([]playback.SubtitleFontBundleItem, bool, error) {
				return nil, true, nil
			},
			fallback: playback.SubtitleFontFallbackSubsetUnavailable,
		},
	}
	h := newTestHandler(t, deps)
	resp := do(t, h, http.MethodGet, Prefix+"/stream/"+deliveryTestSession+"/subtitles/1/fonts?file_id=42&st=opaque", "", viewerHeaders())
	if resp.Code != http.StatusOK {
		t.Fatalf("fonts status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("X-Vio-Font-Bundle-Fallback"); got != playback.SubtitleFontFallbackSubsetUnavailable {
		t.Fatalf("fallback header = %q, want %q", got, playback.SubtitleFontFallbackSubsetUnavailable)
	}
	if got := resp.Header().Get("X-Vio-Font-Bundle-Pending"); got != "true" {
		t.Fatalf("pending header = %q, want true", got)
	}
	if got := resp.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", got)
	}
}

// fakeSubtitleFontFallbackService implements the optional detailed extension
// while keeping the historical two-result fake usable for other tests.
type fakeSubtitleFontFallbackService struct {
	fakeSubtitleFontService
	fallback string
}

func (f fakeSubtitleFontFallbackService) SubtitleFontsWithFallback(ctx context.Context, in handlers.SubtitleFontRequest) ([]playback.SubtitleFontBundleItem, bool, string, error) {
	items, pending, err := f.fakeSubtitleFontService(ctx, in)
	return items, pending, f.fallback, err
}

func TestPlaybackDecisionV2EmptySubtitleInventoryIsArray(t *testing.T) {
	for _, tc := range []struct {
		name      string
		inventory []playback.SubtitleInventoryItemV3
	}{{"nil", nil}, {"empty", []playback.SubtitleInventoryItemV3{}}} {
		t.Run(tc.name, func(t *testing.T) {
			in := playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{
				Subtitle: playback.SubtitleDecisionV3{Mode: playback.SubtitleOffV3, Inventory: tc.inventory},
			}}
			out := playbackDecision(in)
			raw, err := json.Marshal(out.PlaybackPlan.Subtitle)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"inventory":[]`) {
				t.Fatalf("v2 inventory must be an array: %s", raw)
			}
			if (in.PlaybackPlan.Subtitle.Inventory == nil) != (tc.inventory == nil) || len(in.PlaybackPlan.Subtitle.Inventory) != 0 {
				t.Fatal("projection mutated the source inventory")
			}
		})
	}
}

func TestPlaybackDecisionV2PublishesTheActiveQuality(t *testing.T) {
	in := playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{ActiveQuality: playback.QualityRung1080pLowV3}}
	raw, err := json.Marshal(playbackDecision(in).PlaybackPlan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"active_quality":"1080p-low"`) {
		t.Fatalf("v2 plan must carry active_quality: %s", raw)
	}
}
