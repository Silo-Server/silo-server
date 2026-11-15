package plugins

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/Silo-Server/silo-server/internal/plugins/pluginsig"
	"github.com/Silo-Server/silo-server/internal/plugins/pluginsig/pluginsigtest"
)

const (
	tmdbID      = "silo.tmdb"
	tmdbRepoURL = "https://github.com/Silo-Server/silo-plugin-metadata-tmdb"
	thirdRepo   = "https://github.com/someone/silo-plugin-example"
)

func testSignatureChecker(trust pluginsig.TrustedMaterialFunc, enforce bool) *SignatureChecker {
	return &SignatureChecker{verifier: pluginsig.NewVerifier(trust), enforce: enforce, now: time.Now}
}

// signedRelease is a catalog binary release for TestInstallBinarySignaturePolicy.
type signedRelease struct {
	pluginID, version string
	// sign returns the release bundle for the binary's digest; nil is a
	// catalog entry without bundle_url.
	sign    func(digest []byte) *pluginsigtest.Release
	repoURL string
	trust   pluginsig.TrustedMaterialFunc
	// bundleURLErr stands for a catalog bundle_url that doesn't resolve.
	bundleURLErr error
}

// serveSignedRelease serves rel's binary and bundle and returns the catalog
// request for them, an installer that uses rel's trust root, and the file
// the binary creates when it runs.
func serveSignedRelease(t *testing.T, sigstore *pluginsigtest.Sigstore, rel signedRelease, store installationStore, enforce bool) (InstallBinaryRequest, *Installer, string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "ran")
	manifestVersion := rel.version
	if manifestVersion == "" {
		manifestVersion = "1.2.3"
	}
	binary := manifestScript(t, packageErrorTestManifest(rel.pluginID, manifestVersion), marker)
	var bundleJSON []byte
	if rel.sign != nil {
		var err error
		if bundleJSON, err = sigstore.Bundle(*rel.sign(pluginsigtest.Digest(binary))); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/plugin", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(binary) })
	mux.HandleFunc("/bundle", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bundleJSON) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	trust := rel.trust
	if trust == nil {
		trust = sigstore.TrustedMaterial
	}
	installer := NewInstaller(store, InstallerOptions{BaseDir: t.TempDir(), HTTPClient: server.Client(), Signatures: testSignatureChecker(trust, enforce)})
	req := InstallBinaryRequest{BinaryURL: server.URL + "/plugin", Checksum: sha256Hex(binary), PluginID: rel.pluginID, Version: rel.version, SourceRepoURL: rel.repoURL, BundleURLErr: rel.bundleURLErr}
	if rel.sign != nil {
		req.BundleURL = server.URL + "/bundle"
	}
	return req, installer, marker
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// installSignedRelease serves rel and installs it through the installer.
// ran reports whether the binary's manifest command executed.
func installSignedRelease(t *testing.T, sigstore *pluginsigtest.Sigstore, rel signedRelease, enforce bool) (store *recordingInstallationStore, ran bool, err error) {
	t.Helper()
	store = newRecordingInstallationStore()
	req, installer, marker := serveSignedRelease(t, sigstore, rel, store, enforce)
	_, err = installer.InstallBinary(context.Background(), req)
	return store, fileExists(marker), err
}

// A catalog install checks the release signature after the SHA-256 and
// before the binary runs. With enforcement on, a first-party plugin ID whose
// signature is missing or fails the required policy is refused and its
// binary never runs; with it off the outcome is only recorded. Any other
// plugin ID gets the advisory policy, which never refuses.
func TestInstallBinarySignaturePolicy(t *testing.T) {
	sigstore, err := pluginsigtest.New()
	if err != nil {
		t.Fatal(err)
	}
	offline := func(context.Context) (root.TrustedMaterial, error) { return nil, errors.New("offline") }
	official := func(repo string) func([]byte) *pluginsigtest.Release {
		return func(digest []byte) *pluginsigtest.Release {
			r := pluginsigtest.OfficialRelease(repo, "1.2.3", map[string][]byte{"plugin-linux-amd64": digest})
			return &r
		}
	}
	thirdParty := func(digest []byte) *pluginsigtest.Release {
		return &pluginsigtest.Release{
			SAN:              thirdRepo + "/.github/workflows/release.yml@refs/tags/v1.2.3",
			Issuer:           pluginsig.GitHubActionsIssuer,
			SourceRepository: thirdRepo,
			SourceRef:        "refs/tags/v1.2.3",
			PredicateType:    pluginsig.SLSAProvenanceV1,
			Subjects:         map[string][]byte{"plugin-linux-amd64": digest},
		}
	}

	for _, tc := range []struct {
		name    string
		release signedRelease
		// wantState is what is recorded when the install goes ahead.
		wantState SignatureState
		// refusedWhenEnforcing marks a first-party failure.
		refusedWhenEnforcing bool
	}{
		{name: "signed first-party release", release: signedRelease{pluginID: tmdbID, version: "1.2.3", sign: official(tmdbRepoURL)}, wantState: SignatureStateVerified},
		{name: "first-party without bundle", release: signedRelease{pluginID: tmdbID, version: "1.2.3"}, wantState: SignatureStateUnsigned, refusedWhenEnforcing: true},
		{name: "first-party signed from another plugin's repo", release: signedRelease{pluginID: tmdbID, version: "1.2.3", sign: official("https://github.com/Silo-Server/silo-plugin-metadata-tvdb")}, wantState: SignatureStateInvalid, refusedWhenEnforcing: true},
		// An external catalog listing a first-party ID gets the required
		// policy: its own repo_url and validly signed release don't count.
		{name: "external catalog's own release of a first-party ID", release: signedRelease{pluginID: tmdbID, version: "1.2.3", sign: thirdParty, repoURL: thirdRepo}, wantState: SignatureStateInvalid, refusedWhenEnforcing: true},
		{name: "first-party with no catalog version", release: signedRelease{pluginID: tmdbID, sign: official(tmdbRepoURL)}, wantState: SignatureStateInvalid, refusedWhenEnforcing: true},
		{name: "first-party with the trust root unreachable", release: signedRelease{pluginID: tmdbID, version: "1.2.3", sign: official(tmdbRepoURL), trust: offline}, wantState: SignatureStateUnknown, refusedWhenEnforcing: true},
		{name: "third-party signed release", release: signedRelease{pluginID: "test.plugin", version: "1.2.3", sign: thirdParty, repoURL: thirdRepo + "/"}, wantState: SignatureStateVerified},
		{name: "third-party without bundle", release: signedRelease{pluginID: "test.plugin", version: "1.2.3"}, wantState: SignatureStateUnsigned},
		{name: "first-party with an unresolvable bundle_url", release: signedRelease{pluginID: tmdbID, version: "1.2.3", bundleURLErr: errors.New("bad url")}, wantState: SignatureStateInvalid, refusedWhenEnforcing: true},
		{name: "third-party with an unresolvable bundle_url", release: signedRelease{pluginID: "test.plugin", version: "1.2.3", bundleURLErr: errors.New("bad url")}, wantState: SignatureStateInvalid},
		{name: "third-party signed from another repo", release: signedRelease{pluginID: "test.plugin", version: "1.2.3", sign: thirdParty, repoURL: "https://github.com/someone/other"}, wantState: SignatureStateInvalid},
	} {
		for _, enforce := range []bool{false, true} {
			name := tc.name + "/not enforced"
			if enforce {
				name = tc.name + "/enforced"
			}
			t.Run(name, func(t *testing.T) {
				store, ran, err := installSignedRelease(t, sigstore, tc.release, enforce)
				if enforce && tc.refusedWhenEnforcing {
					if _, ok := AsPackageError(err); !ok {
						t.Fatalf("InstallBinary() error = %v, want a PackageError", err)
					}
					if ran || len(store.createInputs) != 0 {
						t.Fatalf("refused install ran the binary (%v) or created %d installations", ran, len(store.createInputs))
					}
					return
				}
				if err != nil {
					t.Fatalf("InstallBinary() error = %v", err)
				}
				if !ran {
					t.Fatal("the installed binary's manifest command did not run")
				}
				got := store.createInputs[0].Signature
				if got.State != tc.wantState {
					t.Fatalf("recorded signature state = %q, want %q", got.State, tc.wantState)
				}
				if verified := got.State == SignatureStateVerified; verified != (got.SourceRepo != nil && got.VerifiedAt != nil) {
					t.Fatalf("recorded signature = %+v: source repo and time must be set exactly when verified", got)
				}
			})
		}
	}
}

// Enforcement refuses a first-party catalog entry that can't carry a
// signature before anything is downloaded, whichever catalog lists it. Every
// catalog install, manual or automatic, resolves through here.
func TestInstallTargetRefusesUnsignableFirstPartyEntries(t *testing.T) {
	binaries := map[string]PlatformBinary{"linux/amd64": {URL: "plugin-linux-amd64", Checksum: sha256Hex([]byte("binary"))}}
	external := &Repository{ID: 9, URL: "https://catalog.example.test/manifest.json", SourceKind: RepositorySourceExternal}

	for _, tc := range []struct {
		name    string
		pkg     CatalogPackage
		refused bool
	}{
		{name: "first-party legacy archive", pkg: CatalogPackage{Manifest: packageErrorTestManifest(tmdbID, "1.2.3"), ArchiveURL: "plugin.zip"}, refused: true},
		{name: "first-party without bundle_url", pkg: CatalogPackage{Manifest: packageErrorTestManifest(tmdbID, "1.2.3"), Binaries: binaries}, refused: true},
		{name: "first-party with bundle_url", pkg: CatalogPackage{Manifest: packageErrorTestManifest(tmdbID, "1.2.3"), Binaries: binaries, BundleURL: "plugin.sigstore.json"}},
		{name: "third-party legacy archive", pkg: CatalogPackage{Manifest: packageErrorTestManifest("test.plugin", "1.2.3"), ArchiveURL: "plugin.zip"}},
		{name: "third-party without bundle_url", pkg: CatalogPackage{Manifest: packageErrorTestManifest("test.plugin", "1.2.3"), Binaries: binaries}},
		// An unresolvable bundle_url is left to the signature check at install.
		{name: "first-party with unresolvable bundle_url", pkg: CatalogPackage{Manifest: packageErrorTestManifest(tmdbID, "1.2.3"), Binaries: binaries, BundleURL: "%zz"}},
		{name: "third-party with unresolvable bundle_url", pkg: CatalogPackage{Manifest: packageErrorTestManifest("test.plugin", "1.2.3"), Binaries: binaries, BundleURL: "%zz"}},
	} {
		for _, enforce := range []bool{false, true} {
			catalog := NewCatalogService(nil, CatalogServiceOptions{CurrentOS: "linux", CurrentArch: "amd64", Signatures: testSignatureChecker(nil, enforce)})
			target, err := catalog.installTargetFromPackage(context.Background(), external, tc.pkg)
			if enforce && tc.refused {
				if _, ok := AsPackageError(err); !ok {
					t.Errorf("%s, enforced: error = %v, want a PackageError", tc.name, err)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s, enforced %v: error = %v", tc.name, enforce, err)
				continue
			}
			switch {
			case tc.pkg.BundleURL == "%zz":
				if target.BundleURLErr == nil || target.BinaryRequest().BundleURLErr == nil || target.BundleURL != "" {
					t.Errorf("%s: target = %+v, want the resolution error carried to the install request", tc.name, target)
				}
				if _, listed, err := catalog.catalogEntryFromPackage(external, tc.pkg); !listed || err != nil {
					t.Errorf("%s: catalog entry listed = %v, error = %v; want it listed", tc.name, listed, err)
				}
			case tc.pkg.BundleURL != "" && target.BundleURL != "https://catalog.example.test/plugin.sigstore.json":
				t.Errorf("%s: bundle URL = %q, want it resolved against the catalog", tc.name, target.BundleURL)
			}
		}
	}
}

// An automatic update of an installed first-party plugin goes through the
// installer's replace path. Under enforcement, a release that fails the
// required policy is refused before its binary runs, and the installation
// keeps its files and release; only the refused update is recorded.
func TestAutoUpdateRefusesUnprovenFirstPartyRelease(t *testing.T) {
	sigstore, err := pluginsigtest.New()
	if err != nil {
		t.Fatal(err)
	}
	store := newRecordingInstallationStore()
	req, installer, marker := serveSignedRelease(t, sigstore, signedRelease{
		pluginID: tmdbID, version: "1.2.3",
		sign: func(digest []byte) *pluginsigtest.Release {
			r := pluginsigtest.OfficialRelease("https://github.com/Silo-Server/silo-plugin-metadata-tvdb", "1.2.3", map[string][]byte{"plugin-linux-amd64": digest})
			return &r
		},
	}, store, true)

	oldPath := filepath.Join(t.TempDir(), "plugin")
	if err := os.WriteFile(oldPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installations := &fakeAutoUpdateInstallations{list: []*Installation{{
		ID: 41, RepositoryID: pluginRepositoryID(7), PluginID: tmdbID, Version: "1.2.2",
		InstallPath: oldPath, UpdatePolicy: "auto", Enabled: true,
	}}}
	catalog := &fakeAutoUpdateCatalog{
		entries: []CatalogEntry{{RepositoryID: 7, Manifest: packageErrorTestManifest(tmdbID, "1.2.3")}},
		resolved: &ResolvedCatalogInstall{
			RepositoryID: 7, PluginID: tmdbID, Version: "1.2.3",
			ArchiveURL: req.BinaryURL, Checksum: req.Checksum, BundleURL: req.BundleURL,
		},
	}
	service := NewAutoUpdateService(
		&fakeAutoUpdateRepositories{list: []*Repository{{ID: 7, URL: DefaultRepositoryURL, Enabled: true}}},
		installations, catalog, installer, &fakeAutoUpdateHost{}, nil, nil,
	)
	summary, err := service.Check(context.Background(), AutoUpdateOptions{})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if summary.UpdatesApplied != 0 || summary.FailedOperations == 0 {
		t.Fatalf("summary = %+v, want the update refused", summary)
	}
	if fileExists(marker) {
		t.Fatal("the refused release's binary ran")
	}
	if len(store.createInputs)+len(store.updateInputs)+len(store.saveArchiveIDs) != 0 {
		t.Fatalf("installer wrote the installation: creates %d, updates %d, archives %d", len(store.createInputs), len(store.updateInputs), len(store.saveArchiveIDs))
	}
	if !fileExists(oldPath) {
		t.Fatal("the installed release's files were removed")
	}
	for _, update := range installations.updates {
		if update.Version != nil || update.InstallPath != nil || update.Signature != nil || update.UpdateFailure == nil {
			t.Fatalf("installation update = %+v, want only the refused update recorded", update)
		}
	}
	if len(installations.updates) != 1 {
		t.Fatalf("installation updates = %d, want the refused update recorded once", len(installations.updates))
	}
}
