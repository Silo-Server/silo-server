package plugins

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/plugins/pluginsig/pluginsigtest"
)

const (
	provisionTestPluginID = "silo.auth.oidc"
	provisionTestOld      = "0.1.1"
	provisionTestCurrent  = "0.1.2"
)

type provisionCatalogAssets map[string][]byte

func (a provisionCatalogAssets) RoundTrip(req *http.Request) (*http.Response, error) {
	data, ok := a[req.URL.String()]
	status := http.StatusOK
	if !ok {
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
}

func fixtureOfficialPackage(version, digest string) CatalogPackage {
	repository, _ := officialPluginRepository(provisionTestPluginID)
	tag := "v" + version
	prefix := repository + "/releases/download/" + tag + "/"
	return CatalogPackage{
		Manifest:  packageErrorTestManifest(provisionTestPluginID, version),
		RepoURL:   repository,
		BundleURL: prefix + "plugin.sigstore.json",
		Tag:       tag,
		Binaries:  map[string]PlatformBinary{"linux/amd64": {URL: prefix + "plugin-linux-amd64", Checksum: digest}},
	}
}

func officialDescriptorURL(version string) string {
	root, _, _ := strings.CutLast(DefaultRepositoryURL, "/")
	return root + "/releases/" + provisionTestPluginID + "/" + version + ".json"
}

func putProvisionAsset(t *testing.T, assets provisionCatalogAssets, url string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	assets[url] = data
}

func TestResolveHistoricalOfficialPlugin(t *testing.T) {
	old := fixtureOfficialPackage(provisionTestOld, strings.Repeat("a", 64))
	current := fixtureOfficialPackage(provisionTestCurrent, strings.Repeat("b", 64))
	assets := provisionCatalogAssets{}
	repository := &Repository{ID: 1, URL: DefaultRepositoryURL, Enabled: true, ManagedKey: new(OfficialRepositoryManagedKey), SourceKind: RepositorySourceSilo}
	svc := NewCatalogService(nil, CatalogServiceOptions{HTTPClient: &http.Client{Transport: assets}, CurrentOS: "linux", CurrentArch: "amd64"})
	index := RepositoryIndex{Plugins: []CatalogPackage{current}}
	putProvisionAsset(t, assets, DefaultRepositoryURL, index)
	putProvisionAsset(t, assets, officialDescriptorURL(provisionTestOld), old)
	for _, tc := range []struct {
		pin string
		pkg CatalogPackage
	}{
		{pin: provisionTestOld, pkg: old},
		{pkg: current},
	} {
		target, err := svc.resolveOfficialInstall(t.Context(), repository, provisionTestPluginID, tc.pin)
		if err != nil {
			t.Fatal(err)
		}
		if target.Version != tc.pkg.Manifest.GetVersion() || target.Checksum != tc.pkg.Binaries["linux/amd64"].Checksum || target.RepoURL != tc.pkg.RepoURL {
			t.Fatalf("resolved package %+v does not match %s", target, tc.pkg.Manifest.GetVersion())
		}
	}

	if _, err := svc.resolveOfficialInstall(t.Context(), repository, provisionTestPluginID, "../../manifest"); err == nil {
		t.Fatal("accepted a path in the version pin")
	}
	putProvisionAsset(t, assets, officialDescriptorURL(provisionTestOld), current)
	_, err := svc.resolveOfficialInstall(t.Context(), repository, provisionTestPluginID, provisionTestOld)
	requirePackageError(t, err, "release metadata is invalid")

	old.Manifest.Capabilities = []*pluginv1.CapabilityDescriptor{nil}
	putProvisionAsset(t, assets, officialDescriptorURL(provisionTestOld), old)
	_, err = svc.resolveOfficialInstall(t.Context(), repository, provisionTestPluginID, provisionTestOld)
	requirePackageError(t, err, "capability at index 0 is null")

	index.Withdrawals = []CatalogWithdrawal{{PluginID: provisionTestPluginID, Version: provisionTestOld}}
	putProvisionAsset(t, assets, DefaultRepositoryURL, index)
	delete(assets, officialDescriptorURL(provisionTestOld))
	_, err = svc.resolveOfficialInstall(t.Context(), repository, provisionTestPluginID, provisionTestOld)
	requirePackageError(t, err, "withdrawn")
}

func TestProvisionStagingSweepPreservesLivePackage(t *testing.T) {
	installer := NewInstaller(nil, InstallerOptions{BaseDir: t.TempDir()})
	active, err := newProvisionPackageStage(installer.baseDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = active.owner.Close() })
	abandoned, err := newProvisionPackageStage(installer.baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := abandoned.owner.Close(); err != nil {
		t.Fatal(err)
	}
	publishing := filepath.Join(filepath.Dir(active.dir), provisionStagePrefix+"publishing")
	if err := os.Mkdir(publishing, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(publishing, old, old); err != nil {
		t.Fatal(err)
	}
	if err := installer.CleanupProvisionStaging(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(active.dir); err != nil {
		t.Fatalf("active stage removed: %v", err)
	}
	if _, err := os.Stat(abandoned.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned stage still exists: %v", err)
	}
	if _, err := os.Stat(publishing); err != nil {
		t.Fatalf("stage without a published owner removed: %v", err)
	}
}

func TestAutoUpdateClearsWithdrawnNoticeDB(t *testing.T) {
	ctx := t.Context()
	pool := builtinGuardTestPool(t)
	repositories := NewRepositoryStore(pool)
	if _, err := repositories.ReconcileManaged(ctx); err != nil {
		t.Fatal(err)
	}
	assets := provisionCatalogAssets{}
	catalog := NewCatalogService(repositories, CatalogServiceOptions{
		HTTPClient: &http.Client{Transport: assets}, CurrentOS: "linux", CurrentArch: "amd64",
	})
	repository, err := catalog.OfficialRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := seedResidentQueryInstallation(t, pool, true)
	store := NewInstallationStore(pool)
	current, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	notice := provisionTestCurrent
	if err := store.Update(ctx, id, UpdateInstallationInput{
		SetRepository: true, RepositoryID: &repository.ID, UpdatePolicy: new("notify"), AvailableVersion: &notice,
	}); err != nil {
		t.Fatal(err)
	}
	var notifications int
	updater := NewAutoUpdateService(repositories, store, catalog, nil, nil, func(context.Context) { notifications++ })
	checkNotice := func(wantVersion string, wantNotifications int) {
		t.Helper()
		summary, err := updater.Check(ctx, AutoUpdateOptions{})
		if err != nil || summary.FailedOperations != 0 {
			t.Fatalf("checking for updates returned %+v, %v", summary, err)
		}
		installation, err := store.GetByID(ctx, id)
		if err != nil || installation.AvailableVersion == nil || *installation.AvailableVersion != wantVersion {
			t.Fatalf("want saved update notice %q, got %+v, %v", wantVersion, installation, err)
		}
		if notifications != wantNotifications {
			t.Fatalf("want %d lifecycle notifications, got %d", wantNotifications, notifications)
		}
	}

	checkNotice(notice, 0) // Catalog unavailable.
	pkg := fixtureOfficialPackage(notice, strings.Repeat("a", 64))
	pkg.Manifest.PluginId = current.PluginID
	putProvisionAsset(t, assets, DefaultRepositoryURL, RepositoryIndex{
		Plugins:     []CatalogPackage{pkg},
		Withdrawals: []CatalogWithdrawal{{PluginID: current.PluginID, Version: notice}},
	})
	checkNotice("", 1) // Withdrawals also apply to versions still listed.

	newNotice := "1.0.0"
	if err := store.Update(ctx, id, UpdateInstallationInput{AvailableVersion: &newNotice}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, id, UpdateInstallationInput{
		AvailableVersion: new(""), ifAvailableVersion: &notice, IfVersion: &current.Version, IfInstallPath: &current.InstallPath,
	}); !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("clearing the old notice after a concurrent check returned %v", err)
	}
	checkNotice(newNotice, 1)

	// A notice no newer than the installed release, as a stale check could
	// record before #4518, is cleared: applying it is refused (409), so
	// keeping it would leave an Update button that can never succeed.
	if _, err := pool.Exec(ctx, `UPDATE plugin_installations SET version = '2.0.0' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	checkNotice("", 2)
}

func newProvisionCommitFixture(t *testing.T, pool *pgxpool.Pool) (*CatalogService, *Installer) {
	t.Helper()
	ctx := t.Context()
	repositories := NewRepositoryStore(pool)
	if _, err := repositories.ReconcileManaged(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewInstallationStore(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM plugin_installations WHERE plugin_id = $1`, provisionTestPluginID)
	})
	sigstore, err := pluginsigtest.New()
	if err != nil {
		t.Fatal(err)
	}

	assets := provisionCatalogAssets{}
	client := &http.Client{Transport: assets}
	checker := testSignatureChecker(sigstore.TrustedMaterial, false)
	catalog := NewCatalogService(repositories, CatalogServiceOptions{HTTPClient: client, CurrentOS: "linux", CurrentArch: "amd64"})
	installer := NewInstaller(store, InstallerOptions{BaseDir: t.TempDir(), HTTPClient: client, Signatures: checker})
	var current CatalogPackage
	for _, version := range []string{provisionTestOld, provisionTestCurrent} {
		binary := manifestScript(t, packageErrorTestManifest(provisionTestPluginID, version), "")
		pkg := fixtureOfficialPackage(version, sha256Hex(binary))
		bundle, err := sigstore.Bundle(pluginsigtest.OfficialRelease(pkg.RepoURL, version,
			map[string][]byte{"plugin-linux-amd64": pluginsigtest.Digest(binary)}))
		if err != nil {
			t.Fatal(err)
		}
		assets[pkg.Binaries["linux/amd64"].URL] = binary
		assets[pkg.BundleURL] = bundle
		putProvisionAsset(t, assets, officialDescriptorURL(version), pkg)
		current = pkg
	}
	putProvisionAsset(t, assets, DefaultRepositoryURL, RepositoryIndex{Plugins: []CatalogPackage{current}})
	return catalog, installer
}

func TestProvisionPackageCommitAndRollbackDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	catalog, installer := newProvisionCommitFixture(t, pool)
	ctx := t.Context()
	store := installer.store
	prepare := func(existing *Installation, version string) *PreparedProvisionPackage {
		t.Helper()
		pkg, err := catalog.PrepareProvisionPackage(ctx, installer, ProvisionPackageRequest{
			PluginID: provisionTestPluginID, Version: version, Existing: existing,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := pkg.Abort(); err != nil {
				t.Error(err)
			}
		})
		return pkg
	}
	fresh := prepare(nil, provisionTestOld)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := fresh.Persist(ctx, store, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListByPluginID(ctx, provisionTestPluginID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("fresh install rows = %v, error = %v", rows, err)
	}
	existing := rows[0]
	if existing.Version != provisionTestOld || !fileExists(existing.InstallPath) {
		t.Fatalf("older release did not become available: %+v", existing)
	}
	checkProvisionOfflineReuse(t, catalog.repositories, installer, existing)

	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return PutProvisionOwnership(ctx, tx, existing.ID, ProvisionOwnership{Managed: true, VersionPin: provisionTestOld})
	}); err != nil {
		t.Fatal(err)
	}
	firstArchive, err := store.GetArchive(ctx, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement := prepare(existing, provisionTestCurrent)
	if !replacement.ReplacesPackage() || !fileExists(existing.InstallPath) || fileExists(replacement.create.InstallPath) {
		t.Fatal("visible files changed before commit")
	}
	rollback := errors.New("rollback package test")
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := replacement.Persist(ctx, store, tx); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("replacement rollback returned %v", err)
	}
	if err := replacement.Abort(); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetArchive(ctx, existing.ID)
	if err != nil || stored.Checksum != firstArchive.Checksum || !fileExists(existing.InstallPath) || fileExists(replacement.stage.dir) {
		t.Fatalf("rollback changed the installed release or retained stage files: %v", err)
	}

	replacement = prepare(existing, provisionTestCurrent)
	var replaced *Installation
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		replaced, err = replacement.Persist(ctx, store, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := replacement.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if replaced.ID != existing.ID || replaced.Version != provisionTestCurrent || !fileExists(replaced.InstallPath) || fileExists(existing.InstallPath) {
		t.Fatalf("committed replacement did not retire the old files: %+v", replaced)
	}
	cache := NewArchiveCacheAt(store, filepath.Join(t.TempDir(), "cold-cache"))
	manifest, err := cache.Ensure(ctx, replaced)
	if err != nil || manifest.GetVersion() != provisionTestCurrent {
		t.Fatalf("cold cache rehydration = %v, %v", manifest, err)
	}

	checkStaleProvisionPackage(t, catalog, installer, existing, replaced)
}

