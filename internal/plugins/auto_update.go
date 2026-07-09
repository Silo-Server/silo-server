package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// compareVersions compares two dot-separated version strings numerically.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
// Non-numeric segments fall back to lexicographic comparison.
func compareVersions(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")

	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}

	for i := 0; i < maxLen; i++ {
		var segA, segB string
		if i < len(partsA) {
			segA = partsA[i]
		}
		if i < len(partsB) {
			segB = partsB[i]
		}

		numA, errA := strconv.Atoi(segA)
		numB, errB := strconv.Atoi(segB)

		if errA == nil && errB == nil {
			if numA < numB {
				return -1
			}
			if numA > numB {
				return 1
			}
		} else {
			if segA < segB {
				return -1
			}
			if segA > segB {
				return 1
			}
		}
	}
	return 0
}

const (
	DefaultRepositoryURL  = "https://raw.githubusercontent.com/Silo-Server/silo-plugins/main/manifest.json"
	DefaultRepositoryName = "Silo Official Plugins"
)

var defaultPluginIDs = []string{"silo.tmdb", "silo.tvdb"}

type autoUpdateRepositoryStore interface {
	List(ctx context.Context) ([]*Repository, error)
	Create(ctx context.Context, input CreateRepositoryInput) (*Repository, error)
}

type autoUpdateInstallationStore interface {
	List(ctx context.Context) ([]*Installation, error)
	Update(ctx context.Context, id int, input UpdateInstallationInput) error
	Delete(ctx context.Context, id int) error
}

type autoUpdateCatalog interface {
	Fetch(ctx context.Context) ([]CatalogEntry, error)
	ResolveInstall(ctx context.Context, req InstallCatalogRequest) (*ResolvedCatalogInstall, error)
}

type autoUpdateInstaller interface {
	InstallRemote(ctx context.Context, req InstallArchiveRequest) (*InstallResult, error)
	InstallBinary(ctx context.Context, req InstallBinaryRequest) (*InstallResult, error)
	ReplaceRemote(ctx context.Context, existing *Installation, req InstallArchiveRequest) (*InstallResult, error)
	ReplaceBinary(ctx context.Context, existing *Installation, req InstallBinaryRequest) (*InstallResult, error)
}

type autoUpdateHost interface {
	Stop(installationID int) error
}

type AutoUpdateOptions struct {
	SeedDefaultRepository bool
	AutoInstallDefaults   bool
}

type AutoUpdateSummary struct {
	RepositoriesSeeded      int      `json:"repositories_seeded"`
	CatalogEntries          int      `json:"catalog_entries"`
	InstalledPlugins        int      `json:"installed_plugins"`
	DefaultPluginsInstalled int      `json:"default_plugins_installed"`
	UpdatesApplied          int      `json:"updates_applied"`
	UpdatesAvailable        int      `json:"updates_available"`
	FailedOperations        int      `json:"failed_operations"`
	Failures                []string `json:"failures,omitempty"`
}

// AutoUpdateService seeds the default plugin repository, auto-installs default
// plugins, and auto-updates installed plugins at server startup.
type AutoUpdateService struct {
	repositories  autoUpdateRepositoryStore
	installations autoUpdateInstallationStore
	catalog       autoUpdateCatalog
	installer     autoUpdateInstaller
	host          autoUpdateHost
	logger        *slog.Logger

	// onChange is fired after a run mutates any plugin_installations row so
	// that peers sharing the same store (notably plugins.Service and its
	// installation cache) can invalidate their memoized state. It is optional:
	// when nil, no notification is sent. Pass plugins.Service.OnLifecycleChange
	// here to keep that service's installation cache consistent with the
	// version-specific InstallPath/Version this service writes.
	onChange func(context.Context)
}

// NewAutoUpdateService creates a new AutoUpdateService. onChange is optional and
// nil-safe: when non-nil it is invoked once after any run that mutates an
// installation row (auto-update applied, default plugin installed, or an
// available version recorded) so peers can invalidate cached installation state.
func NewAutoUpdateService(
	repositories autoUpdateRepositoryStore,
	installations autoUpdateInstallationStore,
	catalog autoUpdateCatalog,
	installer autoUpdateInstaller,
	host autoUpdateHost,
	logger *slog.Logger,
	onChange func(context.Context),
) *AutoUpdateService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AutoUpdateService{
		repositories:  repositories,
		installations: installations,
		catalog:       catalog,
		installer:     installer,
		host:          host,
		logger:        logger,
		onChange:      onChange,
	}
}

