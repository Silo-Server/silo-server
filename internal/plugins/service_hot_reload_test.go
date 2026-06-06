package plugins

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

func TestServiceEnsureClientRestartsOnManifestDrift(t *testing.T) {
	ctx := context.Background()
	manifest := testPluginManifest(t, "silo.metadb", "0.0.36")
	installPath := writeInstalledPluginManifest(t, manifest)

	store := newFakeServiceInstallationStore(&Installation{
		ID:          3,
		PluginID:    manifest.GetPluginId(),
		Version:     manifest.GetVersion(),
		InstallPath: installPath,
		Enabled:     true,
	})
	host := &fakeServiceHost{
		clientResult: &fakePluginClient{manifest: testPluginManifest(t, "silo.metadb", "0.0.34")},
		startResult:  &fakePluginClient{manifest: manifest},
	}
	service := &Service{
		installations: store,
		host:          host,
	}

	got, err := service.MetadataProviderClient(ctx, 3, "metadb")
	if err != nil {
		t.Fatalf("MetadataProviderClient() returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("MetadataProviderClient() = %#v, want nil fake client", got)
	}
	if len(host.stopped) != 1 || host.stopped[0] != 3 {
		t.Fatalf("stopped installations = %#v, want [3]", host.stopped)
	}
	if len(host.started) != 1 {
		t.Fatalf("start calls = %d, want 1", len(host.started))
	}
	startReq := host.started[0]
	if startReq.InstallationID != 3 {
		t.Fatalf("start installation id = %d, want 3", startReq.InstallationID)
	}
	if startReq.BinaryPath != installPath {
		t.Fatalf("start binary path = %q, want %q", startReq.BinaryPath, installPath)
	}
	if startReq.Manifest.GetVersion() != "0.0.36" {
		t.Fatalf("start manifest version = %q, want 0.0.36", startReq.Manifest.GetVersion())
	}
}

func TestServiceEnsureClientKeepsHealthyClientWhenInstalledManifestUnavailable(t *testing.T) {
	ctx := context.Background()

	installDir := t.TempDir()
	installPath := filepath.Join(installDir, "plugin")
	if err := os.WriteFile(installPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("WriteFile(%q) returned error: %v", installPath, err)
	}

	runningClient := &fakePluginClient{manifest: testPluginManifest(t, "silo.metadb", "0.0.36")}
	store := newFakeServiceInstallationStore(&Installation{
		ID:          3,
		PluginID:    "silo.metadb",
		Version:     "0.0.36",
		InstallPath: installPath,
		Enabled:     true,
	})
	host := &fakeServiceHost{clientResult: runningClient}
	service := &Service{
		installations: store,
		host:          host,
	}

	got, err := service.ensureClient(ctx, 3)
	if err != nil {
		t.Fatalf("ensureClient() returned error: %v", err)
	}
	if got != runningClient {
		t.Fatalf("ensureClient() returned %#v, want existing running client %#v", got, runningClient)
	}
	if len(host.started) != 0 {
		t.Fatalf("start calls = %d, want 0", len(host.started))
	}
	if len(host.stopped) != 0 {
		t.Fatalf("stop calls = %d, want 0", len(host.stopped))
	}
}

func TestServiceEnsureClientRestartsWhenInstalledManifestDiffers(t *testing.T) {
	ctx := context.Background()
	installedManifest := testPluginManifest(t, "silo.metadb", "0.0.36")
	installPath := writeInstalledPluginManifest(t, installedManifest)

	runningClient := &fakePluginClient{manifest: testPluginManifest(t, "silo.metadb", "0.0.34")}
	restartedClient := &fakePluginClient{manifest: installedManifest}
	store := newFakeServiceInstallationStore(&Installation{
		ID:          3,
		PluginID:    installedManifest.GetPluginId(),
		Version:     installedManifest.GetVersion(),
		InstallPath: installPath,
		Enabled:     true,
	})
	host := &fakeServiceHost{
		clientResult: runningClient,
		startResult:  restartedClient,
	}
	service := &Service{
		installations: store,
		host:          host,
	}

	got, err := service.ensureClient(ctx, 3)
	if err != nil {
		t.Fatalf("ensureClient() returned error: %v", err)
	}
	if got != restartedClient {
		t.Fatalf("ensureClient() returned %#v, want restarted client %#v", got, restartedClient)
	}
	if len(host.stopped) != 1 || host.stopped[0] != 3 {
		t.Fatalf("stopped installations = %#v, want [3]", host.stopped)
	}
	if len(host.started) != 1 {
		t.Fatalf("start calls = %d, want 1", len(host.started))
	}
	if host.started[0].Manifest.GetVersion() != "0.0.36" {
		t.Fatalf("started manifest version = %q, want 0.0.36", host.started[0].Manifest.GetVersion())
	}
}

func TestNewHostAdapterReturnsHost(t *testing.T) {
	adapted := NewHostAdapter(pluginhost.NewHost(pluginhost.Config{}))
	if adapted == nil {
		t.Fatal("NewHostAdapter() = nil, want host adapter")
	}
}

type fakeServiceHost struct {
	clientResult pluginClient
	clientErr    error
	startResult  pluginClient
	startErr     error
	started      []pluginhost.StartRequest
	stopped      []int
	events       *[]string
}

func (f *fakeServiceHost) Start(_ context.Context, req pluginhost.StartRequest) (pluginClient, error) {
	recordTestEvent(f.events, "start")
	f.started = append(f.started, req)
	if f.startErr != nil {
		return nil, f.startErr
	}
	if f.startResult != nil {
		return f.startResult, nil
	}
	return &fakePluginClient{manifest: req.Manifest}, nil
}

func (f *fakeServiceHost) Client(int) (pluginClient, error) {
	if f.clientErr != nil {
		return nil, f.clientErr
	}
	return f.clientResult, nil
}

func (f *fakeServiceHost) Stop(installationID int) error {
	recordTestEvent(f.events, "stop")
	f.stopped = append(f.stopped, installationID)
	return nil
}

func (f *fakeServiceHost) Shutdown(context.Context) error {
	return nil
}

type fakePluginClient struct {
	manifest *pluginv1.PluginManifest
}

func (f *fakePluginClient) Manifest() *pluginv1.PluginManifest {
	return f.manifest
}

func (f *fakePluginClient) MetadataProvider(string) (*pluginhost.MetadataProviderClient, error) {
	return nil, nil
}

func (f *fakePluginClient) MediaAnalyzer(string) (*pluginhost.MediaAnalyzerClient, error) {
	return nil, nil
}

func (f *fakePluginClient) ScheduledTask(string) (*pluginhost.ScheduledTaskClient, error) {
	return nil, nil
}

func (f *fakePluginClient) ScanSource(string) (*pluginhost.ScanSourceClient, error) {
	return nil, nil
}

func (f *fakePluginClient) EventConsumer(string) (*pluginhost.EventConsumerClient, error) {
	return nil, nil
}

func (f *fakePluginClient) AuthProvider(string) (*pluginhost.AuthProviderClient, error) {
	return nil, nil
}

func (f *fakePluginClient) HTTPRoutes(string) (*pluginhost.HTTPRoutesClient, error) {
	return nil, nil
}

type fakeServiceInstallationStore struct {
	byID             map[int]*Installation
	byPluginID       map[string][]*Installation
	createInputs     []CreateInstallationInput
	updateIDs        []int
	updateInputs     []UpdateInstallationInput
	deleteIDs        []int
	saveArchiveIDs   []int
	saveArchiveErr   error
	listCapabilities []*Capability
	events           *[]string
}

func newFakeServiceInstallationStore(installations ...*Installation) *fakeServiceInstallationStore {
	store := &fakeServiceInstallationStore{
		byID:       make(map[int]*Installation, len(installations)),
		byPluginID: make(map[string][]*Installation),
	}
	for _, installation := range installations {
		if installation == nil {
			continue
		}
		cloned := *installation
		store.byID[cloned.ID] = &cloned
		store.byPluginID[cloned.PluginID] = append(store.byPluginID[cloned.PluginID], &cloned)
	}
	return store
}

func (s *fakeServiceInstallationStore) Create(_ context.Context, input CreateInstallationInput) (*Installation, error) {
	recordTestEvent(s.events, "create")
	s.createInputs = append(s.createInputs, input)
	id := len(s.byID) + 1
	installation := &Installation{
		ID:          id,
		PluginID:    input.PluginID,
		Version:     input.Version,
		InstallPath: input.InstallPath,
		Enabled:     input.Enabled,
	}
	s.byID[id] = installation
	s.byPluginID[input.PluginID] = append(s.byPluginID[input.PluginID], installation)
	return installation, nil
}

func (s *fakeServiceInstallationStore) SaveArchive(_ context.Context, installationID int, _ []byte, _ string, _ []byte) error {
	recordTestEvent(s.events, "save_archive")
	s.saveArchiveIDs = append(s.saveArchiveIDs, installationID)
	return s.saveArchiveErr
}

func (s *fakeServiceInstallationStore) Update(_ context.Context, id int, input UpdateInstallationInput) error {
	recordTestEvent(s.events, "update")
	s.updateIDs = append(s.updateIDs, id)
	s.updateInputs = append(s.updateInputs, input)

	installation, ok := s.byID[id]
	if !ok {
		return nil
	}
	if input.Version != nil {
		installation.Version = *input.Version
	}
	if input.InstallPath != nil {
		installation.InstallPath = *input.InstallPath
	}
	if input.Enabled != nil {
		installation.Enabled = *input.Enabled
	}
	return nil
}

func (s *fakeServiceInstallationStore) Delete(_ context.Context, id int) error {
	recordTestEvent(s.events, "delete")
	s.deleteIDs = append(s.deleteIDs, id)
	delete(s.byID, id)
	return nil
}

func (s *fakeServiceInstallationStore) GetByID(_ context.Context, id int) (*Installation, error) {
	installation, ok := s.byID[id]
	if !ok {
		return nil, ErrInstallationNotFound
	}
	cloned := *installation
	return &cloned, nil
}

func (s *fakeServiceInstallationStore) List(context.Context) ([]*Installation, error) {
	result := make([]*Installation, 0, len(s.byID))
	for _, installation := range s.byID {
		cloned := *installation
		result = append(result, &cloned)
	}
	return result, nil
}

func (s *fakeServiceInstallationStore) ListEnabled(_ context.Context) ([]*Installation, error) {
	var result []*Installation
	for _, installation := range s.byID {
		if !installation.Enabled {
			continue
		}
		cloned := *installation
		result = append(result, &cloned)
	}
	return result, nil
}

func (s *fakeServiceInstallationStore) ListByPluginID(_ context.Context, pluginID string) ([]*Installation, error) {
	list := s.byPluginID[pluginID]
	result := make([]*Installation, 0, len(list))
	for _, installation := range list {
		cloned := *installation
		result = append(result, &cloned)
	}
	return result, nil
}

func (s *fakeServiceInstallationStore) ListCapabilities(context.Context, int) ([]*Capability, error) {
	return s.listCapabilities, nil
}

func (s *fakeServiceInstallationStore) GetArchive(context.Context, int) (*InstallationArchive, error) {
	return nil, ErrArchiveNotFound
}

func writeInstalledPluginManifest(t *testing.T, manifest *pluginv1.PluginManifest) string {
	t.Helper()

	installDir := t.TempDir()
	installPath := filepath.Join(installDir, "plugin")
	if err := os.WriteFile(installPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("WriteFile(%q) returned error: %v", installPath, err)
	}

	manifestBytes, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatalf("protojson.Marshal() returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "manifest.json"), manifestBytes, 0644); err != nil {
		t.Fatalf("WriteFile(manifest.json) returned error: %v", err)
	}

	return installPath
}

func writePluginArchive(t *testing.T, path string, manifest *pluginv1.PluginManifest) {
	t.Helper()

	binaryBytes := []byte("#!/bin/sh\nexit 0\n")
	manifestCopy := proto.Clone(manifest).(*pluginv1.PluginManifest)
	checksum := sha256.Sum256(binaryBytes)
	manifestCopy.Checksum = hex.EncodeToString(checksum[:])

	manifestBytes, err := protojson.Marshal(manifestCopy)
	if err != nil {
		t.Fatalf("protojson.Marshal() returned error: %v", err)
	}

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%q) returned error: %v", path, err)
	}
	defer file.Close()

	writer := zip.NewWriter(file)
	manifestEntry, err := writer.Create("manifest.json")
	if err != nil {
		t.Fatalf("Create(manifest.json) returned error: %v", err)
	}
	if _, err := manifestEntry.Write(manifestBytes); err != nil {
		t.Fatalf("Write(manifest.json) returned error: %v", err)
	}

	binaryEntry, err := writer.Create("plugin")
	if err != nil {
		t.Fatalf("Create(plugin) returned error: %v", err)
	}
	if _, err := binaryEntry.Write(binaryBytes); err != nil {
		t.Fatalf("Write(plugin) returned error: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}
}

func recordTestEvent(events *[]string, event string) {
	if events == nil {
		return
	}
	*events = append(*events, event)
}
