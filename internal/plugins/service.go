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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type pluginClient interface {
	Manifest() *pluginv1.PluginManifest
	MetadataProvider(capabilityID string) (*pluginhost.MetadataProviderClient, error)
	ImageResolver(capabilityID string) (*pluginhost.ImageResolverClient, error)
	MarkerProvider(capabilityID string) (*pluginhost.MarkerProviderClient, error)
	MediaAnalyzer(capabilityID string) (*pluginhost.MediaAnalyzerClient, error)
	ScheduledTask(capabilityID string) (*pluginhost.ScheduledTaskClient, error)
	ScanSource(capabilityID string) (*pluginhost.ScanSourceClient, error)
	RequestRouter(capabilityID string) (*pluginhost.RequestRouterClient, error)
	EventConsumer(capabilityID string) (*pluginhost.EventConsumerClient, error)
	AuthProvider(capabilityID string) (*pluginhost.AuthProviderClient, error)
	HTTPRoutes(capabilityID string) (*pluginhost.HTTPRoutesClient, error)
	WatchSyncProvider(capabilityID string) (*pluginhost.WatchSyncProviderClient, error)
	NetworkAccessProvider(capabilityID string) (*pluginhost.NetworkAccessProviderClient, error)
	NotificationChannel(capabilityID string) (*pluginhost.NotificationChannelClient, error)
	ConfigurationChecks() (*pluginhost.ConfigurationChecksClient, error)
}

type Host interface {
	Start(ctx context.Context, req pluginhost.StartRequest) (pluginClient, error)
	Client(installationID int) (pluginClient, error)
	Stop(installationID int) error
	Shutdown(ctx context.Context) error
	// Running reports whether the installation's process is up. Unlike
	// Client it does not count as a use, so a status read cannot keep an
	// idle plugin running.
	Running(installationID int) bool
}

type serviceInstallationStore interface {
	archiveStore
	GetByID(ctx context.Context, id int) (*Installation, error)
	List(ctx context.Context) ([]*Installation, error)
	ListEnabled(ctx context.Context) ([]*Installation, error)
	ListEnabledWithCapabilityTypes(ctx context.Context, capabilityTypes []string) ([]*Installation, error)
	ListByPluginID(ctx context.Context, pluginID string) ([]*Installation, error)
	Update(ctx context.Context, id int, input UpdateInstallationInput) error
	ListCapabilities(ctx context.Context, installationID int) ([]*Capability, error)
}

type serviceConfigStore interface {
	ListGlobalConfigs(ctx context.Context, installationID int) ([]*RuntimeConfig, error)
	PutGlobalConfig(ctx context.Context, installationID int, key string, value map[string]any) error
	CompareAndSwapGlobalConfig(
		ctx context.Context,
		installationID int,
		key string,
		value map[string]any,
		expectedUpdatedAt *time.Time,
	) (bool, error)
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
	lifecycleHooks []func(context.Context, bool)
	launchGroup    singleflight.Group
	// Serialize operations on a process, including configuration and the
	// supervisor's acceptance of a launch. Distinct installations can
	// still launch independently.
	runtimeLocksMu sync.Mutex
	runtimeLocks   map[int]runtimeMutex
	running        runningInstallations
	resident       *ResidentSupervisor
	// lazyStartFailures records failed launches of non-resident plugins for
	// the admin runtime state; see RuntimeState.
	lazyStartFailures lazyStartFailures
	lifecycleBus      cache.EventBus // protected by lifecycleMu
	reconcileMu       sync.Mutex
	reconciled        map[int]*Installation

	// networkAccessHostInfo and networkAccessStatus back the network access
	// admin reads; see network_access.go.
	networkAccessHostInfo pluginhost.HostInfoFunc
	networkAccessStatus   NetworkAccessStatusSink
	networkAccessNodes    NetworkAccessNodes

	// installationCache memoizes plugin_installations rows keyed by ID so the
	// hot plugin-RPC path (ensureClient -> loadInstallation) and the metadata
	// chain enabled-check answer from memory instead of a per-call DB read. It
	// is wiped when lifecycle reconciliation observes changed rows.
	//
	// installationCacheGen guards the read-through against an invalidate that
	// races an in-flight GetByID: the generation is captured before the store
	// read and re-checked under the write lock, so a row fetched before a
	// lifecycle mutation is never written back into a freshly-cleared cache.
	installationCacheMu  sync.RWMutex
	installationCache    map[int]*Installation
	installationCacheGen uint64
}

// SetEventDispatcher wires the EventDispatcher into the Service and registers
// a lifecycle hook that drops the dispatcher's subscriber index, so an
// install, enable, disable, upgrade, or uninstall on this replica reaches the
// next event. Other replicas drop theirs on cache.EventPluginsChanged.
//
// The hook runs before every other lifecycle hook, so events that arrive
// while the slower hooks run (resident reconcile, provider reloads) already
// see the change.
func (s *Service) SetEventDispatcher(d *EventDispatcher) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.dispatcher = d
	if d != nil {
		s.lifecycleHooks = slices.Insert(s.lifecycleHooks, 0, func(_ context.Context, changed bool) {
			if changed {
				d.invalidateIndex()
			}
		})
	}
}

// AddLifecycleHook registers a callback invoked after plugin install, enable,
// disable, uninstall, preload, or runtime-configuration changes.
func (s *Service) AddLifecycleHook(hook func(context.Context)) {
	if s == nil || hook == nil {
		return
	}
	s.addLifecycleHook(func(ctx context.Context, changed bool) {
		if changed {
			hook(ctx)
		}
	})
}