// Check runs a plugin update pass. It can be used by startup, scheduled tasks,
// and manual admin actions.
func (s *AutoUpdateService) Check(ctx context.Context, opts AutoUpdateOptions) (AutoUpdateSummary, error) {
	var summary AutoUpdateSummary

	if opts.SeedDefaultRepository {
		seeded, err := s.seedDefaultRepository(ctx)
		if err != nil {
			return summary, err
		}
		if seeded {
			summary.RepositoriesSeeded++
		}
	}

	entries, err := s.catalog.Fetch(ctx)
	if err != nil {
		return summary, err
	}
	summary.CatalogEntries = len(entries)

	installed, err := s.installations.List(ctx)
	if err != nil {
		return summary, err
	}
	summary.InstalledPlugins = len(installed)

	installedByPluginID := make(map[string]*Installation, len(installed))
	for _, inst := range installed {
		if inst == nil {
			continue
		}
		installedByPluginID[inst.PluginID] = inst
	}

	latestByPluginID := latestCatalogEntries(entries)
	for pluginID, entry := range latestByPluginID {
		existing, isInstalled := installedByPluginID[pluginID]

		if !isInstalled {
			if opts.AutoInstallDefaults {
				installedDefault, err := s.handleNewPlugin(ctx, pluginID, entry)
				if err != nil {
					summary.recordFailure("auto-install default plugin %s: %v", pluginID, err)
				} else if installedDefault {
					summary.DefaultPluginsInstalled++
				}
			}
			continue
		}

		outcome, err := s.handleExistingPlugin(ctx, existing, entry)
		if err != nil {
			summary.recordFailure("process plugin update %s: %v", pluginID, err)
			continue
		}
		switch outcome {
		case autoUpdateOutcomeUpdated:
			summary.UpdatesApplied++
		case autoUpdateOutcomeNotified:
			summary.UpdatesAvailable++
		}
	}

	// Any of these outcomes wrote to a plugin_installations row: installing a
	// default plugin creates one, an applied auto-update rewrites the version-
	// specific InstallPath/Version (and deletes the old install dir), and a
	// notify records available_version. Fire onChange once per run so peers such
	// as plugins.Service invalidate their installation cache; otherwise stale
	// rows (old InstallPath/Version) would make later plugin RPCs fail against a
	// re-extracted, newer archive.
	if summary.DefaultPluginsInstalled > 0 || summary.UpdatesApplied > 0 || summary.UpdatesAvailable > 0 {
		s.notifyChanged(ctx)
	}

	return summary, nil
}

// notifyChanged fires the optional onChange hook. It is nil-safe and
// best-effort: OnLifecycleChange already recovers hook panics internally, so a
// direct call cannot fail the update pass.
func (s *AutoUpdateService) notifyChanged(ctx context.Context) {
	if s.onChange == nil {
		return
	}
	s.onChange(ctx)
}

// Run seeds the default repository if needed, fetches the catalog, auto-installs
// default plugins that are not yet installed, and processes updates for installed
// plugins according to their update policy. All errors are logged rather than
// returned so that startup is never blocked.
func (s *AutoUpdateService) Run(ctx context.Context) error {
	summary, err := s.Check(ctx, AutoUpdateOptions{
		SeedDefaultRepository: true,
		AutoInstallDefaults:   true,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "failed to run plugin auto-update", "error", err)
		return nil
	}
	for _, failure := range summary.Failures {
		s.logger.WarnContext(ctx, "plugin auto-update operation failed", "error", failure)
	}
	return nil
}

// seedDefaultRepository creates the official plugin repository when no
// repositories are configured.
func (s *AutoUpdateService) seedDefaultRepository(ctx context.Context) (bool, error) {
	repos, err := s.repositories.List(ctx)
	if err != nil {
		return false, err
	}
	if len(repos) > 0 {
		return false, nil
	}

	enabled := true
	_, err = s.repositories.Create(ctx, CreateRepositoryInput{
		URL:         DefaultRepositoryURL,
		DisplayName: DefaultRepositoryName,
		Enabled:     &enabled,
	})
	if err != nil {
		return false, err
	}

	s.logger.InfoContext(ctx, "seeded default plugin repository",
		"url", DefaultRepositoryURL,
		"name", DefaultRepositoryName,
	)
	return true, nil
}