func checkStaleProvisionPackage(t *testing.T, catalog *CatalogService, installer *Installer, existing, replaced *Installation) {
	t.Helper()
	ctx := t.Context()
	// A package prepared from a snapshot taken before the replacement must be retried.
	request := ProvisionPackageRequest{PluginID: provisionTestPluginID, Existing: existing, Offline: true}
	if _, err := catalog.PrepareProvisionPackage(ctx, installer, request); !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("offline reuse from a stale installation returned %v, want ErrInstallationChanged", err)
	}
	if _, err := installer.store.pool.Exec(ctx, `DELETE FROM plugin_archives WHERE plugin_installation_id = $1`, replaced.ID); err != nil {
		t.Fatal(err)
	}
	request.Existing = replaced
	if _, err := catalog.PrepareProvisionPackage(ctx, installer, request); !errors.Is(err, ErrProvisionPackageUntrusted) {
		t.Fatalf("offline reuse with the current archive missing returned %v, want ErrProvisionPackageUntrusted", err)
	}
}

func checkProvisionOfflineReuse(t *testing.T, repositories *RepositoryStore, installer *Installer, existing *Installation) {
	t.Helper()
	catalog := NewCatalogService(repositories, CatalogServiceOptions{HTTPClient: &http.Client{Transport: provisionCatalogAssets{}}})
	for _, pin := range []string{"", provisionTestOld} {
		reuse, err := catalog.PrepareProvisionPackage(t.Context(), installer, ProvisionPackageRequest{
			PluginID: provisionTestPluginID, Version: pin, Existing: existing, Offline: true,
		})
		if err != nil || reuse.stage != nil || reuse.Version != provisionTestOld {
			t.Fatalf("offline reuse at pin %q = %+v, %v", pin, reuse, err)
		}
	}
	// A server API change prevents reuse, but must allow a different pin to be fetched.
	catalog.siloAPIVersion = "test-next"
	request := ProvisionPackageRequest{PluginID: provisionTestPluginID, Existing: existing, Offline: true}
	if _, err := catalog.PrepareProvisionPackage(t.Context(), installer, request); err == nil || errors.Is(err, ErrProvisionPackageNeedsFetch) {
		t.Fatalf("incompatible archive reuse returned %v", err)
	}
	request.Version = provisionTestCurrent
	if _, err := catalog.PrepareProvisionPackage(t.Context(), installer, request); !errors.Is(err, ErrProvisionPackageNeedsFetch) {
		t.Fatalf("replacement of an incompatible archive returned %v, want fetch", err)
	}
	untrusted := *existing
	untrusted.Signature.State = SignatureStateUnknown
	if _, err := catalog.PrepareProvisionPackage(t.Context(), installer, ProvisionPackageRequest{
		PluginID: provisionTestPluginID, Existing: &untrusted, Offline: true,
	}); !errors.Is(err, ErrProvisionPackageUntrusted) {
		t.Fatalf("unverified package = %v", err)
	}
}

// A server on another platform fetches the build of the official release an
// installation holds even after the official catalog's list moved on to a
// newer one, from that release's descriptor, with its signature verified.
func TestPlatformBuildOfAnEarlierOfficialReleaseDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	catalog, installer := newProvisionCommitFixture(t, pool)
	official, err := catalog.OfficialRepository(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	repoURL, _ := officialPluginRepository(provisionTestPluginID)
	installation := &Installation{
		ID: 1, RepositoryID: &official.ID, PluginID: provisionTestPluginID, Version: provisionTestOld,
		Signature: Signature{State: SignatureStateVerified, SourceRepo: &repoURL},
	}
	svc := &Service{catalog: catalog, installer: installer}
	build, err := svc.fetchPlatformBuild(t.Context(), installation)
	if err != nil {
		t.Fatalf("fetchPlatformBuild(%s) = %v", provisionTestOld, err)
	}
	if build.manifest.GetVersion() != provisionTestOld || build.signature.State != SignatureStateVerified {
		t.Fatalf("fetched %s signed %q; want %s, verified", build.manifest.GetVersion(), build.signature.State, provisionTestOld)
	}
}
