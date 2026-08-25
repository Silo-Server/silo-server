package downloadprepare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestHTTPPreparerSendsAuthenticatedRecipe(t *testing.T) {
	var got Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/downloads/prepare" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer secret" {
			t.Fatalf("Authorization = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(Result{ArtifactID: got.ArtifactID, FileSize: 123})
	}))
	defer server.Close()

	want := Request{ArtifactID: "job-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac"}
	result, err := (HTTPPreparer{}).Prepare(context.Background(), server.URL+"/", "secret", want)
	if err != nil {
		t.Fatal(err)
	}
	if result != (Result{ArtifactID: "job-1", FileSize: 123}) {
		t.Fatalf("result = %+v", result)
	}
	if got != want {
		t.Fatalf("request = %+v, want %+v", got, want)
	}
}

func TestResultJSONCarriesToneMapAttestationAndOmitsItForOrdinaryArtifacts(t *testing.T) {
	toneMapped := Result{
		ArtifactID:                       "artifact-tone-map",
		FileSize:                         123,
		ToneMapRecipeVersion:             "1",
		ToneMapMode:                      tonemap.ModeSoftware,
		ToneMapSourceRevisionFingerprint: "0123456789abcdef",
	}
	wire, err := json.Marshal(toneMapped)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Result
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != toneMapped {
		t.Fatalf("result round trip = %+v, want %+v", decoded, toneMapped)
	}

	ordinary, err := json.Marshal(Result{ArtifactID: "artifact-ordinary", FileSize: 42})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(ordinary); got != `{"artifact_id":"artifact-ordinary","file_size":42}` {
		t.Fatalf("ordinary result JSON = %s", got)
	}
}

// TestRequestRoundTripPreservesFrozenToneMapRecipe verifies remote requests retain executor facts.
func TestRequestRoundTripPreservesFrozenToneMapRecipe(t *testing.T) {
	revision := tonemap.SourceRevision{MediaFileID: 42, FileSize: 100, FileModifiedUnixNano: 200, StreamSignature: "stream"}
	want := playback.TranscodeOpts{
		InputPath: "/media/hdr.mkv", ToneMapPolicy: tonemap.PolicyHardwareThenSoftware,
		ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ,
		ToneMapFilter: "tonemap_vaapi", ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapPreflightRequired: true, ToneMapSourceRevision: revision,
		ToneMapDVConfigPresent: true, ToneMapDVBLCompatIDPresent: true, ToneMapDVBLPresent: true, ToneMapDVRPUPresent: true,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac",
	}
	request := NewRequest("artifact-1", want)
	wire, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var decoded Request
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	got := decoded.TranscodeOpts("/usr/bin/ffmpeg", "qsv", "/dev/dri/renderD128", nil)
	if got.ToneMapPolicy != want.ToneMapPolicy || got.ToneMapMode != want.ToneMapMode ||
		got.ToneMapSourceKind != want.ToneMapSourceKind || got.ToneMapRecipeVersion != want.ToneMapRecipeVersion ||
		got.ToneMapPreflightRequired != want.ToneMapPreflightRequired || got.ToneMapSourceRevision != revision {
		t.Fatalf("tone-map request round trip = %+v, want %+v", got, want)
	}
	if !got.ToneMapDVConfigPresent || !got.ToneMapDVBLCompatIDPresent || !got.ToneMapDVBLPresent || !got.ToneMapDVRPUPresent {
		t.Fatalf("Dolby Vision source presence flags were lost: %+v", got)
	}
	if got.ToneMapFilter != "" {
		t.Fatalf("node request trusted a server-selected filter: %q", got.ToneMapFilter)
	}
}

// TestRequestRoundTripTreatsNonePolicyAsOrdinaryTranscode verifies non-tone-map requests remain compatible.
func TestRequestRoundTripTreatsNonePolicyAsOrdinaryTranscode(t *testing.T) {
	request := NewRequest("artifact-1", playback.TranscodeOpts{
		InputPath: "/media/sdr.mkv", ToneMapPolicy: tonemap.PolicyNone,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac",
	})
	got := request.TranscodeOpts("/usr/bin/ffmpeg", "qsv", "/dev/dri/renderD128", nil)
	if got.ToneMapMode != "" || got.ToneMapSourceKind != "" || got.ToneMapRecipeVersion != "" {
		t.Fatalf("ordinary transcode gained tone-map recipe fields: %+v", got)
	}
}