// AddRetryableLifecycleHook runs and registers a refresh that keeps its last
// good state on error. A failed refresh is retried on later lifecycle polls,
// including polls that see no installation changes.
func (s *Service) AddRetryableLifecycleHook(ctx context.Context, name string, hook func(context.Context) error) {
	if s == nil || hook == nil {
		return
	}
	var mu sync.Mutex
	pending := true
	refresh := func(ctx context.Context, changed bool) {
		mu.Lock()
		defer mu.Unlock()
		if !changed && !pending {
			return
		}
		pending = true
		if err := hook(ctx); err != nil {
			slog.WarnContext(ctx, "plugin lifecycle refresh failed", "component", "plugins", "hook", name, "error", err)
			return
		}
		pending = false
	}
	s.addLifecycleHook(refresh)
	runLifecycleHook(ctx, refresh, false)
}

func (s *Service) addLifecycleHook(hook func(context.Context, bool)) {
	s.lifecycleMu.Lock()
	s.lifecycleHooks = append(s.lifecycleHooks, hook)
	s.lifecycleMu.Unlock()
}

// OnLifecycleChange is invoked by API handlers and the installer after every
// plugin lifecycle mutation. Secondary cache refreshes run on a best-effort
// basis so a refresh failure does not fail a committed admin operation.
func (s *Service) OnLifecycleChange(ctx context.Context) {
	if s == nil {
		return
	}
	if !s.reconcileInstallations(ctx) {
		s.InvalidateInstallationCache()
	}
	s.publishPluginsChanged(ctx, PluginsChangedEvent{})
	s.runLifecycleHooks(ctx, true)
}

func (s *Service) runLifecycleHooks(ctx context.Context, changed bool) {
	s.lifecycleMu.RLock()
	hooks := slices.Clone(s.lifecycleHooks)
	s.lifecycleMu.RUnlock()
	for _, hook := range hooks {
		runLifecycleHook(ctx, hook, changed)
	}
}

func runLifecycleHook(ctx context.Context, hook func(context.Context, bool), changed bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.ErrorContext(ctx,
				"plugin lifecycle hook panicked; continuing", "component", "plugins",
				"panic", recovered,
				"stack", string(debug.Stack()),
			)
		}
	}()
	hook(ctx, changed)
}

func NewService(
	repositories *RepositoryStore,
	installations *InstallationStore,
	configs *RuntimeConfigStore,
	catalog *CatalogService,
	installer *Installer,
	host Host,
) *Service {
	var cacheDir string
	if installer != nil {
		cacheDir = installer.baseDir
	}
	archiveCache := NewArchiveCacheAt(installations, cacheDir)
	if installations != nil {
		installations.archiveCache = archiveCache
	}
	svc := &Service{
		repositories:  repositories,
		installations: installations,
		configs:       configs,
		catalog:       catalog,
		installer:     installer,
		archiveCache:  archiveCache,
		host:          host,
	}
	if svc.archiveCache != nil && installations != nil && catalog != nil && installer != nil {
		// A server on another platform than the one that installed a
		// plugin fetches its own platform's build of it.
		svc.archiveCache.platforms = &platformArchives{store: installations, fetch: svc.fetchPlatformBuild}
	}
	// Resident plugins (network access providers) are reconciled after every
	// lifecycle change; the supervisor stays inert until StartResidents arms
	// it once the API listener is bound.
	svc.resident = newResidentSupervisor(svc, ResidentOptions{})
	svc.AddLifecycleHook(func(ctx context.Context) { svc.resident.Reconcile(ctx) })
	return svc
}

func (s *Service) FetchCatalog(ctx context.Context) ([]CatalogEntry, error) {
	entries, err := s.catalog.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	return catalogEntriesForDiscovery(entries), nil
}

func catalogEntriesForDiscovery(entries []CatalogEntry) []CatalogEntry {
	selected := make(map[string]CatalogEntry, len(entries))
	for _, entry := range entries {
		if entry.Manifest == nil {
			continue
		}
		pluginID := entry.Manifest.GetPluginId()
		if isApprovedCommunityPlugin(pluginID) && entry.SourceKind != RepositorySourceApprovedCommunity {
			continue
		}

		existing, ok := selected[pluginID]
		if !ok || catalogEntryPreferredForDiscovery(entry, existing) {
			selected[pluginID] = entry
		}
	}

	result := make([]CatalogEntry, 0, len(selected))
	for _, entry := range selected {
		result = append(result, entry)
	}
	slices.SortFunc(result, func(left, right CatalogEntry) int {
		return strings.Compare(left.Manifest.GetPluginId(), right.Manifest.GetPluginId())
	})
	return result
}

func catalogEntryPreferredForDiscovery(candidate, current CatalogEntry) bool {
	candidatePrecedence := repositorySourcePrecedence(candidate.SourceKind)
	currentPrecedence := repositorySourcePrecedence(current.SourceKind)
	if candidatePrecedence != currentPrecedence {
		return candidatePrecedence < currentPrecedence
	}
	if candidate.RepositoryID != current.RepositoryID {
		return candidate.RepositoryID < current.RepositoryID
	}
	return compareVersions(candidate.Manifest.GetVersion(), current.Manifest.GetVersion()) > 0
}

