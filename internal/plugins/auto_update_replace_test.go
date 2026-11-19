package plugins

import (
	"context"
	"errors"
	"slices"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// A refused replacement package leaves the running plugin alone.
func TestAutoUpdateServiceRefusedPackageLeavesPluginRunning(t *testing.T) {
	events := []string{}
	service := NewAutoUpdateService(
		&fakeAutoUpdateRepositories{list: []*Repository{{ID: 7, Enabled: true}}},
		&fakeAutoUpdateInstallations{list: []*Installation{{ID: 41, RepositoryID: pluginRepositoryID(7), PluginID: "silo.tmdb", Version: "1.0.0", UpdatePolicy: "auto", Enabled: true}}},
		&fakeAutoUpdateCatalog{
			entries:  []CatalogEntry{{RepositoryID: 7, Manifest: &pluginv1.PluginManifest{PluginId: "silo.tmdb", Version: "1.1.0"}}},
			resolved: &ResolvedCatalogInstall{RepositoryID: 7, ArchiveURL: "https://plugins.example.test/tmdb", Checksum: "deadbeef"},
		},
		&fakeAutoUpdateInstaller{replaceErr: packageError(errors.New("binary checksum mismatch"), "refused"), events: &events},
		&fakeAutoUpdateHost{events: &events},
		nil,
		nil,
	)
	if _, err := service.Check(context.Background(), AutoUpdateOptions{}); err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if want := []string{"prepare"}; !slices.Equal(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

// An automatic update whose installation an admin's install replaced while
// it downloaded leaves the newer release running and its refusal record
// alone: it is skipped, not a refusal to show in web admin.
func TestAutoUpdateServiceSkipsInstallationReplacedDuringDownload(t *testing.T) {
	events := []string{}
	installations := &fakeAutoUpdateInstallations{
		list: []*Installation{{ID: 41, RepositoryID: pluginRepositoryID(7), PluginID: "silo.tmdb", Version: "1.0.0", InstallPath: "/plugins/1.0.0/plugin", UpdatePolicy: "auto", Enabled: true,
			UpdateFailure: &UpdateFailure{Version: "1.1.0", Reason: "earlier refusal"}}},
		current: &Installation{ID: 41, RepositoryID: pluginRepositoryID(7), PluginID: "silo.tmdb", Version: "2.0.0", InstallPath: "/plugins/2.0.0/plugin", UpdatePolicy: "auto", Enabled: true},
	}
	service := NewAutoUpdateService(
		&fakeAutoUpdateRepositories{list: []*Repository{{ID: 7, Enabled: true}}},
		installations,
		&fakeAutoUpdateCatalog{
			entries:  []CatalogEntry{{RepositoryID: 7, Manifest: &pluginv1.PluginManifest{PluginId: "silo.tmdb", Version: "1.1.0"}}},
			resolved: &ResolvedCatalogInstall{RepositoryID: 7, ArchiveURL: "https://plugins.example.test/tmdb", Checksum: "deadbeef"},
		},
		&fakeAutoUpdateInstaller{events: &events},
		&fakeAutoUpdateHost{events: &events},
		nil,
		nil,
	)
	summary, err := service.Check(context.Background(), AutoUpdateOptions{})
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if want := []string{"prepare"}; !slices.Equal(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if summary.UpdatesApplied != 0 || len(summary.Failures) != 0 {
		t.Fatalf("summary = %+v, want nothing applied and no failure", summary)
	}
	for _, update := range installations.updates {
		if update.UpdateFailure != nil {
			t.Fatalf("recorded failure %+v for a skipped update", update.UpdateFailure)
		}
		// A record on the newer release is about updating that release, so
		// clearing the old snapshot's record must not reach it.
		if update.ClearUpdateFailure && (update.IfInstallPath == nil || *update.IfInstallPath != "/plugins/1.0.0/plugin") {
			t.Fatalf("clear guarded on %v, want the install path the pass read", update.IfInstallPath)
		}
	}
}

// A refused automatic update is recorded on the installation so web admin
// can show it, and a later pass that no longer fails clears it.
func TestAutoUpdateServiceRecordsRefusedUpdates(t *testing.T) {
	refusal := &PackageError{
		Message: "The downloaded plugin doesn't match the SHA-256 checksum its catalog lists, so Silo didn't install it.",
		Err:     errors.New("binary checksum mismatch: expected 00, got cf"),
	}
	earlier := &UpdateFailure{Version: "1.1.0", Reason: "earlier refusal"}
	tests := []struct {
		name           string
		installed      string
		stored         *UpdateFailure
		catalogVersion string
		replaceErr     error
		noRepository   bool
		storeErr       error
		wantFailure    *UpdateFailure
		wantClear      bool
	}{
		{name: "package refusal records the admin message", installed: "1.0.0", catalogVersion: "1.1.0", replaceErr: refusal,
			wantFailure: &UpdateFailure{Version: "1.1.0", Reason: refusal.Message}},
		{name: "other failure records a generic reason", installed: "1.0.0", catalogVersion: "1.1.0", replaceErr: errors.New("write /srv/plugins: token=hunter2 denied"),
			wantFailure: &UpdateFailure{Version: "1.1.0", Reason: updateFailureGenericReason}},
		{name: "applied update clears the refusal", installed: "1.0.0", stored: earlier, catalogVersion: "1.2.0", wantClear: true},
		{name: "catalog no longer newer clears the refusal", installed: "1.1.0", stored: earlier, catalogVersion: "1.1.0", wantClear: true},
		{name: "plugin missing from the catalog keeps the refusal", installed: "1.0.0", stored: earlier},
		{name: "plugin whose repository was deleted clears the refusal", installed: "1.0.0", stored: earlier, catalogVersion: "1.1.0", noRepository: true, wantClear: true},
		{name: "plugin uninstalled during the pass is not a failure", installed: "1.1.0", stored: earlier, catalogVersion: "1.1.0", storeErr: ErrInstallationNotFound, wantClear: true},
		{name: "nothing recorded and nothing failed writes nothing", installed: "1.1.0", catalogVersion: "1.1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installations := &fakeAutoUpdateInstallations{updateErr: tt.storeErr, list: []*Installation{{
				ID: 41, RepositoryID: pluginRepositoryID(7), PluginID: "silo.tmdb", Version: tt.installed,
				UpdatePolicy: "auto", Enabled: true, UpdateFailure: tt.stored,
			}}}
			if tt.noRepository {
				installations.list[0].RepositoryID = nil
			}
			catalog := &fakeAutoUpdateCatalog{resolved: &ResolvedCatalogInstall{RepositoryID: 7, ArchiveURL: "https://plugins.example.test/tmdb", Checksum: "deadbeef"}}
			if tt.catalogVersion != "" {
				catalog.entries = []CatalogEntry{{RepositoryID: 7, Manifest: &pluginv1.PluginManifest{PluginId: "silo.tmdb", Version: tt.catalogVersion}}}
			}
			service := NewAutoUpdateService(
				&fakeAutoUpdateRepositories{list: []*Repository{{ID: 7, Enabled: true}}},
				installations, catalog, &fakeAutoUpdateInstaller{replaceErr: tt.replaceErr}, &fakeAutoUpdateHost{}, nil, nil,
			)
			summary, err := service.Check(context.Background(), AutoUpdateOptions{})
			if err != nil {
				t.Fatalf("Check() returned error: %v", err)
			}
			if tt.replaceErr == nil && len(summary.Failures) > 0 {
				t.Fatalf("summary failures = %v, want none", summary.Failures)
			}

			var recorded *UpdateFailure
			var cleared bool
			for _, update := range installations.updates {
				if update.UpdateFailure != nil {
					recorded = update.UpdateFailure
					if update.IfVersion == nil || *update.IfVersion != tt.installed {
						t.Fatalf("failure write guarded on %v, want installed version %q", update.IfVersion, tt.installed)
					}
				}
				cleared = cleared || update.ClearUpdateFailure
			}
			if cleared != tt.wantClear {
				t.Fatalf("cleared = %v, want %v (updates %+v)", cleared, tt.wantClear, installations.updates)
			}
			if tt.wantFailure == nil {
				if recorded != nil {
					t.Fatalf("recorded failure %+v, want none", recorded)
				}
				return
			}
			if recorded == nil {
				t.Fatalf("no failure recorded, want %+v", tt.wantFailure)
			}
			if recorded.Version != tt.wantFailure.Version || recorded.Reason != tt.wantFailure.Reason || recorded.FailedAt.IsZero() {
				t.Fatalf("recorded failure = %+v, want version %q reason %q and a time", recorded, tt.wantFailure.Version, tt.wantFailure.Reason)
			}
		})
	}
}