func TestRequestExecutionFingerprintBindsRecipeButNotArtifactHandle(t *testing.T) {
	base := Request{
		ArtifactID: "attempt-a", InputPath: "/media/hdr.mkv", SourceVideoCodec: "h264",
		SourceVideoProfile: "High 10", SourceVideoBitDepth: 10, SoftwareVideoDecode: true,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapPreflightRequired: true, ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 42, FileSize: 100},
		ToneMapDVConfigPresent: true, ToneMapDVBLCompatIDPresent: true, ToneMapDVBLPresent: true, ToneMapDVRPUPresent: true,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "1080p",
		TargetBitrateKbps: 8000, AudioTrackIndex: 1, TotalDuration: 7200,
	}
	want := base.ExecutionFingerprint()
	if want == "" {
		t.Fatal("ExecutionFingerprint() is empty")
	}
	handleOnly := base
	handleOnly.ArtifactID = "attempt-b"
	if got := handleOnly.ExecutionFingerprint(); got != want {
		t.Fatalf("artifact handle changed execution fingerprint: %q != %q", got, want)
	}
	changed := base
	changed.SoftwareVideoDecode = false
	if got := changed.ExecutionFingerprint(); got == want {
		t.Fatal("byte-affecting software decode did not change execution fingerprint")
	}
	changed = base
	changed.TotalDuration++
	if got := changed.ExecutionFingerprint(); got == want {
		t.Fatal("byte-affecting duration did not change execution fingerprint")
	}
}

func TestRequestToneMapRequestedIncludesPartialRecipes(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want bool
	}{
		{name: "ordinary", req: Request{ToneMapPolicy: tonemap.PolicyNone}},
		{name: "policy", req: Request{ToneMapPolicy: tonemap.PolicySoftwareOnly}, want: true},
		{name: "mode", req: Request{ToneMapMode: tonemap.ModeSoftware}, want: true},
		{name: "source kind", req: Request{ToneMapSourceKind: tonemap.SourcePQ}, want: true},
		{name: "recipe version", req: Request{ToneMapRecipeVersion: "1"}, want: true},
		{name: "preflight", req: Request{ToneMapPreflightRequired: true}, want: true},
		{name: "source revision", req: Request{ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1}}, want: true},
		{name: "Dolby Vision", req: Request{ToneMapDVConfigPresent: true}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.req.ToneMapRequested(); got != test.want {
				t.Fatalf("ToneMapRequested() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestRequestValidToneMapAttestationRequiresCompleteCurrentRecipe(t *testing.T) {
	valid := Request{
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1, FileSize: 8},
	}
	if !valid.ValidToneMapAttestation() {
		t.Fatal("complete current tone-map recipe was not valid for attestation")
	}
	for _, mutate := range []func(*Request){
		func(req *Request) { req.ToneMapPolicy = tonemap.PolicyNone },
		func(req *Request) { req.ToneMapMode = "" },
		func(req *Request) { req.ToneMapSourceKind = "" },
		func(req *Request) { req.ToneMapRecipeVersion = "stale" },
		func(req *Request) { req.ToneMapSourceRevision = tonemap.SourceRevision{} },
	} {
		req := valid
		mutate(&req)
		if req.ValidToneMapAttestation() {
			t.Fatalf("incomplete or stale recipe was valid for attestation: %+v", req)
		}
	}
}

func TestHTTPPreparerReportsNodeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "mount unavailable", http.StatusUnprocessableEntity)
	}))
	defer server.Close()

	_, err := (HTTPPreparer{}).Prepare(context.Background(), server.URL, "secret", Request{ArtifactID: "job-2"})
	if err == nil {
		t.Fatal("expected remote failure")
	}
}

func TestHTTPPreparerManagesOpaqueArtifact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/downloads/artifacts/artifact-1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Length", "42")
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	defer server.Close()
	client := HTTPPreparer{}
	result, err := client.Stat(context.Background(), server.URL, "secret", "artifact-1")
	if err != nil || result != (Result{ArtifactID: "artifact-1", FileSize: 42}) {
		t.Fatalf("Stat = (%+v, %v)", result, err)
	}
	if err := client.Delete(context.Background(), server.URL, "secret", "artifact-1"); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPPreparerStatRecoversToneMapAttestationFromHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("method = %s, want HEAD", r.Method)
		}
		w.Header().Set("Content-Length", "42")
		w.Header().Set("X-Silo-Tone-Map-Recipe-Version", "1")
		w.Header().Set("X-Silo-Tone-Map-Mode", "software")
		w.Header().Set("X-Silo-Tone-Map-Source-Revision-Fingerprint", "0123456789abcdef")
	}))
	defer server.Close()

	result, err := (HTTPPreparer{}).Stat(context.Background(), server.URL, "secret", "artifact-1")
	if err != nil {
		t.Fatal(err)
	}
	want := Result{
		ArtifactID:                       "artifact-1",
		FileSize:                         42,
		ToneMapRecipeVersion:             "1",
		ToneMapMode:                      tonemap.ModeSoftware,
		ToneMapSourceRevisionFingerprint: "0123456789abcdef",
	}
	if result != want {
		t.Fatalf("Stat = %+v, want %+v", result, want)
	}
}

