package plugins

import (
	"context"
	"errors"
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

func TestAutoUpdateInstallsRequiredPluginsFromTheSiloRepository(t *testing.T) {
	entry := func(repositoryID int, source, pluginID string) CatalogEntry {
		return CatalogEntry{
			RepositoryID: repositoryID,
			SourceKind:   source,
			Manifest:     &pluginv1.PluginManifest{PluginId: pluginID, Version: "1.0.0"},
		}
	}
	catalog := &fakeAutoUpdateCatalog{
		entries: []CatalogEntry{
			entry(7, RepositorySourceSilo, "silo.watchprovider.trakt"),
			// A plugin of the same ID from an admin-added repository must not
			// be what a required install picks.
			entry(9, RepositorySourceExternal, "silo.watchprovider.simkl"),
		},
		resolved: &ResolvedCatalogInstall{
			RepositoryID: 7,
			ArchiveURL:   "https://plugins.example.test/trakt",
			Checksum:     "test-checksum",
		},
	}
	// The defaults are installed already, so only required plugins remain.
	installations := &fakeAutoUpdateInstallations{list: []*Installation{
		{PluginID: pluginIDTMDB, Enabled: true},
		{PluginID: "silo.tvdb", Enabled: true},
		{PluginID: "silo.theintrodb", Enabled: true},
	}}
	installer := &fakeAutoUpdateInstaller{}
	service := NewAutoUpdateService(&fakeAutoUpdateRepositories{}, installations, catalog, installer, nil, nil, nil)
	service.SetRequiredPlugins(func(context.Context) ([]string, error) {
		return []string{"silo.watchprovider.trakt", "silo.watchprovider.simkl"}, nil
	})
	summary, err := service.Check(t.Context(), AutoUpdateOptions{AutoInstallDefaults: true})
	if err != nil {
		t.Fatal(err)
	}
	if summary.DefaultPluginsInstalled != 1 || len(catalog.resolveRequests) != 1 ||
		catalog.resolveRequests[0].PluginID != "silo.watchprovider.trakt" || catalog.resolveRequests[0].RepositoryID != 7 {
		t.Fatalf("installs = %d, resolved = %+v, want only Trakt from the Silo repository", summary.DefaultPluginsInstalled, catalog.resolveRequests)
	}
}

func TestAutoUpdateKeepsInstallingDefaultsWhenRequiredPluginsFail(t *testing.T) {
	catalog := &fakeAutoUpdateCatalog{
		entries: []CatalogEntry{{
			RepositoryID: 7, SourceKind: RepositorySourceSilo,
			Manifest: &pluginv1.PluginManifest{PluginId: "silo.theintrodb", Version: "1.0.0"},
		}},
		resolved: &ResolvedCatalogInstall{RepositoryID: 7, ArchiveURL: "https://plugins.example.test/theintrodb", Checksum: "test-checksum"},
	}
	installer := &fakeAutoUpdateInstaller{}
	service := NewAutoUpdateService(&fakeAutoUpdateRepositories{}, &fakeAutoUpdateInstallations{}, catalog, installer, nil, nil, nil)
	service.SetRequiredPlugins(func(context.Context) ([]string, error) {
		return nil, errors.New("database unavailable")
	})
	summary, err := service.Check(t.Context(), AutoUpdateOptions{AutoInstallDefaults: true})
	if err != nil {
		t.Fatal(err)
	}
	if summary.FailedOperations != 1 {
		t.Fatalf("failed operations = %d, want the required-plugin lookup recorded", summary.FailedOperations)
	}
	if summary.DefaultPluginsInstalled != 1 || len(installer.binary) != 1 {
		t.Fatalf("default installs = %d, binary requests = %d, want 1 each", summary.DefaultPluginsInstalled, len(installer.binary))
	}
}

// The daily update check runs without default installs. Required plugins still
// install there, so a catalog that was unreachable at startup does not leave
// them missing until the next restart.
func TestAutoUpdateInstallsRequiredPluginsOnLaterChecks(t *testing.T) {
	catalog := &fakeAutoUpdateCatalog{
		entries: []CatalogEntry{
			{RepositoryID: 7, SourceKind: RepositorySourceSilo, Manifest: &pluginv1.PluginManifest{PluginId: "silo.theintrodb", Version: "1.0.0"}},
			{RepositoryID: 7, SourceKind: RepositorySourceSilo, Manifest: &pluginv1.PluginManifest{PluginId: "silo.watchprovider.simkl", Version: "0.2.0"}},
		},
		resolved: &ResolvedCatalogInstall{RepositoryID: 7, ArchiveURL: "https://plugins.example.test/simkl", Checksum: "test-checksum"},
	}
	service := NewAutoUpdateService(&fakeAutoUpdateRepositories{}, &fakeAutoUpdateInstallations{}, catalog, &fakeAutoUpdateInstaller{}, nil, nil, nil)
	service.SetRequiredPlugins(func(context.Context) ([]string, error) {
		return []string{"silo.watchprovider.simkl"}, nil
	})
	summary, err := service.Check(t.Context(), AutoUpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.DefaultPluginsInstalled != 1 || len(catalog.resolveRequests) != 1 ||
		catalog.resolveRequests[0].PluginID != "silo.watchprovider.simkl" {
		t.Fatalf("installs = %d, resolved = %+v, want only the required Simkl plugin", summary.DefaultPluginsInstalled, catalog.resolveRequests)
	}
}
