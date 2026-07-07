package plugins

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestAutoUpdateServiceCheckReplacesInstalledPluginsInPlace(t *testing.T) {
	installations := &fakeAutoUpdateInstallations{
		list: []*Installation{
			{ID: 41, PluginID: "silo.tmdb", Version: "1.0.0", UpdatePolicy: "auto", Enabled: true},
		},
	}
	host := &fakeAutoUpdateHost{}
	installer := &fakeAutoUpdateInstaller{}
	catalog := &fakeAutoUpdateCatalog{
		entries: []CatalogEntry{
			{
				RepositoryID: 7,
				Manifest: &pluginv1.PluginManifest{
					PluginId: "silo.tmdb",
					Version:  "1.1.0",
				},
			},
		},
		resolved: &ResolvedCatalogInstall{
			RepositoryID: 7,
			ArchiveURL:   "https://plugins.example.test/tmdb",
			Checksum:     "deadbeef",
		},
	}
	service := NewAutoUpdateService(
		&fakeAutoUpdateRepositories{list: []*Repository{{ID: 7, Enabled: true}}},
		installations,
		catalog,
		installer,
		host,
		nil,
		nil,
	)

	summary, err := service.Check(context.Background(), AutoUpdateOptions{
		SeedDefaultRepository: true,
		AutoInstallDefaults:   false,
	})
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if summary.UpdatesApplied != 1 {
		t.Fatalf("UpdatesApplied = %d, want 1", summary.UpdatesApplied)
	}
	if len(installations.deletedIDs) != 0 {
		t.Fatalf("deleted installation IDs = %#v, want none", installations.deletedIDs)
	}
	if len(installer.replaceBinary) != 1 {
		t.Fatalf("replace binary calls = %d, want 1", len(installer.replaceBinary))
	}
	if installer.replaceBinary[0].existingID != 41 {
		t.Fatalf("replace binary existing_id = %d, want 41", installer.replaceBinary[0].existingID)
	}
	if len(host.stopped) != 1 || host.stopped[0] != 41 {
		t.Fatalf("stopped installations = %#v, want [41]", host.stopped)
	}
}

