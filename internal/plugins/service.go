package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type pluginClient interface {
	Manifest() *pluginv1.PluginManifest
	MetadataProvider(capabilityID string) (*pluginhost.MetadataProviderClient, error)
	MarkerProvider(capabilityID string) (*pluginhost.MarkerProviderClient, error)
	MediaAnalyzer(capabilityID string) (*pluginhost.MediaAnalyzerClient, error)
	ScheduledTask(capabilityID string) (*pluginhost.ScheduledTaskClient, error)
	ScanSource(capabilityID string) (*pluginhost.ScanSourceClient, error)
	EventConsumer(capabilityID string) (*pluginhost.EventConsumerClient, error)
	AuthProvider(capabilityID string) (*pluginhost.AuthProviderClient, error)
	HTTPRoutes(capabilityID string) (*pluginhost.HTTPRoutesClient, error)
}

type Host interface {
	Start(ctx context.Context, req pluginhost.StartRequest) (pluginClient, error)
	Client(installationID int) (pluginClient, error)
	Stop(installationID int) error
	Shutdown(ctx context.Context) error
}

type serviceInstallationStore interface {
	archiveStore
	GetByID(ctx context.Context, id int) (*Installation, error)
	List(ctx context.Context) ([]*Installation, error)
	ListEnabled(ctx context.Context) ([]*Installation, error)
	ListByPluginID(ctx context.Context, pluginID string) ([]*Installation, error)
	Update(ctx context.Context, id int, input UpdateInstallationInput) error
	ListCapabilities(ctx context.Context, installationID int) ([]*Capability, error)
}

type serviceConfigStore interface {
	ListGlobalConfigs(ctx context.Context, installationID int) ([]*RuntimeConfig, error)
	PutGlobalConfig(ctx context.Context, installationID int, key string, value map[string]any) error
}

type Service struct {
	repositories   *RepositoryStore
	installations  serviceInstallationStore
	configs        serviceConfigStore
	catalog        *CatalogService
	installer      *Installer
	archiveCache   *ArchiveCache
	host           Host
	testConfigSeq  atomic.Int64
	dispatcher     *EventDispatcher
	lifecycleMu    sync.RWMutex
	lifecycleHooks []func(context.Context)
}

// SetEventDispatcher wires the EventDispatcher into the Service. The
// dispatcher reference is retained so future hooks can act on lifecycle
// changes; the current dispatcher implementation is fully driven by
// per-event store reads and needs no notification on install/enable/disable.
func (s *Service) SetEventDispatcher(d *EventDispatcher) { s.dispatcher = d }

// AddLifecycleHook registers a callback invoked after plugin install, enable,
// disable, uninstall, or preload lifecycle changes.
func (s *Service) AddLifecycleHook(hook func(context.Context)) {
	if s == nil || hook == nil {
		return
	}
	s.lifecycleMu.Lock()
	s.lifecycleHooks = append(s.lifecycleHooks, hook)
	s.lifecycleMu.Unlock()
}

// OnLifecycleChange is invoked by API handlers and the installer after every
// plugin lifecycle mutation. Hooks should be best-effort and log their own
// errors so plugin admin operations are not failed by secondary cache refreshes.
func (s *Service) OnLifecycleChange(ctx context.Context) {
	if s == nil {
		return
	}
	s.lifecycleMu.RLock()
	hooks := append([]func(context.Context){}, s.lifecycleHooks...)
	s.lifecycleMu.RUnlock()
	for _, hook := range hooks {
		func(hook func(context.Context)) {
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error(
						"plugin lifecycle hook panicked; continuing",
						"panic", recovered,
						"stack", string(debug.Stack()),
					)
				}
			}()
			hook(ctx)
		}(hook)
	}
}

func NewService(
	repositories *RepositoryStore,
	installations *InstallationStore,
	configs *RuntimeConfigStore,
	catalog *CatalogService,
	installer *Installer,
	host Host,
) *Service {
	return &Service{
		repositories:  repositories,
		installations: installations,
		configs:       configs,
		catalog:       catalog,
		installer:     installer,
		archiveCache:  NewArchiveCache(installations),
		host:          host,
	}
}

func (s *Service) FetchCatalog(ctx context.Context) ([]CatalogEntry, error) {
	return s.catalog.Fetch(ctx)
}

