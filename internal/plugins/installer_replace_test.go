package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestInstallerInstallBinaryPersistsPackagedArchive(t *testing.T) {
	ctx := context.Background()

	store := newRecordingInstallationStore()
	installer := NewInstaller(store, InstallerOptions{BaseDir: t.TempDir()})

	manifest := testPluginManifest(t, "silo.metadb", "0.0.19")
	binaryData := []byte("#!/bin/sh\nexit 0\n")
	checksum := sha256.Sum256(binaryData)
	manifest.Checksum = hex.EncodeToString(checksum[:])

	result, err := installer.installBinary(ctx, binaryData, manifest.GetChecksum(), manifest, nil, nil, unsignedSignature, false)
	if err != nil {
		t.Fatalf("installBinary() returned error: %v", err)
	}

	if result.Installation.ID != 1 {
		t.Fatalf("result installation id = %d, want 1", result.Installation.ID)
	}
	if len(store.createInputs) != 1 {
		t.Fatalf("Create() calls = %d, want 1", len(store.createInputs))
	}
	created := store.createInputs[0]
	assertStoredPluginArchive(t, created.Archive.Bytes, created.Archive.ManifestJSON, binaryData)
}

func TestInstallerReplaceBinaryPreservesInstallationID(t *testing.T) {
	ctx := context.Background()

	root := t.TempDir()
	oldDir := filepath.Join(root, "silo.metadb", "0.0.18", "install-old")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldDir, "plugin")
	if err := os.WriteFile(oldPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("WriteFile(%q) returned error: %v", oldPath, err)
	}

	store := newRecordingInstallationStore()
	installer := NewInstaller(store, InstallerOptions{BaseDir: root})

	manifest := testPluginManifest(t, "silo.metadb", "0.0.19")
	binaryData := []byte("#!/bin/sh\nexit 0\n")
	checksum := sha256.Sum256(binaryData)
	manifest.Checksum = hex.EncodeToString(checksum[:])

	oldRepository := 7
	oldRepo, verifiedAt := "https://github.com/Silo-Server/silo-plugin-metadb", time.Now()
	prepared, err := installer.prepareBinary(&Installation{
		ID:            15,
		RepositoryID:  &oldRepository,
		PluginID:      "silo.metadb",
		Version:       "0.0.18",
		InstallPath:   oldPath,
		Enabled:       true,
		UpdateFailure: &UpdateFailure{Version: "0.0.19", Reason: "refused earlier"},
		Signature:     Signature{State: SignatureStateVerified, SourceRepo: &oldRepo, VerifiedAt: &verifiedAt},
	}, binaryData, hex.EncodeToString(checksum[:]), manifest, nil, nil, unsignedSignature, false)
	if err != nil {
		t.Fatalf("prepareBinary() returned error: %v", err)
	}
	result, err := prepared.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	if len(store.createInputs) != 0 {
		t.Fatalf("Create() called %d times, want 0", len(store.createInputs))
	}
	if len(store.updateIDs) != 1 || store.updateIDs[0] != 15 {
		t.Fatalf("Update() ids = %#v, want [15]", store.updateIDs)
	}
	archive := store.updateInputs[0].Archive
	if archive == nil {
		t.Fatal("Update() stored no archive with the replacement")
	}
	assertStoredPluginArchive(t, archive.Bytes, archive.ManifestJSON, binaryData)
	if result.Installation.ID != 15 {
		t.Fatalf("result installation id = %d, want 15", result.Installation.ID)
	}
	if result.Installation.Version != "0.0.19" {
		t.Fatalf("result installation version = %q, want 0.0.19", result.Installation.Version)
	}
	// The version change cleared the stored refusal; the result must not
	// report it as still pending.
	if result.Installation.UpdateFailure != nil {
		t.Fatalf("result update failure = %+v, want nil after a replacement", result.Installation.UpdateFailure)
	}
	if result.Installation.InstallPath == oldPath {
		t.Fatal("expected the replacement to move the installation to a new path")
	}
	if result.Installation.InstallPath != result.BinaryPath {
		t.Fatalf("result install path = %q, want binary path %q", result.Installation.InstallPath, result.BinaryPath)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("expected old installation dir to be removed, stat error = %v", err)
	}
	if _, err := os.Stat(result.BinaryPath); err != nil {
		t.Fatalf("expected replaced plugin binary to exist: %v", err)
	}
	if _, err := os.Stat(result.ManifestPath); err != nil {
		t.Fatalf("expected replaced manifest to exist: %v", err)
	}

	update := store.updateInputs[0]
	// An uploaded replacement no longer comes from the catalog repository the
	// row named, so the row must stop claiming it.
	if !update.SetRepository || update.RepositoryID != nil || result.Installation.RepositoryID != nil {
		t.Fatalf("update repository = %t/%v, result repository = %v; want cleared", update.SetRepository, update.RepositoryID, result.Installation.RepositoryID)
	}
	// Nor may it keep the verified signature of the package it replaced.
	if update.Signature == nil || update.Signature.State != SignatureStateUnsigned || result.Installation.Signature.State != SignatureStateUnsigned {
		t.Fatalf("update signature = %+v, result signature = %+v; want unsigned", update.Signature, result.Installation.Signature)
	}
	if update.Version == nil || *update.Version != "0.0.19" {
		t.Fatalf("update version = %v, want 0.0.19", update.Version)
	}
	if update.InstallPath == nil || *update.InstallPath != result.BinaryPath {
		t.Fatalf("update install_path = %v, want %q", update.InstallPath, result.BinaryPath)
	}
	// The enabled flag is the admin's, and may have changed while the
	// package was prepared, so the replacement leaves it alone.
	if update.Enabled != nil {
		t.Fatalf("update enabled = %v, want unchanged", *update.Enabled)
	}
	if len(update.Capabilities) != 1 || update.Capabilities[0].ID != "metadb" {
		t.Fatalf("update capabilities = %#v, want metadata_provider.v1/metadb", update.Capabilities)
	}
}

