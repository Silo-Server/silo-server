package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"net/http"
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