func repositorySourcePrecedence(sourceKind string) int {
	switch sourceKind {
	case RepositorySourceSilo:
		return 0
	case RepositorySourceApprovedCommunity:
		return 1
	default:
		return 2
	}
}

func (s *Service) InstallLocal(ctx context.Context, req InstallArchiveRequest) (*InstallResult, error) {
	if req.ArchivePath == "" {
		return nil, fmt.Errorf("archive path is required")
	}
	data, err := os.ReadFile(req.ArchivePath)
	if err != nil {
		return nil, fmt.Errorf("read archive %q: %w", req.ArchivePath, err)
	}
	return s.installArchive(ctx, data, req, func(pluginID string) (*Installation, error) {
		return s.existingInstallationByPluginID(ctx, pluginID)
	})
}

// InstallRemote installs the archive at req.ArchiveURL, replacing the
// plugin's installation when it already has one.
func (s *Service) InstallRemote(ctx context.Context, req InstallArchiveRequest) (*InstallResult, error) {
	if req.ArchiveURL == "" {
		return nil, errors.New("archive url is required")
	}
	// The plugin is known only once its archive is downloaded, so read every
	// installation first, as installCatalog reads its plugin's: another
	// install of the plugin that lands during the download then refuses this
	// one rather than being replaced by it.
	var before []*Installation
	if s.installations != nil {
		var err error
		if before, err = s.installations.List(ctx); err != nil {
			return nil, err
		}
	}
	data, err := s.installer.downloadArchive(ctx, req.ArchiveURL)
	if err != nil {
		return nil, err
	}
	return s.installArchive(ctx, data, req, func(pluginID string) (*Installation, error) {
		var existing *Installation
		for _, installation := range before {
			if installation.PluginID != pluginID {
				continue
			}
			if existing != nil {
				return nil, fmt.Errorf("multiple existing installations found for plugin %q", pluginID)
			}
			existing = installation
		}
		return existing, nil
	})
}

// installArchive installs an archive package as a new installation of its
// plugin, or as a replacement for the installation existingFor returns.
func (s *Service) installArchive(ctx context.Context, data []byte, req InstallArchiveRequest, existingFor func(pluginID string) (*Installation, error)) (*InstallResult, error) {
	// Refuse a package the installer would refuse before an installed copy
	// is stopped to replace it.
	_, _, manifest, err := openInstallArchive(data, req)
	if err != nil {
		return nil, err
	}

	existing, err := existingFor(manifest.GetPluginId())
	if err != nil {
		return nil, err
	}
	var result *InstallResult
	if existing != nil {
		if err := s.preflightManagedReplacement(ctx, existing, nil); err != nil {
			return nil, err
		}

		var prepared *PreparedReplacement
		prepared, err = s.installer.prepareArchive(existing, data, req)
		if err == nil {
			result, err = s.applyReplacement(ctx, existing, prepared)
		}
		if errors.Is(err, ErrInstallationNotFound) {
			// The installation this install replaces was uninstalled since it
			// was read: the request conflicts with that change, as when another
			// install replaced it.
			err = ErrInstallationChanged
		}
	} else {
		result, err = s.installer.installArchive(ctx, data, req)
	}
	if err != nil {
		return nil, err
	}
	s.OnLifecycleChange(ctx)
	return result, nil
}

func (s *Service) InstallCatalog(ctx context.Context, req InstallCatalogRequest) (*InstallResult, error) {
	return s.installCatalog(ctx, req, nil)
}