func (s *Service) InstallLocal(ctx context.Context, req InstallArchiveRequest) (*InstallResult, error) {
	if req.ArchivePath == "" {
		return nil, fmt.Errorf("archive path is required")
	}
	data, err := os.ReadFile(req.ArchivePath)
	if err != nil {
		return nil, fmt.Errorf("read archive %q: %w", req.ArchivePath, err)
	}
	_, _, manifest, err := openPluginArchive(data)
	if err != nil {
		return nil, err
	}

	existing, err := s.existingInstallationByPluginID(ctx, manifest.GetPluginId())
	if err != nil {
		return nil, err
	}
	var result *InstallResult
	if existing != nil {
		if err := s.stopInstallationIfRunning(existing); err != nil {
			return nil, err
		}
		result, err = s.installer.ReplaceLocal(ctx, existing, req)
	} else {
		result, err = s.installer.InstallLocal(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	s.OnLifecycleChange(ctx)
	return result, nil
}

func (s *Service) InstallRemote(ctx context.Context, req InstallArchiveRequest) (*InstallResult, error) {
	result, err := s.installer.InstallRemote(ctx, req)
	if err != nil {
		return nil, err
	}
	s.OnLifecycleChange(ctx)
	return result, nil
}

func (s *Service) InstallCatalog(ctx context.Context, req InstallCatalogRequest) (*InstallResult, error) {
	target, err := s.catalog.ResolveInstall(ctx, req)
	if err != nil {
		return nil, err
	}

	repositoryID := target.RepositoryID
	existing, err := s.existingInstallationByPluginID(ctx, req.PluginID)
	if err != nil {
		return nil, err
	}
	var result *InstallResult
	if target.LegacyArchive {
		archiveReq := InstallArchiveRequest{
			ArchiveURL:   target.ArchiveURL,
			RepositoryID: &repositoryID,
		}
		if existing == nil {
			result, err = s.installer.InstallRemote(ctx, archiveReq)
		} else {
			if err = s.stopInstallationIfRunning(existing); err != nil {
				return nil, err
			}
			result, err = s.installer.ReplaceRemote(ctx, existing, archiveReq)
		}
	} else {
		binaryReq := InstallBinaryRequest{
			BinaryURL:    target.ArchiveURL,
			Checksum:     target.Checksum,
			RepositoryID: &repositoryID,
		}
		if existing == nil {
			result, err = s.installer.InstallBinary(ctx, binaryReq)
		} else {
			if err = s.stopInstallationIfRunning(existing); err != nil {
				return nil, err
			}
			result, err = s.installer.ReplaceBinary(ctx, existing, binaryReq)
		}
	}
	if err != nil {
		return nil, err
	}
	s.OnLifecycleChange(ctx)
	return result, nil
}

// UpdateToAvailableVersion updates a plugin to its available_version.
// Returns the updated installation after the update completes.
func (s *Service) UpdateToAvailableVersion(ctx context.Context, installationID int) (*Installation, error) {
	installation, err := s.installations.GetByID(ctx, installationID)
	if err != nil {
		return nil, err
	}
	if installation.AvailableVersion == nil || *installation.AvailableVersion == "" {
		return nil, fmt.Errorf("no update available for plugin %q", installation.PluginID)
	}

	targetVersion := *installation.AvailableVersion

	if installation.RepositoryID == nil || *installation.RepositoryID == 0 {
		return nil, fmt.Errorf("plugin %q has no repository_id, cannot update from catalog", installation.PluginID)
	}

	_, err = s.InstallCatalog(ctx, InstallCatalogRequest{
		RepositoryID: *installation.RepositoryID,
		PluginID:     installation.PluginID,
		Version:      targetVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("update plugin %q to %s: %w", installation.PluginID, targetVersion, err)
	}

	// Clear available_version now that we've updated.
	empty := ""
	if err := s.installations.Update(ctx, installationID, UpdateInstallationInput{
		AvailableVersion: &empty,
	}); err != nil {
		slog.Warn("failed to clear available_version after update",
			"installation_id", installationID, "error", err)
	}

	// Reload and return the updated installation.
	return s.installations.GetByID(ctx, installationID)
}

func (s *Service) InstallBinary(ctx context.Context, req InstallBinaryRequest) (*InstallResult, error) {
	var result *InstallResult
	var err error
	if req.Manifest != nil && req.Manifest.GetPluginId() != "" {
		existing, existErr := s.existingInstallationByPluginID(ctx, req.Manifest.GetPluginId())
		if existErr != nil {
			return nil, existErr
		}
		if existing != nil {
			if err = s.stopInstallationIfRunning(existing); err != nil {
				return nil, err
			}
			result, err = s.installer.ReplaceBinary(ctx, existing, req)
			if err != nil {
				return nil, err
			}
			s.OnLifecycleChange(ctx)
			return result, nil
		}
	}
	result, err = s.installer.InstallBinary(ctx, req)
	if err != nil {
		return nil, err
	}
	s.OnLifecycleChange(ctx)
	return result, nil
}

func (s *Service) InstallBinaryUpload(ctx context.Context, binaryData []byte) (*InstallResult, error) {
	if len(binaryData) == 0 {
		return nil, fmt.Errorf("binary data is required")
	}

	manifest, err := loadManifestFromBinary(ctx, binaryData)
	if err != nil {
		return nil, err
	}
	checksum := sha256.Sum256(binaryData)
	actualChecksum := hex.EncodeToString(checksum[:])

	var result *InstallResult
	if s.installations == nil {
		result, err = s.installer.installBinary(ctx, binaryData, actualChecksum, manifest, nil)
		if err != nil {
			return nil, err
		}
		s.OnLifecycleChange(ctx)
		return result, nil
	}

	existing, err := s.installations.ListByPluginID(ctx, manifest.GetPluginId())
	if err != nil {
		return nil, fmt.Errorf("list existing plugin installations for %q: %w", manifest.GetPluginId(), err)
	}
	if len(existing) == 0 {
		result, err = s.installer.installBinary(ctx, binaryData, actualChecksum, manifest, nil)
		if err != nil {
			return nil, err
		}
		s.OnLifecycleChange(ctx)
		return result, nil
	}
	if len(existing) > 1 {
		return nil, fmt.Errorf("multiple existing installations found for plugin %q", manifest.GetPluginId())
	}

	oldInstallation := existing[0]
	if err := s.stopInstallationIfRunning(oldInstallation); err != nil {
		return nil, err
	}
	result, err = s.installer.replaceBinary(ctx, oldInstallation, binaryData, actualChecksum, manifest)
	if err != nil {
		return nil, err
	}
	s.OnLifecycleChange(ctx)
	return result, nil
}

func (s *Service) existingInstallationByPluginID(ctx context.Context, pluginID string) (*Installation, error) {
	if s.installations == nil || pluginID == "" {
		return nil, nil
	}

	existing, err := s.installations.ListByPluginID(ctx, pluginID)
	if err != nil {
		return nil, fmt.Errorf("list existing plugin installations for %q: %w", pluginID, err)
	}
	if len(existing) == 0 {
		return nil, nil
	}
	if len(existing) > 1 {
		return nil, fmt.Errorf("multiple existing installations found for plugin %q", pluginID)
	}
	return existing[0], nil
}

func (s *Service) stopInstallationIfRunning(existing *Installation) error {
	if existing == nil || !existing.Enabled || s.host == nil {
		return nil
	}
	if err := s.host.Stop(existing.ID); err != nil && !errors.Is(err, pluginhost.ErrClientNotFound) {
		return fmt.Errorf("stop existing plugin installation %d: %w", existing.ID, err)
	}
	return nil
}

func (s *Service) PreloadEnabled(ctx context.Context) error {
	if s.installations == nil {
		return nil
	}

	installations, err := s.installations.ListEnabled(ctx)
	if err != nil {
		return err
	}
	for _, installation := range installations {
		if installation == nil {
			continue
		}
		if _, err := s.ensureLoadedInstallation(ctx, installation); err != nil {
			if errors.Is(err, ErrArchiveNotFound) {
				slog.Warn(
					"plugin preload skipped: archive not found for enabled installation",
					"installation_id", installation.ID,
					"plugin_id", installation.PluginID,
					"version", installation.Version,
				)
				continue
			}
			return fmt.Errorf("preload plugin installation %d: %w", installation.ID, err)
		}
	}
	s.OnLifecycleChange(ctx)
	return nil
}

func (s *Service) Start(ctx context.Context, installationID int) (pluginClient, error) {
	installation, manifest, err := s.ensureInstallationCache(ctx, installationID, true)
	if err != nil {
		return nil, err
	}
	configEntries, err := s.globalConfigEntries(ctx, installation.ID)
	if err != nil {
		return nil, err
	}
	return s.host.Start(ctx, pluginhost.StartRequest{
		InstallationID: installation.ID,
		BinaryPath:     installation.InstallPath,
		Manifest:       manifest,
		Config:         configEntries,
	})
}

func (s *Service) Stop(installationID int) error {
	if s.host == nil {
		return nil
	}
	return s.host.Stop(installationID)
}

func (s *Service) MediaAnalyzerClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.MediaAnalyzerClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.MediaAnalyzer(capabilityID)
}

func (s *Service) MetadataProviderClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.MetadataProviderClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.MetadataProvider(capabilityID)
}

func (s *Service) MarkerProviderClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.MarkerProviderClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.MarkerProvider(capabilityID)
}

