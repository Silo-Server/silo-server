package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// memorySettings is the durable surface the artwork store pin needs, without a
// database.
type memorySettings struct {
	mu     sync.Mutex
	values map[string]string
}

type staticArtworkTargets struct {
	selectedPath string
}

func (s staticArtworkTargets) LoadTarget(_ context.Context, target artworkurl.Target) (metadata.ArtworkTargetState, error) {
	return metadata.ArtworkTargetState{Target: target, SelectedPath: s.selectedPath, ImageType: target.Slot}, nil
}

func (staticArtworkTargets) SignalMissing(context.Context, metadata.ArtworkTargetState) error {
	return nil
}

func (staticArtworkTargets) ReadSidecar(context.Context, metadata.ArtworkTargetState) (metadata.ConfinedLocalArtwork, error) {
	return metadata.ConfinedLocalArtwork{}, nil
}

func (s *memorySettings) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}

func (s *memorySettings) SetIfAbsent(_ context.Context, key, value string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.values[key]; ok {
		return false, nil
	}
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return true, nil
}

// The artwork route has to be reachable with no Authorization header at all: a
// browser <img> element cannot attach one, and the signature in the URL is the
// capability that replaces it.
func TestArtworkRouteIsPublicAndSignedWhenArtworkIsStoredLocally(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}

	settings := &memorySettings{values: map[string]string{}}
	handle, err := artworkstore.Open(t.Context(), artworkstore.Options{
		Backend:   artworkstore.BackendLocal,
		LocalPath: t.TempDir(),
		Settings:  settings,
	})
	if err != nil {
		t.Fatalf("opening the artwork store: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	payload := []byte("stored artwork bytes")
	revision, err := artworkkey.BuildPortableRevision(artworkkey.RevisionInput{
		ImageType: artworkkey.ImageTypePoster,
		MediaType: "image/webp",
		Ext:       ".webp",
		Variants:  []artworkkey.VariantBytes{{Name: artworkkey.OriginalVariant, Data: payload}},
	})
	if err != nil {
		t.Fatalf("BuildPortableRevision: %v", err)
	}
	key := revision.OriginalKey
	if err := handle.Store.WriteImmutable(t.Context(), key, payload, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	if err := handle.Store.WriteImmutable(t.Context(), revision.ManifestKey, revision.ManifestJSON, artworkstore.ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable(manifest): %v", err)
	}

	signer, err := artworkurl.NewSigner("cluster-secret", func() time.Duration { return time.Hour })
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	resolver, err := artworkurl.NewResolver(signer)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	routerDelivery := handlers.NewArtworkHandler(handle.Store, signer)
	routerDelivery.SetResilientDependencies(staticArtworkTargets{selectedPath: key}, nil, nil)
	router := NewRouter(Dependencies{
		Config:           cfg,
		ArtworkStore:     handle,
		ArtworkURLSigner: signer,
		ArtworkURLs:      resolver,
		ArtworkDelivery:  routerDelivery,
	})

	signed, err := resolver.ResolveTargetURL(t.Context(), artworkurl.Target{
		Surface: artworkurl.SurfaceItemPosters, Keys: []string{"movie-1"}, Slot: "poster",
	}.WithReference(key), "original")
	if err != nil {
		t.Fatalf("ResolveTargetURL: %v", err)
	}
	if !strings.HasPrefix(signed.URL, artworkurl.RoutePrefix) {
		t.Fatalf("resolved URL = %q, want a signed target-bound artwork URL", signed.URL)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, signed.URL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(payload) {
		t.Fatalf("body = %q, want the stored bytes", rec.Body.String())
	}

	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest(http.MethodHead, signed.URL, nil))
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", head.Code)
	}
}

// The capability probe is a static sibling of the delivery route's key
// parameter. Registering them in that order on one tree is what makes both
// reachable; a shadowed capability route would have clients concluding the
// server is too old to describe its own artwork storage.
func TestArtworkCapabilityAndDeliveryRoutesCoexist(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{"auth.jwt_secret": "Y2x1c3Rlci1zZWNyZXQ="})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	settings := &memorySettings{values: map[string]string{}}
	handle, err := artworkstore.Open(t.Context(), artworkstore.Options{
		Backend:   artworkstore.BackendLocal,
		LocalPath: t.TempDir(),
		Settings:  settings,
	})
	if err != nil {
		t.Fatalf("opening the artwork store: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	signer, err := artworkurl.NewSigner("cluster-secret", func() time.Duration { return time.Hour })
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	resolver, err := artworkurl.NewResolver(signer)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	router := NewRouter(Dependencies{
		DB:               pool,
		Config:           cfg,
		FileRepo:         scanner.NewFileRepository(pool),
		FolderRepo:       catalog.NewFolderRepository(pool),
		SessionMgr:       playback.NewSessionManager(0, 0),
		ArtworkStore:     handle,
		ArtworkURLSigner: signer,
		ArtworkURLs:      resolver,
	})

	registered := map[string]bool{}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatalf("walking routes: %v", err)
	}

	for _, want := range []string{
		"GET /api/v1/artwork/capability",
		"GET /api/v1/artwork/{capability}/{variant}",
		"HEAD /api/v1/artwork/{capability}/{variant}",
		"GET /api/v1/artwork-library/{identity}",
		"HEAD /api/v1/artwork-library/{identity}",
	} {
		if !registered[want] {
			t.Fatalf("route %q is not registered", want)
		}
	}
	for _, removed := range []string{"GET /api/v1/artwork/*", "HEAD /api/v1/artwork/*"} {
		if registered[removed] {
			t.Fatalf("removed raw-key route %q is still registered", removed)
		}
	}
}

func TestS3ResolverDefaultsToTargetBoundResilientRoute(t *testing.T) {
	signer, err := artworkurl.NewSigner("cluster-secret", func() time.Duration { return time.Hour })
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	resolver, err := artworkurl.NewResolver(signer)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	const key = "artwork/v1/objects/poster/ab/abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789/original.webp"
	signed, err := resolver.ResolveTargetURL(t.Context(), artworkurl.Target{
		Surface: artworkurl.SurfaceItemPosters, Keys: []string{"movie-1"}, Slot: "poster",
	}.WithReference(key), "original")
	if err != nil {
		t.Fatalf("ResolveTargetURL: %v", err)
	}
	if !strings.HasPrefix(signed.URL, artworkurl.RoutePrefix) {
		t.Fatalf("URL = %q, want resilient route", signed.URL)
	}
}
