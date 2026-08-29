package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/go-chi/chi/v5"
)

const (
	artworkTestSecret = "cluster-authentication-secret"
	artworkTestTTL    = time.Hour
)

var artworkTestBytes = []byte("not really webp, but immutable bytes")

var artworkTestRevision = func() *artworkkey.PortableRevision {
	revision, err := artworkkey.BuildPortableRevision(artworkkey.RevisionInput{
		ImageType: "poster", MediaType: "image/webp", Ext: ".webp",
		Variants: []artworkkey.VariantBytes{{Name: artworkkey.OriginalVariant, Data: artworkTestBytes}},
	})
	if err != nil {
		panic(err)
	}
	return revision
}()

var artworkTestKey = artworkTestRevision.OriginalKey

var artworkTestTarget = artworkurl.Target{
	Surface: artworkurl.SurfaceItemPosters,
	Keys:    []string{"movie-1"},
	Slot:    "poster",
}.WithReference(artworkTestKey)

type fakeArtworkTargets struct {
	state       metadata.ArtworkTargetState
	signals     int
	markHealthy int
	signalCh    chan struct{}
}

type coldBurstArtworkTargets struct {
	mu       sync.Mutex
	state    metadata.ArtworkTargetState
	reads    int
	signals  int
	signalCh chan struct{}
}

type unavailableArtworkStore struct{}

type countingArtworkStore struct {
	ArtworkObjectStore
	key       string
	readBytes int
}

type countingReadCloser struct {
	io.ReadCloser
	count *int
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	*r.count += n
	return n, err
}

func (s *countingArtworkStore) Open(ctx context.Context, key string) (*artworkstore.Object, error) {
	object, err := s.ArtworkObjectStore.Open(ctx, key)
	if err == nil && key == s.key {
		object.Body = &countingReadCloser{ReadCloser: object.Body, count: &s.readBytes}
	}
	return object, err
}

func (unavailableArtworkStore) Open(context.Context, string) (*artworkstore.Object, error) {
	return nil, errors.New("backend transport unavailable")
}
func (unavailableArtworkStore) Health() (artworkstore.HealthState, time.Time) {
	return artworkstore.HealthUnavailable, time.Now()
}

func (f *coldBurstArtworkTargets) LoadTarget(_ context.Context, target artworkurl.Target) (metadata.ArtworkTargetState, error) {
	state := f.state
	state.Target = target
	return state, nil
}