func (s *Service) ScheduledTaskClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.ScheduledTaskClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.ScheduledTask(capabilityID)
}

func (s *Service) ScanSourceClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.ScanSourceClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.ScanSource(capabilityID)
}

func (s *Service) ScanSourceClientByPluginID(
	ctx context.Context,
	pluginID string,
	capabilityID string,
) (*pluginhost.ScanSourceClient, error) {
	if s == nil || s.installations == nil {
		return nil, fmt.Errorf("scan source plugin resolver is not configured")
	}
	installations, err := s.installations.ListByPluginID(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	if len(installations) == 0 {
		return nil, fmt.Errorf("scan source plugin %q is not installed", pluginID)
	}
	if len(installations) > 1 {
		return nil, fmt.Errorf("scan source plugin %q is ambiguous across %d installations", pluginID, len(installations))
	}

	var matches []*Installation
	for _, installation := range installations {
		if installation == nil {
			continue
		}
		capabilities, err := s.installations.ListCapabilities(ctx, installation.ID)
		if err != nil {
			return nil, err
		}
		for _, capability := range capabilities {
			if capability == nil {
				continue
			}
			if capability.Type == "scan_source.v1" && capability.ID == capabilityID {
				matches = append(matches, installation)
				break
			}
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("scan source capability %q is not installed for plugin %q", capabilityID, pluginID)
	}
	if !matches[0].Enabled {
		return nil, fmt.Errorf("scan source plugin %q is disabled", pluginID)
	}
	return s.ScanSourceClient(ctx, matches[0].ID, capabilityID)
}

func (s *Service) EventConsumerClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.EventConsumerClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.EventConsumer(capabilityID)
}

func (s *Service) AuthProviderClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.AuthProviderClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.AuthProvider(capabilityID)
}