func assertStoredPluginArchive(t *testing.T, archiveBytes, manifestBytes, binaryData []byte) {
	t.Helper()

	reader, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatalf("stored archive is not a zip: %v", err)
	}

	files := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		files[file.Name] = file
	}

	manifestFile, ok := files["manifest.json"]
	if !ok {
		t.Fatal("stored archive is missing manifest.json")
	}
	gotManifest, err := readZipFile(manifestFile)
	if err != nil {
		t.Fatalf("read manifest.json from stored archive: %v", err)
	}
	if !bytes.Equal(gotManifest, manifestBytes) {
		t.Fatal("stored archive manifest does not match saved manifest bytes")
	}

	binaryFile, ok := files["plugin"]
	if !ok {
		t.Fatal("stored archive is missing plugin")
	}
	gotBinary, err := readZipFile(binaryFile)
	if err != nil {
		t.Fatalf("read plugin from stored archive: %v", err)
	}
	if !bytes.Equal(gotBinary, binaryData) {
		t.Fatal("stored archive binary does not match installed binary")
	}
}

func testPluginManifest(t *testing.T, pluginID, version string) *pluginv1.PluginManifest {
	t.Helper()

	manifest := &pluginv1.PluginManifest{
		PluginId:       pluginID,
		Version:        version,
		Checksum:       "sha256-placeholder",
		SiloApiVersion: DefaultSiloAPIVersion,
		SupportedPlatforms: []*pluginv1.SupportedPlatform{
			{Os: "darwin", Arch: "arm64"},
		},
		Capabilities: []*pluginv1.CapabilityDescriptor{
			{
				Type:        "metadata_provider.v1",
				Id:          "metadb",
				DisplayName: "MetaDB",
			},
		},
	}

	if _, err := protojson.Marshal(manifest); err != nil {
		t.Fatalf("protojson.Marshal() returned error: %v", err)
	}

	return manifest
}

type recordingInstallationStore struct {
	createInputs        []CreateInstallationInput
	updateIDs           []int
	updateInputs        []UpdateInstallationInput
	deleteIDs           []int
	saveArchiveIDs      []int
	saveArchiveErr      error
	saveArchiveManifest []byte
	saveArchiveChecksum string
	saveArchiveBytes    []byte
	updateErr           error
}

func newRecordingInstallationStore() *recordingInstallationStore {
	return &recordingInstallationStore{}
}

func (s *recordingInstallationStore) Create(_ context.Context, input CreateInstallationInput) (*Installation, error) {
	s.createInputs = append(s.createInputs, input)
	return &Installation{
		ID:          1,
		PluginID:    input.PluginID,
		Version:     input.Version,
		InstallPath: input.InstallPath,
		Enabled:     input.Enabled,
	}, nil
}

func (s *recordingInstallationStore) SaveArchive(
	_ context.Context,
	installationID int,
	manifestJSON []byte,
	checksum string,
	archiveBytes []byte,
) error {
	s.saveArchiveIDs = append(s.saveArchiveIDs, installationID)
	s.saveArchiveManifest = append([]byte(nil), manifestJSON...)
	s.saveArchiveChecksum = checksum
	s.saveArchiveBytes = append([]byte(nil), archiveBytes...)
	return s.saveArchiveErr
}