// installCatalog installs req. A non-nil expected is the installation the
// caller chose req for; otherwise it is read before the catalog resolves. The
// replacement is prepared against it, so another install that lands while the
// catalog resolves or the package downloads refuses this one rather than
// being replaced by it.
func (s *Service) installCatalog(ctx context.Context, req InstallCatalogRequest, expected *Installation) (*InstallResult, error) {
	existing := expected
	if existing == nil {
		var err error
		existing, err = s.existingInstallationByPluginID(ctx, req.PluginID)
		if err != nil {
			return nil, err
		}
	}
	target, err := s.catalog.ResolveInstall(ctx, req)
	if err != nil {
		return nil, err
	}

	if existing != nil {
		var repositoryID *int
		if !target.LegacyArchive {
			repositoryID = &target.RepositoryID
		}
		if err := s.preflightManagedReplacement(ctx, existing, repositoryID); err != nil {
			return nil, err
		}
	}

	var result *InstallResult
	switch {
	case existing != nil:
		// Download and check the package while the installed copy keeps
		// running; only a package that passed replaces it.
		var prepared *PreparedReplacement
		prepared, err = s.installer.PrepareCatalogReplacement(ctx, existing, target)
		if err == nil {
			result, err = s.applyReplacement(ctx, existing, prepared)
		}
	case target.LegacyArchive:
		result, err = s.installer.InstallRemote(ctx, target.ArchiveRequest())
	default:
		result, err = s.installer.InstallBinary(ctx, target.BinaryRequest())
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
	// A recorded version that isn't newer than the installed one is a stale
	// offer, such as one recorded after an admin installed a newer release:
	// installing it would downgrade the plugin (#4518).
	if installation.AvailableVersion == nil || *installation.AvailableVersion == "" ||
		compareVersions(*installation.AvailableVersion, installation.Version) <= 0 {
		return nil, fmt.Errorf("%w %q", ErrNoUpdateAvailable, installation.PluginID)
	}

	targetVersion := *installation.AvailableVersion

	if installation.RepositoryID == nil || *installation.RepositoryID == 0 {
		return nil, fmt.Errorf("plugin %q has no repository_id, cannot update from catalog", installation.PluginID)
	}

	_, err = s.installCatalog(ctx, InstallCatalogRequest{
		RepositoryID: *installation.RepositoryID,
		PluginID:     installation.PluginID,
		Version:      targetVersion,
	}, installation)
	if err != nil {
		return nil, fmt.Errorf("update plugin %q to %s: %w", installation.PluginID, targetVersion, err)
	}

	// The replacement cleared available_version in its own transaction. A
	// second clear here would erase a newer release an update check recorded
	// since (#4518).

	// Reload and return the updated installation.
	return s.installations.GetByID(ctx, installationID)
}

func (s *Service) InstallBinaryUpload(ctx context.Context, binaryData []byte) (*InstallResult, error) {
	if len(binaryData) == 0 {
		return nil, fmt.Errorf("binary data is required")
	}

	manifest, manifestJSON, err := s.installer.loadManifestFromBinary(ctx, binaryData)
	if err != nil {
		return nil, err
	}
	checksum := sha256.Sum256(binaryData)
	actualChecksum := hex.EncodeToString(checksum[:])

	var result *InstallResult
	if s.installations == nil {
		result, err = s.installer.installBinary(ctx, binaryData, actualChecksum, manifest, manifestJSON, nil, unsignedSignature, false)
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
		result, err = s.installer.installBinary(ctx, binaryData, actualChecksum, manifest, manifestJSON, nil, unsignedSignature, false)
		if err != nil {
			return nil, err
		}
		s.OnLifecycleChange(ctx)
		return result, nil
	}
	if len(existing) > 1 {
		return nil, fmt.Errorf("multiple existing installations found for plugin %q", manifest.GetPluginId())
	}

	if err := s.preflightManagedReplacement(ctx, existing[0], nil); err != nil {
		return nil, err
	}

	prepared, err := s.installer.prepareBinary(existing[0], binaryData, actualChecksum, manifest, manifestJSON, nil, unsignedSignature, false)
	if err != nil {
		return nil, err
	}
	result, err = s.applyReplacement(ctx, existing[0], prepared)
	if err != nil {
		return nil, err
	}
	s.OnLifecycleChange(ctx)
	return result, nil
}

// preflightManagedReplacement refuses an incompatible source before download. The installation transaction checks
// ownership again because a declaration may land during preparation.
func (s *Service) preflightManagedReplacement(ctx context.Context, existing *Installation, repositoryID *int) error {
	store, ok := s.installations.(*InstallationStore)
	if !ok {
		return nil
	}
	ownership, err := store.GetProvisionOwnership(ctx, existing.ID)
	if err != nil || !ownership.Managed {
		return err
	}
	if ownership.VersionPin != "" {
		return &ManagedByEnvironmentError{Field: managedVersionField}
	}
	if repositoryID == nil || existing.RepositoryID == nil || *repositoryID != *existing.RepositoryID {
		return &ManagedByEnvironmentError{Field: managedInstallationField}
	}
	return nil
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

// applyReplacement checks the expected package before committing and reports the
// current enabled flag. The caller reconciles running processes after commit.
func (s *Service) applyReplacement(ctx context.Context, existing *Installation, prepared *PreparedReplacement) (*InstallResult, error) {
	if s.installations != nil {
		if err := checkUnchanged(ctx, s.installations.GetByID, existing); err != nil {
			return nil, err
		}
	}
	result, err := prepared.Apply(ctx)
	if err != nil {
		return nil, err
	}
	if result.Installation != nil && s.installations != nil {
		// The replacement is already written; a failed read only leaves the
		// reported flag as loaded before the update.
		if current, err := s.installations.GetByID(ctx, result.Installation.ID); err != nil {
			slog.WarnContext(ctx, "reload replaced plugin installation", "component", "plugins",
				"installation_id", result.Installation.ID, "error", err)
		} else {
			result.Installation.Enabled = current.Enabled
		}
	}
	return result, nil
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
		// Builtin installations have no archive or binary.
		if installation.IsBuiltin() {
			continue
		}
		// A manifest read rehydrates and hashes the files as a launch does,
		// but doesn't fetch this platform's build of a plugin stored for
		// another platform: preload runs before the API listens, so a slow or
		// unreachable catalog must not hold startup. The first launch fetches.
		if _, err := s.ReadInstalledManifest(ctx, installation); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if errors.Is(err, ErrArchiveNotFound) {
				slog.WarnContext(ctx,
					"plugin preload skipped: archive not found for enabled installation", "component", "plugins",
					"installation_id", installation.ID,
					"plugin_id", installation.PluginID,
					"version", installation.Version,
				)
				continue
			}
			// One plugin whose stored archive no longer verifies must not
			// keep the server from starting; a fresh node loads every plugin
			// from its archive, so a fatal error here would crash-loop it.
			// The plugin stays unavailable and its RPCs report the same error.
			slog.ErrorContext(ctx,
				"plugin preload failed; continuing without it", "component", "plugins",
				"installation_id", installation.ID,
				"plugin_id", installation.PluginID,
				"version", installation.Version,
				"error", err,
			)
		}
	}
	s.OnLifecycleChange(ctx)
	return nil
}