func (f *coldBurstArtworkTargets) SignalMissing(context.Context, metadata.ArtworkTargetState) error {
	f.mu.Lock()
	f.signals++
	f.mu.Unlock()
	if f.signalCh != nil {
		select {
		case f.signalCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *coldBurstArtworkTargets) ReadSidecar(context.Context, metadata.ArtworkTargetState) (metadata.ConfinedLocalArtwork, error) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
	return metadata.ConfinedLocalArtwork{Data: artworkPlaceholder("poster"), MediaType: "image/png"}, nil
}

func (f *coldBurstArtworkTargets) MarkHealthy(context.Context, string) error { return nil }

func (f *fakeArtworkTargets) LoadTarget(_ context.Context, target artworkurl.Target) (metadata.ArtworkTargetState, error) {
	if target.Surface != artworkTestTarget.Surface || target.Keys[0] != artworkTestTarget.Keys[0] {
		return metadata.ArtworkTargetState{}, errors.New("unknown target")
	}
	state := f.state
	state.Target = target
	return state, nil
}

func (f *fakeArtworkTargets) SignalMissing(context.Context, metadata.ArtworkTargetState) error {
	f.signals++
	if f.signalCh != nil {
		select {
		case f.signalCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *fakeArtworkTargets) ReadSidecar(context.Context, metadata.ArtworkTargetState) (metadata.ConfinedLocalArtwork, error) {
	return metadata.ConfinedLocalArtwork{}, errors.New("no sidecar")
}

func (f *fakeArtworkTargets) MarkHealthy(context.Context, string) error {
	f.markHealthy++
	return nil
}

// newArtworkTestRig stands up the real filesystem store behind the real route,
// so the assertions below cover key validation, media typing, and entity tags
// exactly as production does.
func newArtworkTestRig(t *testing.T) (http.Handler, *ArtworkHandler, *artworkurl.Signer, *fakeArtworkTargets) {
	t.Helper()

	store, err := artworkstore.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Probe(t.Context()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if err := store.WriteImmutable(t.Context(), artworkTestKey, artworkTestBytes, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	if err := store.WriteImmutable(t.Context(), artworkTestRevision.ManifestKey, artworkTestRevision.ManifestJSON, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable(manifest): %v", err)
	}

	signer, err := artworkurl.NewSigner(artworkTestSecret, func() time.Duration { return artworkTestTTL })
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	handler := NewArtworkHandler(store, signer)
	if handler == nil {
		t.Fatal("NewArtworkHandler returned nil")
	}

	targets := &fakeArtworkTargets{signalCh: make(chan struct{}, 1), state: metadata.ArtworkTargetState{
		SelectedPath: artworkTestKey, ImageType: "poster", Recoverable: false, Protected: true,
	}}
	handler.SetResilientDependencies(targets, nil, nil)

	router := chi.NewRouter()
	router.Get("/api/v1/artwork/{"+ArtworkCapabilityParam+"}/{"+ArtworkVariantParam+"}", handler.ServeHTTP)
	router.Head("/api/v1/artwork/{"+ArtworkCapabilityParam+"}/{"+ArtworkVariantParam+"}", handler.ServeHTTP)
	return router, handler, signer, targets
}

func waitForRepairSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for durable repair signal")
	}
}

func signArtworkURL(t *testing.T, signer *artworkurl.Signer, at time.Time) string {
	t.Helper()
	signed, err := signer.SignTarget(artworkTestTarget, "original", at)
	if err != nil {
		t.Fatalf("SignTarget: %v", err)
	}
	return signed.URL
}

func TestArtworkHandlerServesSignedObject(t *testing.T) {
	router, _, signer, _ := newArtworkTestRig(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, signArtworkURL(t, signer, time.Now()), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != string(artworkTestBytes) {
		t.Fatalf("body = %q, want the stored bytes", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/webp" {
		t.Fatalf("Content-Type = %q, want image/webp", got)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(artworkTestBytes)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(artworkTestBytes))
	}
	etag := rec.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("ETag = %q, want a quoted strong entity tag", etag)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}

	// Private and bounded by the signed lifetime: a client must not hold bytes
	// under a URL it can no longer fetch, and no shared cache may keep them.
	cacheControl := rec.Header().Get("Cache-Control")
	if !strings.HasPrefix(cacheControl, "private, max-age=") {
		t.Fatalf("Cache-Control = %q, want a private bounded lifetime", cacheControl)
	}
	maxAge, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(cacheControl, "private, max-age="), ", immutable"))
	if err != nil {
		t.Fatalf("parsing max-age from %q: %v", cacheControl, err)
	}
	if maxAge <= 0 || time.Duration(maxAge)*time.Second > 2*artworkTestTTL {
		t.Fatalf("max-age = %ds, want a positive value within the signed lifetime", maxAge)
	}
}

func TestArtworkHandlerServesPreLadderPortableRevisionFromManifest(t *testing.T) {
	store, err := artworkstore.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	oldVariantData := map[string][]byte{
		artworkkey.OriginalVariant: []byte("old original"),
		artworkkey.VariantW500:     []byte("old medium"),
		artworkkey.VariantW300:     []byte("old small"),
	}
	oldRevision, err := artworkkey.BuildPortableRevision(artworkkey.RevisionInput{
		ImageType: artworkkey.ImageTypePoster,
		MediaType: "image/webp",
		Ext:       ".webp",
		Variants: []artworkkey.VariantBytes{
			{Name: artworkkey.OriginalVariant, Data: oldVariantData[artworkkey.OriginalVariant]},
			{Name: artworkkey.VariantW500, Data: oldVariantData[artworkkey.VariantW500]},
			{Name: artworkkey.VariantW300, Data: oldVariantData[artworkkey.VariantW300]},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for variant, key := range oldRevision.VariantKeys {
		if err := store.WriteImmutable(t.Context(), key, oldVariantData[variant], artworkstore.ObjectMetadata{}); err != nil {
			t.Fatalf("write %s: %v", variant, err)
		}
	}
	if err := store.WriteImmutable(t.Context(), oldRevision.ManifestKey, oldRevision.ManifestJSON, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatal(err)
	}

	signer, err := artworkurl.NewSigner(artworkTestSecret, func() time.Duration { return artworkTestTTL })
	if err != nil {
		t.Fatal(err)
	}
	targets := &fakeArtworkTargets{state: metadata.ArtworkTargetState{
		SelectedPath: oldRevision.OriginalKey,
		ImageType:    artworkkey.ImageTypePoster,
		Recoverable:  true,
	}}
	handler := NewArtworkHandler(store, signer)
	handler.SetResilientDependencies(targets, nil, nil)
	target := artworkTestTarget.WithReference(oldRevision.OriginalKey)
	signed, err := signer.SignTarget(target, artworkkey.VariantW780, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeArtworkURL(rec, httptest.NewRequest(http.MethodGet, signed.URL, nil), signed.URL)

	if rec.Code != http.StatusOK || rec.Body.String() != "old medium" {
		t.Fatalf("response = %d %q, want the manifest-selected w500 bytes", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Silo-Artwork"); got != "stored" {
		t.Fatalf("X-Silo-Artwork = %q, want stored", got)
	}
	if targets.signals != 0 || len(handler.repairSignals) != 0 {
		t.Fatalf("pre-ladder fallback queued repair: signals=%d queued=%d", targets.signals, len(handler.repairSignals))
	}
}

func TestArtworkHandlerHonorsHeadAndConditionalRequests(t *testing.T) {
	router, _, signer, _ := newArtworkTestRig(t)
	signedURL := signArtworkURL(t, signer, time.Now())

	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest(http.MethodHead, signedURL, nil))
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", head.Code)
	}
	if head.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", head.Body.String())
	}
	if got := head.Header().Get("Content-Length"); got != strconv.Itoa(len(artworkTestBytes)) {
		t.Fatalf("HEAD Content-Length = %q, want %d", got, len(artworkTestBytes))
	}

	etag := head.Header().Get("ETag")
	conditional := httptest.NewRequest(http.MethodGet, signedURL, nil)
	conditional.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, conditional)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional GET status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 body = %q, want empty", rec.Body.String())
	}
}

// Ranges are not required by the delivery contract, but a seekable store gets
// them from the standard serving primitives, and a client that asks for one
// must not receive the whole object with a 200.
func TestArtworkHandlerServesRangesFromASeekableStore(t *testing.T) {
	router, _, signer, _ := newArtworkTestRig(t)

	req := httptest.NewRequest(http.MethodGet, signArtworkURL(t, signer, time.Now()), nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got, want := rec.Body.String(), string(artworkTestBytes[:4]); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

// nonSeekableArtworkBody hides the seeking a bytes.Reader would otherwise
// expose, so the handler sees the one-shot stream a remote backend hands it.
type nonSeekableArtworkBody struct {
	io.Reader
}

func (nonSeekableArtworkBody) Close() error { return nil }

type nonSeekableArtworkStore struct {
	ArtworkObjectStore
}

func (s nonSeekableArtworkStore) Open(ctx context.Context, key string) (*artworkstore.Object, error) {
	object, err := s.ArtworkObjectStore.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(object.Body)
	_ = object.Close()
	if readErr != nil {
		return nil, readErr
	}
	object.Body = nonSeekableArtworkBody{Reader: bytes.NewReader(data)}
	return object, nil
}

func newNonSeekableArtworkRig(t *testing.T) (*ArtworkHandler, *artworkurl.Signer) {
	t.Helper()
	store, err := artworkstore.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.WriteImmutable(t.Context(), artworkTestKey, artworkTestBytes, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	if err := store.WriteImmutable(t.Context(), artworkTestRevision.ManifestKey, artworkTestRevision.ManifestJSON, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable(manifest): %v", err)
	}
	signer, err := artworkurl.NewSigner(artworkTestSecret, func() time.Duration { return artworkTestTTL })
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	handler := NewArtworkHandler(nonSeekableArtworkStore{ArtworkObjectStore: store}, signer)
	handler.SetResilientDependencies(&fakeArtworkTargets{state: metadata.ArtworkTargetState{
		SelectedPath: artworkTestKey, ImageType: "poster", Protected: true,
	}}, nil, nil)
	return handler, signer
}

// The delivery contract promises ranges on every backend, so a store whose body
// cannot seek — an object store, in production — still has to answer one
// instead of replying 200 with the whole object.
func TestArtworkHandlerServesRangesFromANonSeekableStore(t *testing.T) {
	handler, signer := newNonSeekableArtworkRig(t)
	signedURL := signArtworkURL(t, signer, time.Now())

	ranged := httptest.NewRequest(http.MethodGet, signedURL, nil)
	ranged.Header.Set("Range", "bytes=4-9")
	rec := httptest.NewRecorder()
	handler.ServeArtworkURL(rec, ranged, signedURL)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 (body %q)", rec.Code, rec.Body.String())
	}
	if got, want := rec.Body.String(), string(artworkTestBytes[4:10]); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	want := "bytes 4-9/" + strconv.Itoa(len(artworkTestBytes))
	if got := rec.Header().Get("Content-Range"); got != want {
		t.Fatalf("Content-Range = %q, want %q", got, want)
	}

	// A request without a Range header must keep streaming the body whole,
	// buying no buffer for a client that never asked for one.
	full := httptest.NewRecorder()
	handler.ServeArtworkURL(full, httptest.NewRequest(http.MethodGet, signedURL, nil), signedURL)
	if full.Code != http.StatusOK || full.Body.String() != string(artworkTestBytes) {
		t.Fatalf("unranged response = %d %q, want 200 with the stored bytes", full.Code, full.Body.String())
	}
	if got := full.Header().Get("Content-Length"); got != strconv.Itoa(len(artworkTestBytes)) {
		t.Fatalf("unranged Content-Length = %q, want %d", got, len(artworkTestBytes))
	}
}

// Every rejection has to look the same, or the route becomes a way to ask
// which artwork a server holds.
func TestArtworkHandlerHidesKeyExistence(t *testing.T) {
	router, _, signer, _ := newArtworkTestRig(t)

	valid := signArtworkURL(t, signer, time.Now())

	cases := []struct {
		name string
		url  string
	}{
		{"unsigned", artworkurl.RoutePrefix + "unsigned/original"},
		{"forged signature", strings.Replace(valid, ".", ".AAAA", 1)},
		{"missing capability", artworkurl.RoutePrefix + "/original"},
		{"invalid variant", strings.TrimSuffix(valid, "/original") + "/../../secret"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestArtworkHandlerReturnsPlaceholderAndSignalsRepairForValidMissingTarget(t *testing.T) {
	router, _, signer, targets := newArtworkTestRig(t)
	targets.state.SelectedPath = "artwork/v1/objects/poster/cd/cdef456789/original.webp"
	targets.state.Protected = true

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, signArtworkURL(t, signer, time.Now()), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want placeholder 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("X-Silo-Artwork"); got != "placeholder" {
		t.Fatalf("diagnostic header = %q, want placeholder", got)
	}
	waitForRepairSignal(t, targets.signalCh)
	if targets.signals != 1 {
		t.Fatalf("repair signals = %d, want exactly one", targets.signals)
	}
}

func TestArtworkHandlerColdBurstSingleflightsSourceFallback(t *testing.T) {
	router, handler, signer, _ := newArtworkTestRig(t)
	targets := &coldBurstArtworkTargets{signalCh: make(chan struct{}, 1), state: metadata.ArtworkTargetState{
		SelectedPath: "artwork/v1/objects/poster/cd/cdef456789/original.webp",
		SourcePath:   "file:///library/movie/poster.png",
		ImageType:    "poster",
		Recoverable:  true,
	}}
	handler.SetResilientDependencies(targets, nil, nil)
	signedURL := signArtworkURL(t, signer, time.Now())

	const requests = 24
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, signedURL, nil))
			results <- rec
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for rec := range results {
		if rec.Code != http.StatusOK || rec.Header().Get("X-Silo-Artwork") != "source_fallback" {
			t.Fatalf("status/route = %d/%q", rec.Code, rec.Header().Get("X-Silo-Artwork"))
		}
	}
	targets.mu.Lock()
	reads := targets.reads
	targets.mu.Unlock()
	// Singleflight coalesces overlapping work, while a request scheduled after
	// the first flight completes must re-read the sidecar to obtain its current
	// file identity before consulting the emergency cache. The burst must still
	// collapse well below one confined read per request.
	if reads <= 0 || reads >= requests {
		t.Fatalf("confined source reads = %d, want between 1 and %d", reads, requests-1)
	}
	targets.mu.Lock()
	signals := targets.signals
	targets.mu.Unlock()
	if signals != 1 {
		t.Fatalf("cold burst repair signals = %d, want one", signals)
	}
}

func TestArtworkHandlerDoesNotDeclareStoredRevisionMissingDuringBackendOutage(t *testing.T) {
	signer, err := artworkurl.NewSigner(artworkTestSecret, func() time.Duration { return artworkTestTTL })
	if err != nil {
		t.Fatal(err)
	}
	targets := &coldBurstArtworkTargets{signalCh: make(chan struct{}, 1), state: metadata.ArtworkTargetState{
		SelectedPath: artworkTestKey,
		SourcePath:   "file:///library/poster.png",
		ImageType:    "poster",
		Recoverable:  true,
	}}
	handler := NewArtworkHandler(unavailableArtworkStore{}, signer)
	handler.SetResilientDependencies(targets, nil, nil)
	signed, err := signer.SignTarget(artworkTestTarget, "original", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.serve(recorder, httptest.NewRequest(http.MethodGet, signed.URL, nil),
		strings.Split(strings.TrimPrefix(signed.URL, artworkurl.RoutePrefix), "/")[0], "original")
	if recorder.Code != http.StatusOK || recorder.Header().Get("X-Silo-Artwork") != "source_fallback" {
		t.Fatalf("response = %d %q, want verified source fallback", recorder.Code, recorder.Header().Get("X-Silo-Artwork"))
	}
	targets.mu.Lock()
	signals := targets.signals
	targets.mu.Unlock()
	if signals != 0 {
		t.Fatalf("durable missing signals = %d, want zero for a transport outage", signals)
	}
}

func TestArtworkHandlerTreatsPortableDigestMismatchAsAuthoritativeLoss(t *testing.T) {
	store, err := artworkstore.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.WriteImmutable(t.Context(), artworkTestKey, []byte("corrupted immutable bytes"), artworkstore.ObjectMetadata{}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteImmutable(t.Context(), artworkTestRevision.ManifestKey, artworkTestRevision.ManifestJSON, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatal(err)
	}
	signer, err := artworkurl.NewSigner(artworkTestSecret, func() time.Duration { return artworkTestTTL })
	if err != nil {
		t.Fatal(err)
	}
	targets := &coldBurstArtworkTargets{signalCh: make(chan struct{}, 1), state: metadata.ArtworkTargetState{
		SelectedPath: artworkTestKey, SourcePath: "file:///library/poster.png", ImageType: "poster", Recoverable: true,
	}}
	handler := NewArtworkHandler(store, signer)
	handler.SetResilientDependencies(targets, nil, nil)
	signed, err := signer.SignTarget(artworkTestTarget, "original", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimPrefix(signed.URL, artworkurl.RoutePrefix), "/")
	recorder := httptest.NewRecorder()
	handler.serve(recorder, httptest.NewRequest(http.MethodGet, signed.URL, nil), parts[0], parts[1])
	if recorder.Code != http.StatusOK || recorder.Header().Get("X-Silo-Artwork") != "source_fallback" {
		t.Fatalf("response = %d %q, want verified fallback", recorder.Code, recorder.Header().Get("X-Silo-Artwork"))
	}
	waitForRepairSignal(t, targets.signalCh)
	targets.mu.Lock()
	signals := targets.signals
	targets.mu.Unlock()
	if signals != 1 {
		t.Fatalf("durable missing signals = %d, want one", signals)
	}
}

func TestOpenVerifiedWarmHitDoesNotRehashObject(t *testing.T) {
	store, err := artworkstore.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.WriteImmutable(t.Context(), artworkTestKey, artworkTestBytes, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteImmutable(t.Context(), artworkTestRevision.ManifestKey, artworkTestRevision.ManifestJSON, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatal(err)
	}
	counting := &countingArtworkStore{ArtworkObjectStore: store, key: artworkTestKey}
	signer, err := artworkurl.NewSigner(artworkTestSecret, func() time.Duration { return artworkTestTTL })
	if err != nil {
		t.Fatal(err)
	}
	handler := NewArtworkHandler(counting, signer)
	first, err := handler.openVerified(t.Context(), artworkTestKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	if counting.readBytes != len(artworkTestBytes) {
		t.Fatalf("cold verification read %d bytes, want %d", counting.readBytes, len(artworkTestBytes))
	}
	second, err := handler.openVerified(t.Context(), artworkTestKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	if counting.readBytes != len(artworkTestBytes) {
		t.Fatalf("warm verification re-read object: bytes=%d", counting.readBytes)
	}
}

// expireVerifiedArtworkEntries ages every cached verification out without
// sleeping through the real TTL.
func expireVerifiedArtworkEntries(cache *verifiedArtworkCache) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	past := time.Now().Add(-time.Second)
	for key, entry := range cache.entries {
		entry.expiresAt = past
		cache.entries[key] = entry
	}
}

func TestVerifiedArtworkCacheDropsExpiredEntries(t *testing.T) {
	cache := newVerifiedArtworkCache(1 << 20)
	cache.put("validator", `"digest"`, 128)
	if etag, ok := cache.get("validator"); !ok || etag != `"digest"` {
		t.Fatalf("fresh entry = %q/%v, want the stored entity tag", etag, ok)
	}
	expireVerifiedArtworkEntries(cache)
	if _, ok := cache.get("validator"); ok {
		t.Fatal("expired verification was served")
	}
	if len(cache.entries) != 0 || cache.bytes != 0 {
		t.Fatalf("after expiry: entries=%d bytes=%d, want the entry evicted and its size reclaimed", len(cache.entries), cache.bytes)
	}
}

// The cached validator is metadata only, so bytes swapped in place under the
// same size and mtime would otherwise be trusted forever. Expiry is what
// forces delivery back to the manifest digest.
func TestOpenVerifiedRehashesAfterVerificationExpires(t *testing.T) {
	store, err := artworkstore.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.WriteImmutable(t.Context(), artworkTestKey, artworkTestBytes, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteImmutable(t.Context(), artworkTestRevision.ManifestKey, artworkTestRevision.ManifestJSON, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatal(err)
	}
	counting := &countingArtworkStore{ArtworkObjectStore: store, key: artworkTestKey}
	signer, err := artworkurl.NewSigner(artworkTestSecret, func() time.Duration { return artworkTestTTL })
	if err != nil {
		t.Fatal(err)
	}
	handler := NewArtworkHandler(counting, signer)

	first, err := handler.openVerified(t.Context(), artworkTestKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	if counting.readBytes != len(artworkTestBytes) {
		t.Fatalf("cold verification read %d bytes, want %d", counting.readBytes, len(artworkTestBytes))
	}

	expireVerifiedArtworkEntries(handler.verified)
	second, err := handler.openVerified(t.Context(), artworkTestKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	if counting.readBytes != 2*len(artworkTestBytes) {
		t.Fatalf("expired verification read %d bytes, want a second full re-hash of %d", counting.readBytes, 2*len(artworkTestBytes))
	}
}

func TestArtworkHandlerRejectsExpiredURL(t *testing.T) {
	router, _, signer, _ := newArtworkTestRig(t)

	// Signed three windows ago, so the quantized expiry is already behind us.
	expired := signArtworkURL(t, signer, time.Now().Add(-3*artworkTestTTL))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, expired, nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	// A browser must not be prompted for credentials over an <img> request.
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate = %q, want none", got)
	}
}

func TestArtworkHandlerRejectsForeignSecret(t *testing.T) {
	router, _, _, _ := newArtworkTestRig(t)

	foreign, err := artworkurl.NewSigner("a different cluster secret", func() time.Duration { return artworkTestTTL })
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, signArtworkURL(t, foreign, time.Now()), nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// The compat surface listens on its own port and cannot redirect a client to a
// root-relative native URL, so it serves those bytes through this entry point.
func TestServeArtworkURLServesOnlyArtworkRouteURLs(t *testing.T) {
	_, handler, signer, _ := newArtworkTestRig(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/abc/Images/Primary", nil)
	if handler.ServeArtworkURL(rec, req, "https://cdn.example/poster.webp") {
		t.Fatal("ServeArtworkURL claimed a remote provider URL")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("wrote %q for a URL it does not own", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	if !handler.ServeArtworkURL(rec, req, signArtworkURL(t, signer, time.Now())) {
		t.Fatal("ServeArtworkURL declined a signed artwork URL")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(artworkTestBytes) {
		t.Fatalf("body = %q, want the stored bytes", rec.Body.String())
	}
}

func TestArtworkCapabilityReportsDeliveryFacts(t *testing.T) {
	handler := NewArtworkCapabilityHandler("local", func() string { return "selected" })
	handler.SetResilientStatus(func() string { return "degraded" })

	rec := httptest.NewRecorder()
	handler.HandleCapability(rec, httptest.NewRequest(http.MethodGet, "/api/v1/artwork/capability", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var got artworkCapabilityResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}

	if got.StorageBackend != "local" {
		t.Fatalf("storage_backend = %q, want local", got.StorageBackend)
	}
	if got.StorageFormat != "artwork/v1" {
		t.Fatalf("storage_format = %q, want artwork/v1", got.StorageFormat)
	}
	if len(got.DeliveryModes) != 1 || got.DeliveryModes[0] != artworkDeliveryAPI {
		t.Fatalf("delivery_modes = %v, want [%s]", got.DeliveryModes, artworkDeliveryAPI)
	}
	if got.RemoteMaterialization != "selected" {
		t.Fatalf("remote_materialization = %q, want selected", got.RemoteMaterialization)
	}
	if got.DeliveryPolicy != "resilient" || got.StoreHealth != "degraded" || !got.AutomaticRecovery {
		t.Fatalf("resilient status = policy %q, health %q, recovery %v", got.DeliveryPolicy, got.StoreHealth, got.AutomaticRecovery)
	}
	if got.LocalSourcePolicy != "materialize" {
		t.Fatalf("local_source_policy = %q, want materialize", got.LocalSourcePolicy)
	}
	if !got.StorageManagement.Accounting || !got.StorageManagement.SafePurge || !got.StorageManagement.DirectLibraryFallback {
		t.Fatalf("storage_management = %#v", got.StorageManagement)
	}
	if want := []string{"original", "w780", "w500", "w300"}; !equalStrings(got.Variants["poster"], want) {
		t.Fatalf("poster variants = %v, want %v", got.Variants["poster"], want)
	}
	if want := []string{"original", "w1920", "w1280", "w300"}; !equalStrings(got.Variants["backdrop"], want) {
		t.Fatalf("backdrop variants = %v, want %v", got.Variants["backdrop"], want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
