package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

const manifestReadAsset = "web/index.html"

// newManifestReadService installs a plugin whose binary is size bytes, with
// one packaged asset served by a static route, straight into an install dir,
// and returns a Service whose archive cache serves it from there.
func newManifestReadService(tb testing.TB, size int) (*Service, *Installation) {
	tb.Helper()

	binary := make([]byte, size)
	for i := range binary {
		binary[i] = byte(i)
	}
	sum := sha256.Sum256(binary)
	manifest := &pluginv1.PluginManifest{
		PluginId:       "silo.manifestread",
		Version:        "1.0.0",
		Checksum:       hex.EncodeToString(sum[:]),
		SiloApiVersion: DefaultSiloAPIVersion,
		SupportedPlatforms: []*pluginv1.SupportedPlatform{
			{Os: "linux", Arch: "amd64"},
		},
		Capabilities: []*pluginv1.CapabilityDescriptor{{
			Type: "metadata_provider.v1", Id: "metadb", DisplayName: "MetaDB",
		}},
		HttpRoutes: []*pluginv1.HttpRouteDescriptor{{
			Method: http.MethodGet, Path: "/" + manifestReadAsset, Access: "public", StaticAsset: true,
		}},
		Assets: []*pluginv1.PackagedAsset{{Path: manifestReadAsset}},
	}
	manifestBytes, err := protojson.Marshal(manifest)
	if err != nil {
		tb.Fatal(err)
	}

	installPath := filepath.Join(tb.TempDir(), "install-read", "plugin")
	if err := os.MkdirAll(filepath.Join(filepath.Dir(installPath), "web"), 0o755); err != nil {
		tb.Fatal(err)
	}
	for path, data := range map[string][]byte{
		installPath:                        binary,
		InstalledManifestPath(installPath): manifestBytes,
		filepath.Join(filepath.Dir(installPath), manifestReadAsset): []byte("<html></html>"),
	} {
		if err := os.WriteFile(path, data, 0o755); err != nil {
			tb.Fatal(err)
		}
	}

	installation := &Installation{
		ID: 7, PluginID: manifest.GetPluginId(), Version: manifest.GetVersion(),
		InstallPath: installPath, Enabled: true,
	}
	store := newFakeServiceInstallationStore(installation)
	return &Service{installations: store, archiveCache: NewArchiveCache(store), host: &fakeServiceHost{}}, installation
}

// countingHash is a sha256 that adds the length of everything it hashes to n.
type countingHash struct {
	hash.Hash
	n *atomic.Int64
}

func (h countingHash) Write(p []byte) (int, error) {
	h.n.Add(int64(len(p)))
	return h.Hash.Write(p)
}

// countHashedBytes makes cache count the binary bytes it hashes.
func countHashedBytes(cache *ArchiveCache) *atomic.Int64 {
	var n atomic.Int64
	cache.newHash = func() hash.Hash { return countingHash{Hash: sha256.New(), n: &n} }
	return &n
}

func TestServiceManifestReadsSkipRehashOfVerifiedBinary(t *testing.T) {
	const size = 1 << 20
	svc, installation := newManifestReadService(t, size)
	hashed := countHashedBytes(svc.archiveCache)
	ctx := t.Context()

	// The first read hashes the binary: this process has not verified it yet.
	if _, err := svc.ManifestForInstallation(ctx, installation.ID); err != nil {
		t.Fatal(err)
	}
	if got := hashed.Swap(0); got != size {
		t.Fatalf("first manifest read hashed %d bytes, want %d", got, size)
	}

	for _, read := range []struct {
		name string
		call func() error
	}{
		{"ManifestForInstallation", func() error {
			_, err := svc.ManifestForInstallation(ctx, installation.ID)
			return err
		}},
		{"RouteDescriptors", func() error {
			_, err := svc.RouteDescriptors(ctx, installation.ID)
			return err
		}},
		{"UserConfigSchema", func() error {
			_, err := svc.UserConfigSchema(ctx, installation.ID)
			return err
		}},
		{"ResolveAssetPath", func() error {
			_, err := svc.ResolveAssetPath(ctx, installation.ID, manifestReadAsset)
			return err
		}},
		{"networkAccessManifest", func() error {
			_, err := svc.networkAccessManifest(ctx, installation)
			return err
		}},
	} {
		if err := read.call(); err != nil {
			t.Fatalf("%s: %v", read.name, err)
		}
		if got := hashed.Swap(0); got != 0 {
			t.Errorf("warm %s hashed %d bytes, want 0", read.name, got)
		}
	}

	// A plugin asset request reads the manifest twice: once to match the
	// route, once to resolve the file.
	proxy := NewHTTPProxyWithTypedResolver(svc, nil)
	recorder := httptest.NewRecorder()
	proxy.ServeAsset(recorder, httptest.NewRequest(http.MethodGet, "/", nil), installation.ID, manifestReadAsset)
	if recorder.Code != http.StatusOK {
		t.Fatalf("asset request status = %d, want 200", recorder.Code)
	}
	if got := hashed.Swap(0); got != 0 {
		t.Errorf("warm plugin asset request hashed %d bytes, want 0", got)
	}

	// Launching executes the binary, so it hashes the binary every time.
	for range 2 {
		if _, err := svc.Start(ctx, installation.ID); err != nil {
			t.Fatal(err)
		}
		if got := hashed.Swap(0); got != size {
			t.Errorf("Start hashed %d bytes, want %d", got, size)
		}
	}
}

// BenchmarkInstalledPluginWarmRead reads an installed plugin with a 16 MiB
// binary that this process has already verified. manifest is the call the
// admin plugin list makes per installation on every load; ensure is the
// ArchiveCache.Ensure check every plugin start makes before it executes the
// binary, which still hashes the whole binary. It does not start a process.
func BenchmarkInstalledPluginWarmRead(b *testing.B) {
	svc, installation := newManifestReadService(b, 16<<20)
	ctx := context.Background()
	if _, err := svc.ManifestForInstallation(ctx, installation.ID); err != nil {
		b.Fatal(err)
	}
	b.Run("manifest", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := svc.ManifestForInstallation(ctx, installation.ID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ensure", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := svc.archiveCache.Ensure(ctx, installation); err != nil {
				b.Fatal(err)
			}
		}
	})
}