func (s *Service) Start(ctx context.Context, installationID int) (pluginClient, error) {
	lock := s.runtimeLock(installationID)
	lock.Lock()
	defer lock.Unlock()
	return s.start(ctx, installationID, true, nil)
}

// start is called with the process lock held. Read the row directly on
// launches, and check it again after configuration and the host handshake.
// The configuration read may overlap a commit on another host.
func (s *Service) start(ctx context.Context, installationID int, allowResident bool, expected *installationRuntime) (client pluginClient, err error) {
	installation, err := s.installations.GetByID(ctx, installationID)
	if err != nil {
		// No launch was attempted: a disabled, missing or unreadable row says
		// nothing about whether the plugin can start.
		return nil, err
	}
	if installation.IsBuiltin() {
		return nil, ErrInstallationNotFound
	}
	if !installation.Enabled {
		return nil, ErrInstallationDisabled
	}
	runtime := runtimeOf(installation)
	if expected != nil && *expected != runtime {
		return nil, ErrInstallationChanged
	}
	// Record why a lazy launch failed, including an archive or config that
	// cannot be loaded, so the admin API can show a plugin that cannot start.
	// The failure is tied to the snapshot that was tried, not re-read, so an
	// update that lands mid-launch is not marked failed. The supervisor
	// reports failures of the resident plugins it launches itself; a refused
	// resident says nothing about the plugin.
	var manifest *pluginv1.PluginManifest
	refusedResident := false
	defer func() {
		supervised := allowResident && (manifest == nil || isResidentManifest(manifest))
		switch {
		case err == nil:
			s.lazyStartFailures.clear(installationID)
		case !supervised && !refusedResident && !errors.Is(err, ErrInstallationChanged):
			s.lazyStartFailures.record(installation, err)
		}
	}()
	manifest, err = s.ensureLoadedInstallation(ctx, installation)
	if err != nil {
		manifest = nil
		return nil, err
	}
	if !allowResident && isResidentManifest(manifest) {
		refusedResident = true
		return nil, fmt.Errorf("%w: resident plugin installation %d requires supervision", pluginhost.ErrPluginUnhealthy, installationID)
	}
	configEntries, err := s.globalConfigEntries(ctx, installation.ID)
	if err != nil {
		return nil, err
	}
	if s.configs != nil {
		if err := s.checkStartRuntime(ctx, installationID, runtime); err != nil {
			return nil, err
		}
	}
	binaryPath, err := s.localInstallPath(installation)
	if err != nil {
		return nil, err
	}
	client, err = s.host.Start(ctx, pluginhost.StartRequest{
		InstallationID: installation.ID,
		BinaryPath:     binaryPath,
		Manifest:       manifest,
		Config:         configEntries,
		// Every RPC to a lazily started plugin resolves its client through
		// ensureClient, which starts it again. A resident is the
		// supervisor's to keep up.
		StopWhenIdle: !isResidentManifest(manifest),
	})
	if err != nil || client == nil {
		// Binding this host's identity can fail before Host.Start retires the
		// previous process. Keep its record while it is still registered.
		if _, clientErr := s.host.Client(installationID); errors.Is(clientErr, pluginhost.ErrClientNotFound) {
			s.running.delete(installationID)
		}
		return client, err
	}
	s.running.delete(installationID)
	if err := s.checkStartRuntime(ctx, installationID, runtime); err != nil {
		return nil, errors.Join(err, s.stopRuntime(installationID))
	}
	s.running.store(installationID, runtime)
	return client, nil
}

func (s *Service) checkStartRuntime(ctx context.Context, id int, expected installationRuntime) error {
	current, err := s.installations.GetByID(ctx, id)
	if err != nil && !errors.Is(err, ErrInstallationNotFound) {
		return err
	}
	if current == nil || runtimeOf(current) != expected {
		s.InvalidateInstallationCache()
		return ErrInstallationChanged
	}
	return nil
}

// runtimeMutex serializes runtime work and lets callers with a deadline give
// up while the operation holding the lock continues.
type runtimeMutex chan struct{}

func (l runtimeMutex) Lock()   { l <- struct{}{} }
func (l runtimeMutex) Unlock() { <-l }