func (s *Service) HTTPRoutesClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.HTTPRoutesClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.HTTPRoutes(capabilityID)
}

func (s *Service) RouteDescriptors(ctx context.Context, installationID int) ([]*pluginv1.HttpRouteDescriptor, error) {
	manifest, err := s.manifestForInstallation(ctx, installationID, true)
	if err != nil {
		return nil, err
	}
	return append([]*pluginv1.HttpRouteDescriptor(nil), manifest.GetHttpRoutes()...), nil
}

func (s *Service) ResolveAssetPath(ctx context.Context, installationID int, assetPath string) (string, error) {
	installation, manifest, err := s.ensureInstallationCache(ctx, installationID, true)
	if err != nil {
		return "", err
	}
	for _, asset := range manifest.GetAssets() {
		if asset.GetPath() == assetPath {
			resolved := filepath.Join(filepath.Dir(installation.InstallPath), assetPath)
			if _, err := os.Stat(resolved); err != nil {
				return "", fmt.Errorf("plugin asset %q: %w", assetPath, err)
			}
			return resolved, nil
		}
	}
	return "", fmt.Errorf("plugin asset %q not found", assetPath)
}

func (s *Service) UserConfigSchema(ctx context.Context, installationID int) ([]*pluginv1.ConfigSchema, error) {
	manifest, err := s.manifestForInstallation(ctx, installationID, false)
	if err != nil {
		return nil, err
	}
	return append([]*pluginv1.ConfigSchema(nil), manifest.GetUserConfigSchema()...), nil
}

func (s *Service) ManifestForInstallation(
	ctx context.Context,
	installationID int,
) (*pluginv1.PluginManifest, error) {
	return s.manifestForInstallation(ctx, installationID, false)
}