func TestAutoUpdateServiceCheckFiresOnChangeAfterMutation(t *testing.T) {
	newOnChangeService := func(installations *fakeAutoUpdateInstallations, opts AutoUpdateOptions, calls *int) (AutoUpdateSummary, error) {
		catalog := &fakeAutoUpdateCatalog{
			entries: []CatalogEntry{{
				RepositoryID: 7,
				Manifest: &pluginv1.PluginManifest{
					PluginId: "silo.tmdb",
					Version:  "1.1.0",
				},
			}},
			resolved: &ResolvedCatalogInstall{
				RepositoryID: 7,
				ArchiveURL:   "https://plugins.example.test/tmdb",
				Checksum:     "deadbeef",
			},
		}
		service := NewAutoUpdateService(
			&fakeAutoUpdateRepositories{list: []*Repository{{ID: 7, Enabled: true}}},
			installations,
			catalog,
			&fakeAutoUpdateInstaller{},
			&fakeAutoUpdateHost{},
			nil,
			func(context.Context) { *calls++ },
		)
		return service.Check(context.Background(), opts)
	}

	t.Run("applied auto-update fires onChange", func(t *testing.T) {
		var calls int
		installations := &fakeAutoUpdateInstallations{
			list: []*Installation{{ID: 41, PluginID: "silo.tmdb", Version: "1.0.0", UpdatePolicy: "auto", Enabled: true}},
		}
		summary, err := newOnChangeService(installations, AutoUpdateOptions{}, &calls)
		if err != nil {
			t.Fatalf("Check() returned error: %v", err)
		}
		if summary.UpdatesApplied != 1 {
			t.Fatalf("UpdatesApplied = %d, want 1", summary.UpdatesApplied)
		}
		if calls != 1 {
			t.Fatalf("onChange calls = %d, want 1", calls)
		}
	})

	t.Run("notify update fires onChange", func(t *testing.T) {
		var calls int
		installations := &fakeAutoUpdateInstallations{
			list: []*Installation{{ID: 42, PluginID: "silo.tmdb", Version: "1.0.0", UpdatePolicy: "notify", Enabled: true}},
		}
		summary, err := newOnChangeService(installations, AutoUpdateOptions{}, &calls)
		if err != nil {
			t.Fatalf("Check() returned error: %v", err)
		}
		if summary.UpdatesAvailable != 1 {
			t.Fatalf("UpdatesAvailable = %d, want 1", summary.UpdatesAvailable)
		}
		if calls != 1 {
			t.Fatalf("onChange calls = %d, want 1", calls)
		}
	})

	t.Run("no mutation does not fire onChange", func(t *testing.T) {
		var calls int
		installations := &fakeAutoUpdateInstallations{
			list: []*Installation{{ID: 43, PluginID: "silo.tmdb", Version: "1.1.0", UpdatePolicy: "auto", Enabled: true}},
		}
		summary, err := newOnChangeService(installations, AutoUpdateOptions{}, &calls)
		if err != nil {
			t.Fatalf("Check() returned error: %v", err)
		}
		if summary.UpdatesApplied != 0 || summary.UpdatesAvailable != 0 {
			t.Fatalf("unexpected mutation summary: %+v", summary)
		}
		if calls != 0 {
			t.Fatalf("onChange calls = %d, want 0", calls)
		}
	})

	t.Run("nil onChange is safe after mutation", func(t *testing.T) {
		installations := &fakeAutoUpdateInstallations{
			list: []*Installation{{ID: 44, PluginID: "silo.tmdb", Version: "1.0.0", UpdatePolicy: "auto", Enabled: true}},
		}
		service := NewAutoUpdateService(
			&fakeAutoUpdateRepositories{list: []*Repository{{ID: 7, Enabled: true}}},
			installations,
			&fakeAutoUpdateCatalog{
				entries: []CatalogEntry{{
					RepositoryID: 7,
					Manifest:     &pluginv1.PluginManifest{PluginId: "silo.tmdb", Version: "1.1.0"},
				}},
				resolved: &ResolvedCatalogInstall{RepositoryID: 7, ArchiveURL: "https://plugins.example.test/tmdb", Checksum: "deadbeef"},
			},
			&fakeAutoUpdateInstaller{},
			&fakeAutoUpdateHost{},
			nil,
			nil,
		)
		summary, err := service.Check(context.Background(), AutoUpdateOptions{})
		if err != nil {
			t.Fatalf("Check() with nil onChange returned error: %v", err)
		}
		if summary.UpdatesApplied != 1 {
			t.Fatalf("UpdatesApplied = %d, want 1", summary.UpdatesApplied)
		}
	})
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.2.9", "1.2.9", 0},
		{"1.2.10", "1.2.9", 1},
		{"1.2.9", "1.2.10", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.0.0", "1.1.0", -1},
		{"1.10.0", "1.9.0", 1},
		{"0.0.30", "0.0.29", 1},
		{"1.2.3", "1.2.3.1", -1},
	}
	for _, tt := range tests {
		got := compareVersions(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestAutoUpdateMultiDigitVersion(t *testing.T) {
	installations := &fakeAutoUpdateInstallations{
		list: []*Installation{{ID: 50, PluginID: "silo.tmdb", Version: "1.2.9", UpdatePolicy: "auto", Enabled: true}},
	}
	host := &fakeAutoUpdateHost{}
	installer := &fakeAutoUpdateInstaller{}
	catalog := &fakeAutoUpdateCatalog{
		entries: []CatalogEntry{{
			RepositoryID: 7,
			Manifest: &pluginv1.PluginManifest{
				PluginId: "silo.tmdb",
				Version:  "1.2.10",
			},
		}},
		resolved: &ResolvedCatalogInstall{
			RepositoryID: 7,
			ArchiveURL:   "https://plugins.example.test/tmdb",
			Checksum:     "deadbeef",
		},
	}
	service := NewAutoUpdateService(
		&fakeAutoUpdateRepositories{list: []*Repository{{ID: 7, Enabled: true}}},
		installations,
		catalog,
		installer,
		host,
		nil,
		nil,
	)

	summary, err := service.Check(context.Background(), AutoUpdateOptions{
		SeedDefaultRepository: true,
		AutoInstallDefaults:   false,
	})
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if summary.UpdatesApplied != 1 {
		t.Fatalf("UpdatesApplied = %d, want 1 (1.2.10 should be newer than 1.2.9)", summary.UpdatesApplied)
	}
}

type fakeAutoUpdateRepositories struct {
	list []*Repository
}

func (f *fakeAutoUpdateRepositories) List(context.Context) ([]*Repository, error) {
	return f.list, nil
}

func (f *fakeAutoUpdateRepositories) Create(_ context.Context, input CreateRepositoryInput) (*Repository, error) {
	repo := &Repository{
		ID:          len(f.list) + 1,
		URL:         input.URL,
		DisplayName: input.DisplayName,
		Enabled:     input.Enabled == nil || *input.Enabled,
	}
	f.list = append(f.list, repo)
	return repo, nil
}

type fakeAutoUpdateInstallations struct {
	list       []*Installation
	updates    []UpdateInstallationInput
	deletedIDs []int
}

func (f *fakeAutoUpdateInstallations) List(context.Context) ([]*Installation, error) {
	return f.list, nil
}

func (f *fakeAutoUpdateInstallations) Update(_ context.Context, _ int, input UpdateInstallationInput) error {
	f.updates = append(f.updates, input)
	return nil
}

func (f *fakeAutoUpdateInstallations) Delete(_ context.Context, id int) error {
	f.deletedIDs = append(f.deletedIDs, id)
	return nil
}

type fakeAutoUpdateCatalog struct {
	entries  []CatalogEntry
	resolved *ResolvedCatalogInstall
}

func (f *fakeAutoUpdateCatalog) Fetch(context.Context) ([]CatalogEntry, error) {
	return f.entries, nil
}

func (f *fakeAutoUpdateCatalog) ResolveInstall(_ context.Context, _ InstallCatalogRequest) (*ResolvedCatalogInstall, error) {
	return f.resolved, nil
}

type replaceBinaryCall struct {
	existingID int
	req        InstallBinaryRequest
}

type replaceRemoteCall struct {
	existingID int
	req        InstallArchiveRequest
}

type fakeAutoUpdateInstaller struct {
	replaceBinary []replaceBinaryCall
	replaceRemote []replaceRemoteCall
	binary        []InstallBinaryRequest
	remote        []InstallArchiveRequest
}

func (f *fakeAutoUpdateInstaller) InstallRemote(_ context.Context, req InstallArchiveRequest) (*InstallResult, error) {
	f.remote = append(f.remote, req)
	return &InstallResult{}, nil
}

func (f *fakeAutoUpdateInstaller) InstallBinary(_ context.Context, req InstallBinaryRequest) (*InstallResult, error) {
	f.binary = append(f.binary, req)
	return &InstallResult{}, nil
}

func (f *fakeAutoUpdateInstaller) ReplaceRemote(_ context.Context, existing *Installation, req InstallArchiveRequest) (*InstallResult, error) {
	f.replaceRemote = append(f.replaceRemote, replaceRemoteCall{existingID: existing.ID, req: req})
	return &InstallResult{Installation: existing}, nil
}

func (f *fakeAutoUpdateInstaller) ReplaceBinary(_ context.Context, existing *Installation, req InstallBinaryRequest) (*InstallResult, error) {
	f.replaceBinary = append(f.replaceBinary, replaceBinaryCall{existingID: existing.ID, req: req})
	return &InstallResult{Installation: existing}, nil
}

type fakeAutoUpdateHost struct {
	stopped []int
}

func (f *fakeAutoUpdateHost) Stop(installationID int) error {
	f.stopped = append(f.stopped, installationID)
	return nil
}