func (l runtimeMutex) tryLock() bool {
	select {
	case l <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l runtimeMutex) lockContext(ctx context.Context) error {
	select {
	case l <- struct{}{}:
		if err := ctx.Err(); err != nil {
			l.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) runtimeLock(id int) runtimeMutex {
	s.runtimeLocksMu.Lock()
	defer s.runtimeLocksMu.Unlock()
	lock := s.runtimeLocks[id]
	if lock == nil {
		if s.runtimeLocks == nil {
			s.runtimeLocks = make(map[int]runtimeMutex)
		}
		lock = make(runtimeMutex, 1)
		s.runtimeLocks[id] = lock
	}
	return lock
}

func (s *Service) runtimeMatches(installation *Installation) bool {
	running, ok := s.running.load(installation.ID)
	return ok && running == runtimeOf(installation)
}

// localInstallPath is where this host keeps the installation's binary: the
// archive cache's copy under this host's configured root.
func (s *Service) localInstallPath(installation *Installation) (string, error) {
	var cache *ArchiveCache
	if s != nil {
		cache = s.archiveCache
	}
	return cache.LocalInstallPath(installation)
}

func (s *Service) Stop(installationID int) error {
	lock := s.runtimeLock(installationID)
	lock.Lock()
	defer lock.Unlock()
	return s.stopRuntime(installationID)
}

// StopUnlessEnabled stops installation id's process after a change that
// disabled or removed it committed. It keeps the process only when the row,
// read again under the runtime lock, is read and enabled: an enable that
// committed and launched in between is newer than the change that asked for
// the stop. Every launch takes the same lock, so none can slip between the
// read and the stop. A row that is gone is stopped, and so is one that could
// not be read: a disabled or removed plugin left answering is worse than an
// enabled one briefly down, which its next call or reconcile starts again.
// The read's error is returned after the stop, for the caller to log.
func (s *Service) StopUnlessEnabled(ctx context.Context, installationID int) error {
	lock := s.runtimeLock(installationID)
	lock.Lock()
	defer lock.Unlock()
	installation, err := s.installations.GetByID(ctx, installationID)
	if err == nil && installation.Enabled {
		return nil
	}
	stopErr := s.stopRuntime(installationID)
	if err != nil && !errors.Is(err, ErrInstallationNotFound) {
		// A plugin that was not running is no failure; the read still is.
		if stopErr != nil && !errors.Is(stopErr, pluginhost.ErrClientNotFound) {
			return errors.Join(err, stopErr)
		}
		return err
	}
	return stopErr
}

// stopRuntime stops a process with its lock held.
func (s *Service) stopRuntime(installationID int) error {
	if s.host == nil {
		return nil
	}
	err := s.host.Stop(installationID)
	if err == nil || errors.Is(err, pluginhost.ErrClientNotFound) {
		s.running.delete(installationID)
	}
	return err
}

// RefreshMarkerRuntime discards local state after the marker registry observes
// a changed database revision, including changes made through another replica.
// Waiting for concurrent launches prevents an old process from appearing after
// the stop and being reused with the new revision.
func (s *Service) RefreshMarkerRuntime(installationID int) error {
	s.InvalidateInstallationCache()
	if err := s.Stop(installationID); err != nil && !errors.Is(err, pluginhost.ErrClientNotFound) {
		return err
	}
	// A plugin can expose both marker and resident capabilities. Restart under
	// supervision so a stopped resident does not wait for another lifecycle
	// event. Restart waits for a launch, so it must run outside the launch lock.
	if err := s.resident.Restart(context.Background(), installationID); err != nil && !errors.Is(err, ErrNotResident) {
		return err
	}
	return nil
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

func (s *Service) ImageResolverClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.ImageResolverClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.ImageResolver(capabilityID)
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
	// The task registry sees lifecycle changes on other API servers sooner
	// than the installation cache refresh. Resolve from the current row.
	s.installationCacheMu.Lock()
	delete(s.installationCache, installationID)
	s.installationCacheGen++
	s.installationCacheMu.Unlock()
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

func (s *Service) RequestRouterClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.RequestRouterClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.RequestRouter(capabilityID)
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

func (s *Service) WatchSyncProviderClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.WatchSyncProviderClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.WatchSyncProvider(capabilityID)
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

// NotificationChannelClient returns the installation's notification_channel.v1
// client for capabilityID, starting the plugin when needed.
func (s *Service) NotificationChannelClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.NotificationChannelClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.NotificationChannel(capabilityID)
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
	installation, manifest, err := s.loadForManifestRead(ctx, installationID, true)
	if err != nil {
		return "", err
	}
	binaryPath, err := s.localInstallPath(installation)
	if err != nil {
		return "", err
	}
	for _, asset := range manifest.GetAssets() {
		if asset.GetPath() == assetPath {
			resolved := filepath.Join(filepath.Dir(binaryPath), assetPath)
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

// ensureClient returns a running client for an RPC. Tracked residents may
// only use the process the supervisor owns; RPCs cannot launch or replace it.
func (s *Service) ensureClient(ctx context.Context, installationID int) (pluginClient, error) {
	if _, tracked := s.resident.State(installationID); tracked {
		return s.residentClient(ctx, installationID)
	}
	v, err, _ := s.launchGroup.Do(strconv.Itoa(installationID), func() (any, error) {
		// Isolate the shared launch from the leader caller's cancellation: other
		// waiters depend on this in-flight launch, so a single caller's canceled
		// request must not tear it down. Values (tracing, auth) are preserved.
		ctx := context.WithoutCancel(ctx)
		lock := s.runtimeLock(installationID)
		lock.Lock()
		defer lock.Unlock()
		for range 3 {
			client, err := s.doEnsureClient(ctx, installationID)
			if !errors.Is(err, ErrInstallationChanged) {
				return client, err
			}
		}
		return nil, ErrInstallationChanged
	})
	if err != nil {
		return nil, err
	}
	// A Host that reports success without a client leaves v nil.
	client, ok := v.(pluginClient)
	if !ok {
		return nil, fmt.Errorf("%w: plugin installation %d started without a client", pluginhost.ErrPluginUnhealthy, installationID)
	}
	return client, nil
}

func (s *Service) residentClient(ctx context.Context, installationID int) (pluginClient, error) {
	state, tracked := s.resident.State(installationID)
	if !tracked {
		return nil, ErrNotResident
	}
	if state.State != ResidentRunning {
		return nil, fmt.Errorf("%w: resident plugin installation %d is %s", pluginhost.ErrPluginUnhealthy, installationID, state.State)
	}
	lock := s.runtimeLock(installationID)
	if err := lock.lockContext(ctx); err != nil {
		return nil, err
	}
	defer lock.Unlock()
	installation, err := s.loadInstallation(ctx, installationID, true)
	if errors.Is(err, ErrInstallationDisabled) || (err == nil && !s.runtimeMatches(installation)) {
		// The supervisor can start a newer generation before the lifecycle poll
		// refreshes the cached installation. Confirm the row before refusing an RPC.
		s.InvalidateInstallationCache()
		installation, err = s.loadInstallation(ctx, installationID, true)
	}
	if err != nil {
		return nil, err
	}
	if state, tracked = s.resident.State(installationID); !tracked || state.State != ResidentRunning || !s.runtimeMatches(installation) {
		return nil, fmt.Errorf("%w: resident plugin installation %d is changing", pluginhost.ErrPluginUnhealthy, installationID)
	}
	return s.host.Client(installationID)
}

func (s *Service) doEnsureClient(ctx context.Context, installationID int) (pluginClient, error) {
	installation, err := s.loadInstallation(ctx, installationID, true)
	if err != nil {
		return nil, err
	}
	client, err := s.host.Client(installationID)
	if err == nil {
		cachedManifest := client.Manifest()
		if isResidentManifest(cachedManifest) {
			return nil, fmt.Errorf("%w: resident plugin installation %d requires supervision", pluginhost.ErrPluginUnhealthy, installationID)
		}
		running, recorded := s.running.load(installationID)
		if (recorded && running != runtimeOf(installation)) || (!recorded && installation.RuntimeGeneration != 0) {
			// The installation cache may have been populated just before a
			// launch read a newer row directly. Keep that current process.
			if recorded {
				if err := s.checkStartRuntime(ctx, installationID, running); err == nil {
					s.InvalidateInstallationCache()
					return client, nil
				} else if !errors.Is(err, ErrInstallationChanged) {
					return nil, err
				}
			}
			if stopErr := s.stopRuntime(installationID); stopErr != nil && !errors.Is(stopErr, pluginhost.ErrClientNotFound) {
				return nil, fmt.Errorf("stop stale plugin installation %d: %w", installationID, stopErr)
			}
			return s.start(ctx, installationID, false, nil)
		}
		binaryPath, pathErr := s.localInstallPath(installation)
		if pathErr != nil {
			return nil, pathErr
		}
		installedManifest, manifestErr := LoadManifestFile(InstalledManifestPath(binaryPath))
		if manifestErr != nil {
			// The files are not here (yet): on a proxy node the row may name
			// a release this host has not rehydrated. The running process is
			// still trustworthy only if it is the row's version.
			if installation.Version != "" && manifestVersion(cachedManifest) != installation.Version {
				slog.WarnContext(ctx, "plugin client runs a different version than the installation; restarting", "component", "plugins",
					"installation_id", installation.ID,
					"plugin_id", installation.PluginID,
					"cached_version", manifestVersion(cachedManifest),
					"installed_version", installation.Version,
				)
				if stopErr := s.stopRuntime(installationID); stopErr != nil && !errors.Is(stopErr, pluginhost.ErrClientNotFound) {
					return nil, fmt.Errorf("stop stale plugin installation %d: %w", installationID, stopErr)
				}
				return s.start(ctx, installationID, false, nil)
			}
			slog.WarnContext(ctx, "plugin installed manifest unavailable; reusing healthy client", "component", "plugins",
				"installation_id", installation.ID,
				"plugin_id", installation.PluginID,
				"version", installation.Version,
				"error", manifestErr,
			)
			return client, nil
		}
		// Compared without fields and enum values from a newer SDK, as the
		// host compares them at start.
		if cachedManifest != nil && proto.Equal(pluginhost.KnownManifest(cachedManifest), pluginhost.KnownManifest(installedManifest)) {
			return client, nil
		}
		slog.WarnContext(ctx, "plugin client manifest drift detected; restarting", "component", "plugins",
			"installation_id", installation.ID,
			"plugin_id", installation.PluginID,
			"cached_plugin_id", manifestPluginID(cachedManifest),
			"installed_plugin_id", installedManifest.GetPluginId(),
			"cached_version", manifestVersion(cachedManifest),
			"installed_version", installedManifest.GetVersion(),
		)
		if stopErr := s.stopRuntime(installationID); stopErr != nil && !errors.Is(stopErr, pluginhost.ErrClientNotFound) {
			return nil, fmt.Errorf("stop stale plugin installation %d: %w", installationID, stopErr)
		}
		return s.start(ctx, installationID, false, nil)
	}
	if errors.Is(err, pluginhost.ErrPluginUnhealthy) {
		if stopErr := s.stopRuntime(installationID); stopErr != nil && !errors.Is(stopErr, pluginhost.ErrClientNotFound) {
			return nil, fmt.Errorf("stop unhealthy plugin installation %d: %w", installationID, stopErr)
		}
		return s.start(ctx, installationID, false, nil)
	}
	if errors.Is(err, pluginhost.ErrClientNotFound) {
		return s.start(ctx, installationID, false, nil)
	}
	return nil, err
}

func (s *Service) manifestForInstallation(ctx context.Context, installationID int, requireEnabled bool) (*pluginv1.PluginManifest, error) {
	_, manifest, err := s.loadForManifestRead(ctx, installationID, requireEnabled)
	return manifest, err
}

// ensureInstallationCache loads the installation and fully verifies its
// files. Callers that are about to execute the binary use it.
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

// loadForManifestRead is ensureInstallationCache for callers that only read the
// manifest or serve packaged assets; see ArchiveCache.Manifest.
func (s *Service) loadForManifestRead(
	ctx context.Context,
	installationID int,
	requireEnabled bool,
) (*Installation, *pluginv1.PluginManifest, error) {
	installation, err := s.loadInstallation(ctx, installationID, requireEnabled)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := s.ReadInstalledManifest(ctx, installation)
	if err != nil {
		return nil, nil, err
	}
	return installation, manifest, nil
}

func (s *Service) loadInstallation(ctx context.Context, installationID int, requireEnabled bool) (*Installation, error) {
	installation, err := s.cachedInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}
	// The requireEnabled gate is applied after the cache read so the cache
	// stores the row regardless of its enabled state and ErrInstallationDisabled
	// semantics are unchanged.
	if requireEnabled && !installation.Enabled {
		return nil, ErrInstallationDisabled
	}
	// requireEnabled marks paths that intend to launch or serve the plugin
	// (start, manifest routes/assets, gRPC clients, HTTP proxy). The reserved
	// builtin row has no binary behind it and must never reach those paths;
	// not-found gives the proxy and API a clean 4xx. Reads with
	// requireEnabled=false (IsInstallationEnabled for the metadata chain,
	// generic listings) still see the row.
	if requireEnabled && installation.IsBuiltin() {
		return nil, ErrInstallationNotFound
	}
	return installation, nil
}

// cachedInstallation returns the plugin_installations row for installationID
// from the in-memory cache, loading it from the store on a miss. The returned
// *Installation is shared and must be treated as read-only by callers; it is
// evicted wholesale by InvalidateInstallationCache on every lifecycle change.
func (s *Service) cachedInstallation(ctx context.Context, installationID int) (*Installation, error) {
	s.installationCacheMu.RLock()
	cached, ok := s.installationCache[installationID]
	gen := s.installationCacheGen
	s.installationCacheMu.RUnlock()
	if ok {
		return cached, nil
	}

	installation, err := s.installations.GetByID(ctx, installationID)
	if err != nil {
		return nil, err
	}

	s.installationCacheMu.Lock()
	// Only publish the fetched row if no invalidation happened while GetByID was
	// in flight. Otherwise the row may pre-date a just-committed lifecycle change
	// (e.g. a disable), and writing it would resurrect stale state until the next
	// event. On a generation mismatch we still return the freshly-read row to the
	// caller but leave the cache untouched.
	if s.installationCacheGen == gen {
		if s.installationCache == nil {
			s.installationCache = make(map[int]*Installation)
		}
		s.installationCache[installationID] = installation
	}
	s.installationCacheMu.Unlock()

	return installation, nil
}

// InvalidateInstallationCache clears installation rows after a lifecycle
// mutation or before a periodic provider reload.
func (s *Service) InvalidateInstallationCache() {
	if s == nil {
		return
	}
	s.installationCacheMu.Lock()
	s.installationCache = nil
	s.installationCacheGen++
	s.installationCacheMu.Unlock()
}

// IsInstallationEnabled reports whether the given plugin installation is
// enabled, served from the in-memory installation cache. It backs the metadata
// chain's enabled-check (internal/metadata/chain.go) so provider construction
// no longer issues a per-capability SELECT on the hot path.
func (s *Service) IsInstallationEnabled(ctx context.Context, installationID int) (bool, error) {
	installation, err := s.loadInstallation(ctx, installationID, false)
	if err != nil {
		return false, err
	}
	return installation.Enabled, nil
}

// InstallationKind returns the installation's kind ("plugin" or "builtin") from
// the same in-memory cache IsInstallationEnabled reads, so metadata chain
// resolution can identify builtin rows without a per-capability DB query.
func (s *Service) InstallationKind(ctx context.Context, installationID int) (string, error) {
	installation, err := s.loadInstallation(ctx, installationID, false)
	if err != nil {
		return "", err
	}
	return installation.Kind, nil
}

// ensureLoadedInstallation makes the installation's files present, hashes the
// binary against the manifest, and returns the manifest.
func (s *Service) ensureLoadedInstallation(
	ctx context.Context,
	installation *Installation,
) (*pluginv1.PluginManifest, error) {
	if s.archiveCache == nil {
		binaryPath, err := s.localInstallPath(installation)
		if err != nil {
			return nil, err
		}
		return LoadManifestFile(InstalledManifestPath(binaryPath))
	}
	return s.archiveCache.Ensure(ctx, installation)
}

// ReadInstalledManifest reads the manifest for the supplied installation row.
// It can reuse a binary verification, so only callers that never execute the
// binary may use it.
func (s *Service) ReadInstalledManifest(
	ctx context.Context,
	installation *Installation,
) (*pluginv1.PluginManifest, error) {
	if s.archiveCache == nil {
		binaryPath, err := s.localInstallPath(installation)
		if err != nil {
			return nil, err
		}
		return LoadManifestFile(InstalledManifestPath(binaryPath))
	}
	return s.archiveCache.Manifest(ctx, installation)
}

// readInstalledManifestFrom is ReadInstalledManifest reading a stored
// archive, when it needs one, through archives.
func (s *Service) readInstalledManifestFrom(ctx context.Context, installation *Installation, archives archiveStore) (*pluginv1.PluginManifest, error) {
	if s.archiveCache == nil {
		return s.ReadInstalledManifest(ctx, installation)
	}
	return s.archiveCache.ManifestFrom(ctx, installation, archives)
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
