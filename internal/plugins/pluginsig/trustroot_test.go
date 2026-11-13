package pluginsig

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// With no cached metadata and the mirror unreachable there is no trust root:
// nothing bundled stands in for TUF.
func TestTUFTrustRootFailsClosedWithoutCache(t *testing.T) {
	mirror := httptest.NewServer(http.NotFoundHandler())
	mirror.Close()
	cacheDir := filepath.Join(t.TempDir(), "tuf")

	trust := NewTUFTrustRoot(TUFTrustRootOptions{CacheDir: cacheDir, Transport: http.DefaultTransport, RepositoryURL: mirror.URL})
	if trusted, err := trust.TrustedMaterial(context.Background()); err == nil {
		t.Fatalf("TrustedMaterial() = %v, want an error with TUF unreachable and no cache", trusted)
	}
	info, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("cache dir mode = %v, want 0700", perm)
	}
}

// After one refresh the cache serves verifications without the mirror until
// its metadata expires, even though the embedded root has expired and the
// repository has rotated past it, as sigstore-go's embedded root has. With
// the cached metadata expired and the mirror unreachable, it fails.
func TestTUFTrustRootServesCacheOffline(t *testing.T) {
	repo := newTestTUFRepo(t)
	server := httptest.NewServer(repo)
	t.Cleanup(server.Close)
	transport := &switchableTransport{}
	trust := NewTUFTrustRoot(TUFTrustRootOptions{
		CacheDir:      filepath.Join(t.TempDir(), "tuf"),
		Transport:     transport,
		RepositoryURL: server.URL,
		embeddedRoot:  repo.files["/1.root.json"],
	})

	if _, err := trust.TrustedMaterial(context.Background()); err != nil {
		t.Fatalf("first TrustedMaterial() error = %v", err)
	}
	transport.offline.Store(true)
	if _, err := trust.TrustedMaterial(context.Background()); err != nil {
		t.Fatalf("offline TrustedMaterial() with an unexpired cache error = %v", err)
	}

	// The cached timestamp expires: a timestamp the repository's own key
	// signed, as a later refresh would have persisted it.
	expired := metadata.Timestamp(time.Now().Add(-time.Minute))
	expired.Signed.Version = 2
	expired.Signed.Meta["snapshot.json"] = metadata.MetaFile(1)
	cachedTimestamp := filepath.Join(trust.opts.CacheDir, tuf.URLToPath(server.URL), "timestamp.json")
	if err := os.WriteFile(cachedTimestamp, repo.sign(t, expired), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := trust.TrustedMaterial(context.Background()); err == nil {
		t.Fatal("offline TrustedMaterial() with expired cached metadata succeeded, want it to fail closed")
	}
}

// A verification whose context ends stops waiting on a mirror that doesn't
// answer, instead of holding the cache lock until the fetch timeout.
func TestTUFTrustRootStopsWithItsContext(t *testing.T) {
	arrived := make(chan struct{}, 1)
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	t.Cleanup(mirror.Close)
	trust := NewTUFTrustRoot(TUFTrustRootOptions{CacheDir: filepath.Join(t.TempDir(), "tuf"), Transport: http.DefaultTransport, RepositoryURL: mirror.URL})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := trust.TrustedMaterial(ctx)
		done <- err
	}()
	<-arrived
	cancel()
	// A fetcher that ignores the context returns only at tufFetchTimeout.
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("TrustedMaterial() error = %v, want context.Canceled", err)
		}
	case <-time.After(tufFetchTimeout / 2):
		t.Fatal("TrustedMaterial() kept waiting on the mirror after its context was canceled")
	}
}

type switchableTransport struct{ offline atomic.Bool }

func (s *switchableTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if s.offline.Load() {
		return nil, errors.New("offline")
	}
	return http.DefaultTransport.RoundTrip(r)
}

// testTUFRepo is a TUF repository without consistent snapshots whose root
// v1 has expired and was rotated to v2, serving trusted_root.json.
type testTUFRepo struct {
	signer signature.Signer
	files  map[string][]byte
}

type signable interface {
	Sign(signature.Signer) (*metadata.Signature, error)
	ToBytes(bool) ([]byte, error)
}

func (r *testTUFRepo) sign(t *testing.T, m signable) []byte {
	t.Helper()
	if _, err := m.Sign(r.signer); err != nil {
		t.Fatal(err)
	}
	data, err := m.ToBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newTestTUFRepo(t *testing.T) *testTUFRepo {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := signature.LoadECDSASigner(priv, crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	key, err := metadata.KeyFromPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	repo := &testTUFRepo{signer: signer, files: map[string][]byte{}}
	valid := time.Now().Add(24 * time.Hour)

	rootAt := func(version int64, expires time.Time) *metadata.Metadata[metadata.RootType] {
		root := metadata.Root(expires)
		root.Signed.Version = version
		root.Signed.ConsistentSnapshot = false
		for _, role := range []string{metadata.ROOT, metadata.TIMESTAMP, metadata.SNAPSHOT, metadata.TARGETS} {
			if err := root.Signed.AddKey(key, role); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	repo.files["/1.root.json"] = repo.sign(t, rootAt(1, time.Now().Add(-time.Hour)))
	repo.files["/2.root.json"] = repo.sign(t, rootAt(2, valid))

	trustedRoot := []byte(`{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1"}`)
	targets := metadata.Targets(valid)
	target, err := metadata.TargetFile().FromBytes("trusted_root.json", trustedRoot, "sha256")
	if err != nil {
		t.Fatal(err)
	}
	targets.Signed.Targets["trusted_root.json"] = target
	repo.files["/targets.json"] = repo.sign(t, targets)
	repo.files["/targets/trusted_root.json"] = trustedRoot

	snapshot := metadata.Snapshot(valid)
	snapshot.Signed.Meta["targets.json"] = metadata.MetaFile(1)
	repo.files["/snapshot.json"] = repo.sign(t, snapshot)

	timestamp := metadata.Timestamp(valid)
	timestamp.Signed.Meta["snapshot.json"] = metadata.MetaFile(1)
	repo.files["/timestamp.json"] = repo.sign(t, timestamp)
	return repo
}

func (r *testTUFRepo) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	data, ok := r.files[req.URL.Path]
	if !ok {
		http.NotFound(w, req)
		return
	}
	_, _ = w.Write(data)
}