func (s *Service) ensureClient(ctx context.Context, installationID int) (pluginClient, error) {
	installation, err := s.loadInstallation(ctx, installationID, true)
	if err != nil {
		return nil, err
	}
	client, err := s.host.Client(installationID)
	if err == nil {
		installedManifest, manifestErr := LoadManifestFile(InstalledManifestPath(installation.InstallPath))
		if manifestErr != nil {
			slog.Warn("plugin installed manifest unavailable; reusing healthy client",
				"installation_id", installation.ID,
				"plugin_id", installation.PluginID,
				"version", installation.Version,
				"error", manifestErr,
			)
			return client, nil
		}
		cachedManifest := client.Manifest()
		if cachedManifest != nil && proto.Equal(cachedManifest, installedManifest) {
			return client, nil
		}
		slog.Warn("plugin client manifest drift detected; restarting",
			"installation_id", installation.ID,
			"plugin_id", installation.PluginID,
			"cached_plugin_id", manifestPluginID(cachedManifest),
			"installed_plugin_id", installedManifest.GetPluginId(),
			"cached_version", manifestVersion(cachedManifest),
			"installed_version", installedManifest.GetVersion(),
		)
		if stopErr := s.host.Stop(installationID); stopErr != nil && !errors.Is(stopErr, pluginhost.ErrClientNotFound) {
			return nil, fmt.Errorf("stop stale plugin installation %d: %w", installationID, stopErr)
		}
		return s.Start(ctx, installationID)
	}
	if errors.Is(err, pluginhost.ErrPluginUnhealthy) {
		if stopErr := s.host.Stop(installationID); stopErr != nil && !errors.Is(stopErr, pluginhost.ErrClientNotFound) {
			return nil, fmt.Errorf("stop unhealthy plugin installation %d: %w", installationID, stopErr)
		}
		return s.Start(ctx, installationID)
	}
	if errors.Is(err, pluginhost.ErrClientNotFound) {
		return s.Start(ctx, installationID)
	}
	return nil, err
}

func (s *Service) manifestForInstallation(ctx context.Context, installationID int, requireEnabled bool) (*pluginv1.PluginManifest, error) {
	_, manifest, err := s.ensureInstallationCache(ctx, installationID, requireEnabled)
	return manifest, err
}

func (s *Service) ensureInstallationCache(
	ctx context.Context,
	installationID int,
	requireEnabled bool,
) (*Installation, *pluginv1.PluginManifest, error) {
	installation, err := s.loadInstallation(ctx, installationID, requireEnabled)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := s.ensureLoadedInstallation(ctx, installation)
	if err != nil {
		return nil, nil, err
	}
	return installation, manifest, nil
}

func (s *Service) loadInstallation(ctx context.Context, installationID int, requireEnabled bool) (*Installation, error) {
	installation, err := s.installations.GetByID(ctx, installationID)
	if err != nil {
		return nil, err
	}
	if requireEnabled && !installation.Enabled {
		return nil, ErrInstallationDisabled
	}
	return installation, nil
}

func (s *Service) ensureLoadedInstallation(
	ctx context.Context,
	installation *Installation,
) (*pluginv1.PluginManifest, error) {
	if s.archiveCache == nil {
		return LoadManifestFile(InstalledManifestPath(installation.InstallPath))
	}
	return s.archiveCache.Ensure(ctx, installation)
}

func (s *Service) globalConfigEntries(ctx context.Context, installationID int) ([]*pluginv1.ConfigEntry, error) {
	if s.configs == nil {
		return nil, nil
	}

	configs, err := s.configs.ListGlobalConfigs(ctx, installationID)
	if err != nil {
		return nil, fmt.Errorf("list plugin runtime configs for installation %d: %w", installationID, err)
	}

	entries := make([]*pluginv1.ConfigEntry, 0, len(configs))
	for _, config := range configs {
		if config == nil {
			continue
		}

		value := config.Value
		if value == nil {
			value = map[string]any{}
		}

		structValue, err := structpb.NewStruct(value)
		if err != nil {
			return nil, fmt.Errorf(
				"encode runtime config %q for installation %d: %w",
				config.Key,
				installationID,
				err,
			)
		}

		entries = append(entries, &pluginv1.ConfigEntry{
			Key:   config.Key,
			Value: structValue,
		})
	}

	return entries, nil
}
