package plugins

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A plugin has at most one installation. A direct archive link for a plugin
// that is already installed replaces it, and a second create for the same
// plugin, as when two servers auto-install it at once, is refused rather than
// adding a row that would fail every later install of the plugin.
func TestPluginInstallKeepsOneInstallationPerPluginDB(t *testing.T) {
	f := newArchiveLinkFixture(t)
	ctx := context.Background()

	first, err := f.service.InstallRemote(ctx, InstallArchiveRequest{ArchiveURL: f.url("1.0.0")})
	if err != nil {
		t.Fatalf("first InstallRemote() returned error: %v", err)
	}
	if _, err := f.service.InstallRemote(ctx, InstallArchiveRequest{ArchiveURL: f.url("2.0.0")}); err != nil {
		t.Fatalf("second InstallRemote() returned error: %v", err)
	}
	installed := f.rows(t)
	if len(installed) != 1 || installed[0].ID != first.Installation.ID || installed[0].Version != "2.0.0" {
		t.Fatalf("after two archive links: %d installations, want installation %d replaced by 2.0.0", len(installed), first.Installation.ID)
	}

	// The create path itself, reached by a second creator that also saw no
	// installation, must not add a row.
	if _, err := f.installer.InstallRemote(ctx, InstallArchiveRequest{ArchiveURL: f.url("1.0.0")}); !errors.Is(err, ErrPluginAlreadyInstalled) {
		t.Fatalf("second create for an installed plugin = %v, want ErrPluginAlreadyInstalled", err)
	}
	installed = f.rows(t)
	if len(installed) != 1 || installed[0].Version != "2.0.0" {
		t.Fatalf("after a refused second create: %d installations, want the 2.0.0 one alone", len(installed))
	}
	archive, err := f.store.GetArchive(ctx, installed[0].ID)
	if err != nil {
		t.Fatalf("stored archive: %v", err)
	}
	if string(archive.Bytes) != string(f.archives["2.0.0"]) {
		t.Fatal("the refused create changed the installed plugin's stored archive")
	}

	// An archive link that finishes downloading after another install of the
	// plugin landed is refused rather than replacing the newer release.
	slow := f.slowInstall(t, "3.0.0")
	if _, err := f.service.InstallRemote(ctx, InstallArchiveRequest{ArchiveURL: f.url("4.0.0")}); err != nil {
		t.Fatalf("InstallRemote(4.0.0) returned error: %v", err)
	}
	f.release["3.0.0"]()
	if err := <-slow; !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("archive link overtaken during its download = %v, want ErrInstallationChanged", err)
	}
	installed = f.rows(t)
	if len(installed) != 1 || installed[0].Version != "4.0.0" {
		t.Fatalf("after an overtaken archive link: %d installations, want the 4.0.0 one alone", len(installed))
	}

	// So is one whose installation was uninstalled during the download: the
	// install it was asked to replace is gone, which is a conflict, not a
	// missing resource.
	slow = f.slowInstall(t, "5.0.0")
	if err := f.store.Delete(ctx, installed[0].ID); err != nil {
		t.Fatal(err)
	}
	f.release["5.0.0"]()
	if err := <-slow; !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("archive link whose installation was removed during its download = %v, want ErrInstallationChanged", err)
	}
}

// archiveLinkFixture serves one test plugin's packages by version at
// /<version>.zip. Versions 3.0.0 and 5.0.0 download only once released.
type archiveLinkFixture struct {
	store     *InstallationStore
	installer *Installer
	service   *Service
	server    *httptest.Server
	pluginID  string
	archives  map[string][]byte
	started   map[string]chan struct{}
	release   map[string]func()
}

func newArchiveLinkFixture(t *testing.T) *archiveLinkFixture {
	t.Helper()
	pool := builtinGuardTestPool(t)
	f := &archiveLinkFixture{
		store:    NewInstallationStore(pool),
		pluginID: "test.unique-install" + time.Now().UTC().Format("-20060102150405.000000000"),
		archives: map[string][]byte{},
		started:  map[string]chan struct{}{},
		release:  map[string]func(){},
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE plugin_id = $1`, f.pluginID)
	})
	for _, version := range []string{"1.0.0", "2.0.0", "3.0.0", "4.0.0", "5.0.0"} {
		path := filepath.Join(t.TempDir(), "plugin.zip")
		writePluginArchive(t, path, testPluginManifest(t, f.pluginID, version))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f.archives[version] = data
	}
	held := map[string]chan struct{}{}
	for _, version := range []string{"3.0.0", "5.0.0"} {
		ch := make(chan struct{})
		f.started[version], held[version] = make(chan struct{}), ch
		f.release[version] = sync.OnceFunc(func() { close(ch) })
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := r.URL.Path[1 : len(r.URL.Path)-len(".zip")]
		if ch, ok := f.started[version]; ok {
			close(ch)
			<-held[version]
		}
		_, _ = w.Write(f.archives[version])
	}))
	// Close waits for handlers, so a download a failed test left held is
	// released first.
	t.Cleanup(func() {
		for _, release := range f.release {
			release()
		}
		f.server.Close()
	})
	events := []string{}
	f.installer = NewInstaller(f.store, InstallerOptions{BaseDir: t.TempDir(), HTTPClient: f.server.Client()})
	f.service = &Service{installations: f.store, installer: f.installer, host: &fakeServiceHost{events: &events}}
	return f
}

func (f *archiveLinkFixture) url(version string) string {
	return f.server.URL + "/" + version + ".zip"
}

func (f *archiveLinkFixture) rows(t *testing.T) []*Installation {
	t.Helper()
	installed, err := f.store.ListByPluginID(context.Background(), f.pluginID)
	if err != nil {
		t.Fatal(err)
	}
	return installed
}

// slowInstall starts an archive-link install of a held version and returns
// once its download has started.
func (f *archiveLinkFixture) slowInstall(t *testing.T, version string) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := f.service.InstallRemote(context.Background(), InstallArchiveRequest{ArchiveURL: f.url(version)})
		done <- err
	}()
	select {
	case <-f.started[version]:
	case err := <-done:
		t.Fatalf("InstallRemote(%s) finished before its download started: %v", version, err)
	}
	return done
}