func TestHTTPPreparerStatRejectsOversizedAttestationHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "42")
		w.Header().Set("X-Silo-Tone-Map-Recipe-Version", strings.Repeat("x", 1025))
	}))
	defer server.Close()

	if _, err := (HTTPPreparer{}).Stat(context.Background(), server.URL, "secret", "artifact-1"); err == nil {
		t.Fatal("Stat accepted an oversized attestation header")
	}
}

func TestSetResultHeadersOmitsOversizedAttestation(t *testing.T) {
	header := make(http.Header)
	SetResultHeaders(header, Result{
		ToneMapRecipeVersion:             strings.Repeat("x", 1025),
		ToneMapMode:                      tonemap.ModeSoftware,
		ToneMapSourceRevisionFingerprint: "0123456789abcdef",
	})
	if got := header.Get("X-Silo-Tone-Map-Recipe-Version"); got != "" {
		t.Fatalf("oversized recipe header = %q", got)
	}
	if got := header.Get("X-Silo-Tone-Map-Mode"); got != string(tonemap.ModeSoftware) {
		t.Fatalf("mode header = %q", got)
	}
}

func TestHTTPPreparerDeleteStopsStalledErrorBodyRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	started := time.Now()
	err := (HTTPPreparer{ReadIdleTimeout: 50 * time.Millisecond}).Delete(
		context.Background(), server.URL, "secret", "artifact-stalled-delete",
	)
	if !errors.Is(err, ErrRelayReadIdle) {
		t.Fatalf("Delete error = %v, want ErrRelayReadIdle", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled delete response took %s, want a bounded failure", elapsed)
	}
}

func TestHTTPPreparerRejectsArtifactPathTraversal(t *testing.T) {
	if _, err := (HTTPPreparer{}).Stat(context.Background(), "http://node", "secret", "../escape"); err == nil {
		t.Fatal("expected invalid artifact id error")
	}
}

func TestDefaultPrepareClientHasNoResponseHeaderDeadline(t *testing.T) {
	transport, ok := defaultPrepareHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", defaultPrepareHTTPClient.Transport)
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Fatalf("prepare response header timeout = %s, want none", transport.ResponseHeaderTimeout)
	}
}

func TestHTTPPreparerPrepareStopsStalledResultBodyRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	started := time.Now()
	_, err := (HTTPPreparer{ReadIdleTimeout: 50 * time.Millisecond}).Prepare(
		context.Background(), server.URL, "secret", Request{ArtifactID: "artifact-stalled-result"},
	)
	if !errors.Is(err, ErrRelayReadIdle) {
		t.Fatalf("Prepare error = %v, want ErrRelayReadIdle", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled result read took %s, want a bounded failure", elapsed)
	}
}

func TestRelayStatusAllowedPreservesServeContentOutcomes(t *testing.T) {
	for _, status := range []int{
		http.StatusOK,
		http.StatusPartialContent,
		http.StatusNotModified,
		http.StatusPreconditionFailed,
		http.StatusRequestedRangeNotSatisfiable,
	} {
		if !RelayStatusAllowed(status) {
			t.Errorf("status %d should be relayed", status)
		}
	}
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusBadRequest, http.StatusInternalServerError} {
		if RelayStatusAllowed(status) {
			t.Errorf("status %d should be rejected", status)
		}
	}
}

func TestCopyResponseHeadersDoesNotExposeOriginArtifactFilename(t *testing.T) {
	src := http.Header{
		"Content-Disposition": {`attachment; filename="opaque-artifact.mp4"`},
		"Content-Type":        {"video/mp4"},
		"Content-Length":      {"42"},
	}
	dst := make(http.Header)
	CopyResponseHeaders(dst, src)
	if got := dst.Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q, want omitted", got)
	}
	if dst.Get("Content-Type") != "video/mp4" || dst.Get("Content-Length") != "42" {
		t.Fatalf("copied headers = %v", dst)
	}
}

func TestHTTPPreparerOpenStopsStalledBodyRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	resp, err := (HTTPPreparer{ReadIdleTimeout: 50 * time.Millisecond}).Open(
		context.Background(), server.URL, "secret", "artifact-stalled", http.MethodGet, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	started := time.Now()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, ErrRelayReadIdle) {
		t.Fatalf("ReadAll error = %v, want ErrRelayReadIdle", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled read took %s, want a bounded failure", elapsed)
	}
}

func TestHTTPPreparerOpenAllowsContinuedBodyProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, _ := w.(http.Flusher)
		for _, chunk := range []string{"one", "two", "three"} {
			_, _ = io.WriteString(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()

	resp, err := (HTTPPreparer{ReadIdleTimeout: 50 * time.Millisecond}).Open(
		context.Background(), server.URL, "secret", "artifact-progress", http.MethodGet, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "onetwothree" {
		t.Fatalf("body = %q", body)
	}
}