// handleNewPlugin auto-installs a plugin if it is in the default plugin list.
func (s *AutoUpdateService) handleNewPlugin(ctx context.Context, pluginID string, entry CatalogEntry) (bool, error) {
	if !slices.Contains(defaultPluginIDs, pluginID) {
		return false, nil
	}

	version := entry.Manifest.GetVersion()
	s.logger.InfoContext(ctx, "auto-installing default plugin",
		"plugin_id", pluginID,
		"version", version,
	)

	repoID := entry.RepositoryID
	target, err := s.catalog.ResolveInstall(ctx, InstallCatalogRequest{
		RepositoryID: repoID,
		PluginID:     pluginID,
		Version:      version,
	})
	if err == nil {
		_, err = s.installResolvedCatalogTarget(ctx, target)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// handleExistingPlugin checks for version updates and applies the installation's
// update policy.
func (s *AutoUpdateService) handleExistingPlugin(ctx context.Context, existing *Installation, entry CatalogEntry) (autoUpdateOutcome, error) {
	catalogVersion := entry.Manifest.GetVersion()
	if compareVersions(catalogVersion, existing.Version) <= 0 {
		return autoUpdateOutcomeNone, nil
	}

	switch existing.UpdatePolicy {
	case "auto":
		return autoUpdateOutcomeUpdated, s.autoUpdatePlugin(ctx, existing, entry)
	case "notify":
		return autoUpdateOutcomeNotified, s.notifyPluginUpdate(ctx, existing, entry)
	default:
		// "off" or any unrecognized policy: do nothing.
		return autoUpdateOutcomeNone, nil
	}
}

// autoUpdatePlugin stops the running plugin and replaces it in-place so the
// installation ID and dependent configuration rows remain stable.
func (s *AutoUpdateService) autoUpdatePlugin(ctx context.Context, existing *Installation, entry CatalogEntry) error {
	pluginID := existing.PluginID
	oldVersion := existing.Version
	newVersion := entry.Manifest.GetVersion()

	// Stop the running plugin if a host is available.
	if s.host != nil {
		if err := s.host.Stop(existing.ID); err != nil && !errors.Is(err, pluginhost.ErrClientNotFound) {
			return fmt.Errorf("stop plugin %s: %w", pluginID, err)
		}
	}

	// Install the new version.
	target, err := s.catalog.ResolveInstall(ctx, InstallCatalogRequest{
		RepositoryID: entry.RepositoryID,
		PluginID:     pluginID,
		Version:      newVersion,
	})
	if err == nil {
		repositoryID := target.RepositoryID
		if target.LegacyArchive {
			_, err = s.installer.ReplaceRemote(ctx, existing, InstallArchiveRequest{
				ArchiveURL:   target.ArchiveURL,
				RepositoryID: &repositoryID,
			})
		} else {
			_, err = s.installer.ReplaceBinary(ctx, existing, InstallBinaryRequest{
				BinaryURL:    target.ArchiveURL,
				Checksum:     target.Checksum,
				RepositoryID: &repositoryID,
			})
		}
	}
	if err != nil {
		return fmt.Errorf("install updated plugin %s from %s to %s: %w", pluginID, oldVersion, newVersion, err)
	}

	s.logger.InfoContext(ctx, "auto-updated plugin",
		"plugin_id", pluginID,
		"old_version", oldVersion,
		"new_version", newVersion,
	)
	return nil
}

// notifyPluginUpdate records the available version on the installation so the
// user can be informed through the UI.
func (s *AutoUpdateService) notifyPluginUpdate(ctx context.Context, existing *Installation, entry CatalogEntry) error {
	newVersion := entry.Manifest.GetVersion()

	if err := s.installations.Update(ctx, existing.ID, UpdateInstallationInput{
		AvailableVersion: &newVersion,
	}); err != nil {
		return fmt.Errorf("record available version for plugin %s: %w", existing.PluginID, err)
	}

	s.logger.InfoContext(ctx, "update available for plugin",
		"plugin_id", existing.PluginID,
		"installed_version", existing.Version,
		"available_version", newVersion,
	)
	return nil
}

// latestCatalogEntries returns a map from plugin ID to the catalog entry with
// the highest version string for that plugin.
func latestCatalogEntries(entries []CatalogEntry) map[string]CatalogEntry {
	latest := make(map[string]CatalogEntry, len(entries))
	for _, entry := range entries {
		pluginID := entry.Manifest.GetPluginId()
		if existing, ok := latest[pluginID]; ok {
			if compareVersions(entry.Manifest.GetVersion(), existing.Manifest.GetVersion()) <= 0 {
				continue
			}
		}
		latest[pluginID] = entry
	}
	return latest
}

func (s *AutoUpdateService) installResolvedCatalogTarget(ctx context.Context, target *ResolvedCatalogInstall) (*InstallResult, error) {
	if target == nil {
		return nil, fmt.Errorf("catalog install target is required")
	}

	repositoryID := target.RepositoryID
	if target.LegacyArchive {
		return s.installer.InstallRemote(ctx, InstallArchiveRequest{
			ArchiveURL:   target.ArchiveURL,
			RepositoryID: &repositoryID,
		})
	}

	return s.installer.InstallBinary(ctx, InstallBinaryRequest{
		BinaryURL:    target.ArchiveURL,
		Checksum:     target.Checksum,
		RepositoryID: &repositoryID,
	})
}

type autoUpdateOutcome int

const (
	autoUpdateOutcomeNone autoUpdateOutcome = iota
	autoUpdateOutcomeUpdated
	autoUpdateOutcomeNotified
)

func (s *AutoUpdateSummary) recordFailure(format string, args ...any) {
	s.FailedOperations++
	s.Failures = append(s.Failures, fmt.Sprintf(format, args...))
}