func (s *recordingInstallationStore) Update(_ context.Context, id int, input UpdateInstallationInput) error {
	s.updateIDs = append(s.updateIDs, id)
	s.updateInputs = append(s.updateInputs, input)
	return s.updateErr
}

func (s *recordingInstallationStore) Delete(_ context.Context, id int) error {
	s.deleteIDs = append(s.deleteIDs, id)
	return nil
}

// A replacement applies only to the installation it was prepared for. An
// automatic update still downloading when an admin installs a newer release
// is refused once it finishes, and the newer release, its stored archive and
// its files stay.
func TestInstallerReplacementRefusedAfterInstallationChangedDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	store := NewInstallationStore(pool)
	installer := NewInstaller(store, InstallerOptions{BaseDir: t.TempDir()})
	pluginID := "silo.test.replace-race" + time.Now().UTC().Format("-20060102150405.000000000")

	release := func(version string) ([]byte, string, *pluginv1.PluginManifest) {
		binary := []byte("#!/bin/sh\n# " + version + "\nexit 0\n")
		checksum := sha256Hex(binary)
		manifest := packageErrorTestManifest(pluginID, version)
		manifest.Checksum = checksum
		return binary, checksum, manifest
	}
	binary, checksum, manifest := release("1.0.0")
	installed, err := installer.installBinary(ctx, binary, checksum, manifest, nil, nil, unsignedSignature, false)
	if err != nil {
		t.Fatalf("install 1.0.0: %v", err)
	}
	id := installed.Installation.ID
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id)
	})
	snapshot, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	binary, checksum, manifest = release("1.1.0")
	stale, err := installer.prepareBinary(snapshot, binary, checksum, manifest, nil, nil, unsignedSignature, false)
	if err != nil {
		t.Fatalf("prepare 1.1.0: %v", err)
	}
	binary, checksum, manifest = release("2.0.0")
	newer, err := installer.prepareBinary(snapshot, binary, checksum, manifest, nil, nil, unsignedSignature, false)
	if err != nil {
		t.Fatalf("prepare 2.0.0: %v", err)
	}
	applied, err := newer.Apply(ctx)
	if err != nil {
		t.Fatalf("apply 2.0.0: %v", err)
	}

	if _, err := stale.Apply(ctx); !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("apply 1.1.0 after 2.0.0 = %v, want ErrInstallationChanged", err)
	}
	current, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != "2.0.0" || current.InstallPath != applied.BinaryPath {
		t.Fatalf("installation = %s at %s, want 2.0.0 at %s", current.Version, current.InstallPath, applied.BinaryPath)
	}
	archive, err := store.GetArchive(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Checksum != checksum {
		t.Fatalf("stored archive checksum = %s, want the 2.0.0 package's %s", archive.Checksum, checksum)
	}
	// The guard holds for a write that changes no column, too.
	staleArchive := &ArchiveInput{ManifestJSON: archive.ManifestJSON, Checksum: "stale", Bytes: archive.Bytes}
	if err := store.Update(ctx, id, UpdateInstallationInput{Archive: staleArchive, IfInstallPath: &snapshot.InstallPath}); !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("archive-only write guarded on the old path = %v, want ErrInstallationChanged", err)
	}
	if archive, err := store.GetArchive(ctx, id); err != nil || archive.Checksum != checksum {
		t.Fatalf("stored archive after a refused archive-only write = %+v, %v; want the 2.0.0 package", archive, err)
	}
	if _, err := os.Stat(applied.BinaryPath); err != nil {
		t.Fatalf("2.0.0 binary: %v", err)
	}
	staleRoot, err := pluginVersionRoot(installer.baseDir, pluginID, "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(staleRoot); len(entries) != 0 {
		t.Fatalf("refused 1.1.0 left %d install dirs behind", len(entries))
	}
}

// A server fetching another platform's build refuses one signed with less
// than the installed build before running it to read its manifest.
func TestDownloadBinaryCheckedRefusesBeforeRunningTheBinary(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	binary := []byte("#!/bin/sh\ntouch '" + ran + "'\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(binary) }))
	t.Cleanup(server.Close)
	installer := NewInstaller(nil, InstallerOptions{BaseDir: t.TempDir(), HTTPClient: server.Client()})
	refused := errors.New("refused")
	_, err := installer.downloadBinaryChecked(context.Background(), InstallBinaryRequest{
		BinaryURL: server.URL + "/plugin", Checksum: sha256Hex(binary), PluginID: "example.thirdparty", Version: "1.0.0",
	}, func([]byte, Signature) error { return refused })
	if !errors.Is(err, refused) {
		t.Fatalf("downloadBinaryChecked() = %v, want the accept hook's refusal", err)
	}
	if _, err := os.Stat(ran); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused binary ran: %v", err)
	}
}