func TestAutoUpdateServiceKeepsUpdatesPinnedToInstallationRepository(t *testing.T) {
	installations := &fakeAutoUpdateInstallations{
		list: []*Installation{{
			ID:           60,
			RepositoryID: pluginRepositoryID(7),
			PluginID:     "silo.requests.arr",
			Version:      "1.0.0",
			UpdatePolicy: "auto",
			Enabled:      true,
		}},
	}
	catalog := &fakeAutoUpdateCatalog{
		entries: []CatalogEntry{
			{RepositoryID: 7, Manifest: &pluginv1.PluginManifest{PluginId: "silo.requests.arr", Version: "1.1.0"}},
			{RepositoryID: 8, Manifest: &pluginv1.PluginManifest{PluginId: "silo.requests.arr", Version: "9.0.0"}},
		},
		resolved: &ResolvedCatalogInstall{RepositoryID: 7, ArchiveURL: "https://plugins.example.test/arr", Checksum: "deadbeef"},
	}
	service := NewAutoUpdateService(
		&fakeAutoUpdateRepositories{list: []*Repository{{ID: 7, Enabled: true}, {ID: 8, Enabled: true}}},
		installations,
		catalog,
		&fakeAutoUpdateInstaller{},
		&fakeAutoUpdateHost{},
		nil,
		nil,
	)

	summary, err := service.Check(context.Background(), AutoUpdateOptions{})
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if summary.UpdatesApplied != 1 {
		t.Fatalf("UpdatesApplied = %d, want 1", summary.UpdatesApplied)
	}
	if len(catalog.resolveRequests) != 1 {
		t.Fatalf("resolve requests = %d, want 1", len(catalog.resolveRequests))
	}
	request := catalog.resolveRequests[0]
	if request.RepositoryID != 7 || request.Version != "1.1.0" {
		t.Fatalf("resolved repository/version = %d/%s, want 7/1.1.0", request.RepositoryID, request.Version)
	}
}

