package plugins

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestAutoUpdateDefaultsInstallTheIntroDBWithoutReenablingExistingInstallations(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new installation"
		if existing {
			name = "existing disabled installation"
		}
		t.Run(name, func(t *testing.T) {
			installations := &fakeAutoUpdateInstallations{}
			if existing {
				installations.list = []*Installation{{PluginID: "silo.theintrodb", Enabled: false}}
			}
			catalog := &fakeAutoUpdateCatalog{
				entries: []CatalogEntry{{
					RepositoryID: 7,
					SourceKind:   RepositorySourceSilo,
					Manifest: &pluginv1.PluginManifest{
						PluginId: "silo.theintrodb",
						Version:  "1.0.0",
					},
				}},
				resolved: &ResolvedCatalogInstall{
					RepositoryID: 7,
					ArchiveURL:   "https://plugins.example.test/theintrodb",
					Checksum:     "test-checksum",
				},
			}
			installer := &fakeAutoUpdateInstaller{}
			service := NewAutoUpdateService(&fakeAutoUpdateRepositories{}, installations, catalog, installer, nil, nil, nil)
			summary, err := service.Check(t.Context(), AutoUpdateOptions{AutoInstallDefaults: true})
			if err != nil {
				t.Fatal(err)
			}
			if existing {
				if summary.DefaultPluginsInstalled != 0 || len(installer.binary) != 0 || len(installations.updates) != 0 {
					t.Fatal("default installation changed an existing disabled plugin")
				}
				return
			}
			if summary.DefaultPluginsInstalled != 1 || len(installer.binary) != 1 {
				t.Fatalf("TheIntroDB installs = %d, binary requests = %d, want 1 each", summary.DefaultPluginsInstalled, len(installer.binary))
			}
			if len(catalog.resolveRequests) != 1 || catalog.resolveRequests[0].PluginID != "silo.theintrodb" {
				t.Fatalf("resolved plugins = %+v, want TheIntroDB", catalog.resolveRequests)
			}
		})
	}
}
