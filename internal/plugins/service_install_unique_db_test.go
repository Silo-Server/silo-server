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
	pool := builtinGuardTestPool(t)
	ctx := context.Background()
	store := NewInstallationStore(pool)
	pluginID := "test.unique-install" + time.Now().UTC().Format("-20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE plugin_id = $1`, pluginID)
	})

	archives := map[string][]byte{}
	for _, version := range []string{"1.0.0", "2.0.0", "3.0.0", "4.0.0"} {
		path := filepath.Join(t.TempDir(), "plugin.zip")
		writePluginArchive(t, path, testPluginManifest(t, pluginID, version))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		archives["/"+version+".zip"] = data
	}
	slowStarted, releaseSlow := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/3.0.0.zip" {
			close(slowStarted)
			<-releaseSlow
		}
		_, _ = w.Write(archives[r.URL.Path])
	}))
	t.Cleanup(server.Close)
	release := sync.OnceFunc(func() { close(releaseSlow) })
	t.Cleanup(release)

	events := []string{}
	installer := NewInstaller(store, InstallerOptions{BaseDir: t.TempDir(), HTTPClient: server.Client()})
	service := &Service{installations: store, installer: installer, host: &fakeServiceHost{events: &events}}

	rows := func() []*Installation {
		t.Helper()
		installed, err := store.ListByPluginID(ctx, pluginID)
		if err != nil {
			t.Fatal(err)
		}
		return installed
	}

	first, err := service.InstallRemote(ctx, InstallArchiveRequest{ArchiveURL: server.URL + "/1.0.0.zip"})
	if err != nil {
		t.Fatalf("first InstallRemote() returned error: %v", err)
	}
	if _, err := service.InstallRemote(ctx, InstallArchiveRequest{ArchiveURL: server.URL + "/2.0.0.zip"}); err != nil {
		t.Fatalf("second InstallRemote() returned error: %v", err)
	}
	installed := rows()
	if len(installed) != 1 || installed[0].ID != first.Installation.ID || installed[0].Version != "2.0.0" {
		t.Fatalf("after two archive links: %d installations, want installation %d replaced by 2.0.0", len(installed), first.Installation.ID)
	}

	// The create path itself, reached by a second creator that also saw no
	// installation, must not add a row.
	if _, err := installer.InstallRemote(ctx, InstallArchiveRequest{ArchiveURL: server.URL + "/1.0.0.zip"}); !errors.Is(err, ErrPluginAlreadyInstalled) {
		t.Fatalf("second create for an installed plugin = %v, want ErrPluginAlreadyInstalled", err)
	}
	installed = rows()
	if len(installed) != 1 || installed[0].Version != "2.0.0" {
		t.Fatalf("after a refused second create: %d installations, want the 2.0.0 one alone", len(installed))
	}
	archive, err := store.GetArchive(ctx, installed[0].ID)
	if err != nil {
		t.Fatalf("stored archive: %v", err)
	}
	if string(archive.Bytes) != string(archives["/2.0.0.zip"]) {
		t.Fatal("the refused create changed the installed plugin's stored archive")
	}

	// An archive link that finishes downloading after another install of the
	// plugin landed is refused rather than replacing the newer release.
	slow := make(chan error, 1)
	go func() {
		_, err := service.InstallRemote(ctx, InstallArchiveRequest{ArchiveURL: server.URL + "/3.0.0.zip"})
		slow <- err
	}()
	select {
	case <-slowStarted:
	case err := <-slow:
		t.Fatalf("InstallRemote(3.0.0) finished before its download started: %v", err)
	}
	if _, err := service.InstallRemote(ctx, InstallArchiveRequest{ArchiveURL: server.URL + "/4.0.0.zip"}); err != nil {
		t.Fatalf("InstallRemote(4.0.0) returned error: %v", err)
	}
	release()
	if err := <-slow; !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("archive link overtaken during its download = %v, want ErrInstallationChanged", err)
	}
	installed = rows()
	if len(installed) != 1 || installed[0].Version != "4.0.0" {
		t.Fatalf("after an overtaken archive link: %d installations, want the 4.0.0 one alone", len(installed))
	}
}