func TestAutoUpdateServiceDoesNotAttachUploadsToCatalogRepositories(t *testing.T) {
	service := NewAutoUpdateService(
		&fakeAutoUpdateRepositories{list: []*Repository{{ID: 7, Enabled: true}}},
		&fakeAutoUpdateInstallations{list: []*Installation{{
			ID:           61,
			PluginID:     "silo.requests.arr",
			Version:      "1.0.0",
			UpdatePolicy: "auto",
			Enabled:      true,
		}}},
		&fakeAutoUpdateCatalog{entries: []CatalogEntry{{
			RepositoryID: 7,
			Manifest:     &pluginv1.PluginManifest{PluginId: "silo.requests.arr", Version: "2.0.0"},
		}}},
		&fakeAutoUpdateInstaller{},
		&fakeAutoUpdateHost{},
		nil,
		nil,
	)

	summary, err := service.Check(context.Background(), AutoUpdateOptions{})
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if summary.UpdatesApplied != 0 || summary.UpdatesAvailable != 0 {
		t.Fatalf("upload unexpectedly received catalog update: %+v", summary)
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
	list []*Installation
	// current, when set, is what GetByID reads, as after another install.
	current    *Installation
	updates    []UpdateInstallationInput
	updateErr  error
	deletedIDs []int
}

func (f *fakeAutoUpdateInstallations) List(context.Context) ([]*Installation, error) {
	return f.list, nil
}

func (f *fakeAutoUpdateInstallations) GetByID(_ context.Context, id int) (*Installation, error) {
	if f.current != nil {
		return f.current, nil
	}
	for _, installation := range f.list {
		if installation.ID == id {
			return installation, nil
		}
	}
	return nil, ErrInstallationNotFound
}

func (f *fakeAutoUpdateInstallations) Update(_ context.Context, _ int, input UpdateInstallationInput) error {
	f.updates = append(f.updates, input)
	return f.updateErr
}

func (f *fakeAutoUpdateInstallations) Delete(_ context.Context, id int) error {
	f.deletedIDs = append(f.deletedIDs, id)
	return nil
}

type fakeAutoUpdateCatalog struct {
	entries         []CatalogEntry
	resolved        *ResolvedCatalogInstall
	resolveRequests []InstallCatalogRequest
}

func (f *fakeAutoUpdateCatalog) Fetch(context.Context) ([]CatalogEntry, error) {
	return f.entries, nil
}

func (f *fakeAutoUpdateCatalog) ResolveInstall(_ context.Context, request InstallCatalogRequest) (*ResolvedCatalogInstall, error) {
	f.resolveRequests = append(f.resolveRequests, request)
	return f.resolved, nil
}

func pluginRepositoryID(id int) *int {
	return &id
}

type fakeAutoUpdateInstaller struct {
	// replaceErr refuses the replacement package while it is prepared.
	replaceErr error
	// prepared lists the installation IDs a replacement was prepared for.
	prepared []int
	binary   []InstallBinaryRequest
	remote   []InstallArchiveRequest
	events   *[]string
}

func (f *fakeAutoUpdateInstaller) InstallRemote(_ context.Context, req InstallArchiveRequest) (*InstallResult, error) {
	f.remote = append(f.remote, req)
	return &InstallResult{}, nil
}

func (f *fakeAutoUpdateInstaller) InstallBinary(_ context.Context, req InstallBinaryRequest) (*InstallResult, error) {
	f.binary = append(f.binary, req)
	return &InstallResult{}, nil
}

func (f *fakeAutoUpdateInstaller) PrepareCatalogReplacement(_ context.Context, existing *Installation, _ *ResolvedCatalogInstall) (*PreparedReplacement, error) {
	recordTestEvent(f.events, "prepare")
	f.prepared = append(f.prepared, existing.ID)
	if f.replaceErr != nil {
		return nil, f.replaceErr
	}
	return &PreparedReplacement{apply: func(context.Context) (*InstallResult, error) {
		recordTestEvent(f.events, "apply")
		return &InstallResult{Installation: existing}, nil
	}}, nil
}

type fakeAutoUpdateHost struct {
	stopped []int
	events  *[]string
}

func (f *fakeAutoUpdateHost) Stop(installationID int) error {
	recordTestEvent(f.events, "stop")
	f.stopped = append(f.stopped, installationID)
	return nil
}
