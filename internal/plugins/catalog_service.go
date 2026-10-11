package plugins

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"runtime"
	"slices"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type PlatformBinary struct {
	URL      string `json:"url"`
	Checksum string `json:"checksum"`
}

type CatalogPackage struct {
	Manifest     *pluginv1.PluginManifest  `json:"manifest"`
	RepoURL      string                    `json:"repo_url,omitempty"`
	ArchiveURL   string                    `json:"archive_url,omitempty"`
	ChecksumsURL string                    `json:"checksums_url,omitempty"`
	Binaries     map[string]PlatformBinary `json:"binaries,omitempty"`
}

type RepositoryIndex struct {
	Plugins []CatalogPackage `json:"plugins"`
}

type CatalogEntry struct {
	RepositoryID          int
	RepositoryDisplayName string
	SourceKind            string
	Manifest              *pluginv1.PluginManifest
	RepoURL               string
	ArchiveURL            string
	Checksum              string
}

type InstallCatalogRequest struct {
	RepositoryID int
	PluginID     string
	Version      string
}

type ResolvedCatalogInstall struct {
	RepositoryID  int
	PluginID      string
	Version       string
	ArchiveURL    string
	Checksum      string
	LegacyArchive bool
}

// ArchiveRequest is the installer request for a legacy archive target, with
// the same catalog identity as BinaryRequest.
func (t *ResolvedCatalogInstall) ArchiveRequest() InstallArchiveRequest {
	repositoryID := t.RepositoryID
	return InstallArchiveRequest{
		ArchiveURL:   t.ArchiveURL,
		RepositoryID: &repositoryID,
		PluginID:     t.PluginID,
		Version:      t.Version,
	}
}

// BinaryRequest is the installer request for a non-legacy target. It carries
// the catalog's plugin ID and version so the installer refuses a binary that
// names another plugin.
func (t *ResolvedCatalogInstall) BinaryRequest() InstallBinaryRequest {
	repositoryID := t.RepositoryID
	return InstallBinaryRequest{
		BinaryURL:    t.ArchiveURL,
		Checksum:     t.Checksum,
		RepositoryID: &repositoryID,
		PluginID:     t.PluginID,
		Version:      t.Version,
	}
}

type CatalogServiceOptions struct {
	HTTPClient     *http.Client
	SiloAPIVersion string
	CurrentOS      string
	CurrentArch    string
}

type CatalogService struct {
	repositories   *RepositoryStore
	httpClient     *http.Client
	siloAPIVersion string
	currentOS      string
	currentArch    string
}

func NewCatalogService(repositories *RepositoryStore, opts CatalogServiceOptions) *CatalogService {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	apiVersion := opts.SiloAPIVersion
	if apiVersion == "" {
		apiVersion = DefaultSiloAPIVersion
	}

	currentOS := opts.CurrentOS
	if currentOS == "" {
		currentOS = runtime.GOOS
	}
	currentArch := opts.CurrentArch
	if currentArch == "" {
		currentArch = runtime.GOARCH
	}

	return &CatalogService{
		repositories:   repositories,
		httpClient:     httpClient,
		siloAPIVersion: apiVersion,
		currentOS:      currentOS,
		currentArch:    currentArch,
	}
}

func (s *CatalogService) Fetch(ctx context.Context) ([]CatalogEntry, error) {
	repositories, err := s.repositories.List(ctx)
	if err != nil {
		return nil, err
	}

	catalogByVersion := make(map[string]CatalogEntry)
	for _, repository := range repositories {
		if !repository.Enabled {
			continue
		}

		index, err := s.fetchRepositoryIndex(ctx, repository.URL, "the "+repository.DisplayName+" catalog")
		if err != nil {
			slog.WarnContext(ctx, "skipping broken plugin repository", "component", "plugins",
				"repository_id", repository.ID,
				"repository_url", repository.URL,
				"error", err,
			)
			continue
		}

		now := time.Now().UTC()
		if err := s.repositories.Update(ctx, repository.ID, UpdateRepositoryInput{LastFetchedAt: &now}); err != nil {
			return nil, err
		}

		for _, pkg := range index.Plugins {
			entry, ok, err := s.catalogEntryFromPackage(repository, pkg)
			if err != nil {
				slog.WarnContext(ctx, "skipping invalid plugin catalog entry", "component", "plugins",
					"repository_id", repository.ID,
					"repository_url", repository.URL,
					"plugin_id", manifestPluginID(pkg.Manifest),
					"version", manifestVersion(pkg.Manifest),
					"error", err,
				)
				continue
			}
			if !ok {
				continue
			}

			key := fmt.Sprintf("%d:%s", entry.RepositoryID, capabilityKey(entry.Manifest.GetPluginId(), entry.Manifest.GetVersion()))
			if _, exists := catalogByVersion[key]; exists {
				continue
			}
			catalogByVersion[key] = entry
		}
	}

	entries := make([]CatalogEntry, 0, len(catalogByVersion))
	for _, entry := range catalogByVersion {
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(left, right CatalogEntry) int {
		if left.Manifest.GetPluginId() == right.Manifest.GetPluginId() {
			switch {
			case left.Manifest.GetVersion() < right.Manifest.GetVersion():
				return -1
			case left.Manifest.GetVersion() > right.Manifest.GetVersion():
				return 1
			default:
				return 0
			}
		}
		switch {
		case left.Manifest.GetPluginId() < right.Manifest.GetPluginId():
			return -1
		case left.Manifest.GetPluginId() > right.Manifest.GetPluginId():
			return 1
		default:
			return 0
		}
	})

	return entries, nil
}

func (s *CatalogService) ResolveInstall(ctx context.Context, req InstallCatalogRequest) (*ResolvedCatalogInstall, error) {
	if req.RepositoryID == 0 {
		return nil, fmt.Errorf("repository_id is required")
	}
	if strings.TrimSpace(req.PluginID) == "" {
		return nil, fmt.Errorf("plugin_id is required")
	}
	if strings.TrimSpace(req.Version) == "" {
		return nil, fmt.Errorf("version is required")
	}

	repository, err := s.repositories.GetByID(ctx, req.RepositoryID)
	if err != nil {
		return nil, err
	}
	if !repository.Enabled {
		return nil, packageError(fmt.Errorf("plugin repository %d is disabled", repository.ID),
			"The %s catalog is turned off. Turn it on before installing from it.", repository.DisplayName)
	}

	index, err := s.fetchRepositoryIndex(ctx, repository.URL, "the "+repository.DisplayName+" catalog")
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if err := s.repositories.Update(ctx, repository.ID, UpdateRepositoryInput{LastFetchedAt: &now}); err != nil {
		return nil, err
	}

	for _, pkg := range index.Plugins {
		if pkg.Manifest == nil {
			continue
		}
		if pkg.Manifest.GetPluginId() != req.PluginID || pkg.Manifest.GetVersion() != req.Version {
			continue
		}

		target, err := s.installTargetFromPackage(ctx, repository, pkg)
		if err != nil {
			return nil, err
		}
		target.PluginID = req.PluginID
		target.Version = req.Version
		return target, nil
	}

	return nil, packageError(fmt.Errorf("plugin %s@%s not found in repository %d", req.PluginID, req.Version, req.RepositoryID),
		"The %s catalog no longer lists %s %s. Refresh the catalog and try again.", repository.DisplayName, req.PluginID, req.Version)
}

// fetchRepositoryIndex reads a catalog index. subject names the catalog for
// the admin in the PackageError a failure returns.
func (s *CatalogService) fetchRepositoryIndex(ctx context.Context, repositoryURL, subject string) (*RepositoryIndex, error) {
	data, err := fetchPackageResource(ctx, s.httpClient, repositoryURL, subject)
	if err != nil {
		return nil, err
	}
	var index RepositoryIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, packageError(fmt.Errorf("decode repository index %q: %w", repositoryURL, err), "Silo couldn't read %s: it isn't a valid plugin catalog.", subject)
	}
	return &index, nil
}

func supportsPlatform(manifest *pluginv1.PluginManifest, osName, arch string) bool {
	for _, platform := range manifest.GetSupportedPlatforms() {
		if platform.GetOs() == osName && platform.GetArch() == arch {
			return true
		}
	}
	return false
}

func resolveRepositoryURL(repositoryURL, resourceURL string) (string, error) {
	if resourceURL == "" {
		return "", fmt.Errorf("plugin resource url is required")
	}
	baseURL, err := url.Parse(repositoryURL)
	if err != nil {
		return "", fmt.Errorf("parse repository url %q: %w", repositoryURL, err)
	}
	relativeURL, err := url.Parse(resourceURL)
	if err != nil {
		return "", fmt.Errorf("parse resource url %q: %w", resourceURL, err)
	}
	return baseURL.ResolveReference(relativeURL).String(), nil
}

func (s *CatalogService) catalogEntryFromPackage(repository *Repository, pkg CatalogPackage) (CatalogEntry, bool, error) {
	if len(pkg.Binaries) > 0 {
		if err := ValidateCatalogManifest(pkg.Manifest); err != nil {
			return CatalogEntry{}, false, err
		}
		if pkg.Manifest.GetSiloApiVersion() != s.siloAPIVersion {
			return CatalogEntry{}, false, nil
		}

		platformKey := s.currentOS + "/" + s.currentArch
		binary, ok := pkg.Binaries[platformKey]
		if !ok {
			return CatalogEntry{}, false, nil
		}
		if strings.TrimSpace(binary.URL) == "" {
			return CatalogEntry{}, false, fmt.Errorf("plugin binary url is required for platform %s", platformKey)
		}
		if strings.TrimSpace(binary.Checksum) == "" && strings.TrimSpace(pkg.ChecksumsURL) == "" {
			return CatalogEntry{}, false, fmt.Errorf("plugin binary checksum is required for platform %s", platformKey)
		}

		resolvedURL, err := resolveRepositoryURL(repository.URL, binary.URL)
		if err != nil {
			return CatalogEntry{}, false, err
		}
		checksum := strings.TrimSpace(binary.Checksum)
		if checksum != "" {
			checksum, err = normalizeSHA256Checksum(checksum)
			if err != nil {
				return CatalogEntry{}, false, err
			}
		}
		if strings.TrimSpace(pkg.ChecksumsURL) != "" {
			if _, err := resolveRepositoryURL(repository.URL, pkg.ChecksumsURL); err != nil {
				return CatalogEntry{}, false, err
			}
		}

		return CatalogEntry{
			RepositoryID:          repository.ID,
			RepositoryDisplayName: repository.DisplayName,
			SourceKind:            repository.SourceKind,
			Manifest:              pkg.Manifest,
			RepoURL:               pkg.RepoURL,
			ArchiveURL:            resolvedURL,
			Checksum:              checksum,
		}, true, nil
	}

	if err := ValidateManifest(pkg.Manifest); err != nil {
		return CatalogEntry{}, false, err
	}
	if pkg.Manifest.GetSiloApiVersion() != s.siloAPIVersion {
		return CatalogEntry{}, false, nil
	}
	if !supportsPlatform(pkg.Manifest, s.currentOS, s.currentArch) {
		return CatalogEntry{}, false, nil
	}

	resolvedURL, err := resolveRepositoryURL(repository.URL, pkg.ArchiveURL)
	if err != nil {
		return CatalogEntry{}, false, err
	}
	return CatalogEntry{
		RepositoryID:          repository.ID,
		RepositoryDisplayName: repository.DisplayName,
		SourceKind:            repository.SourceKind,
		Manifest:              pkg.Manifest,
		RepoURL:               pkg.RepoURL,
		ArchiveURL:            resolvedURL,
	}, true, nil
}

func (s *CatalogService) installTargetFromPackage(ctx context.Context, repository *Repository, pkg CatalogPackage) (*ResolvedCatalogInstall, error) {
	if len(pkg.Binaries) > 0 {
		if err := ValidateCatalogManifest(pkg.Manifest); err != nil {
			return nil, packageError(err, "The catalog's entry for this plugin is invalid: %v.", err)
		}
		if pkg.Manifest.GetSiloApiVersion() != s.siloAPIVersion {
			return nil, unsupportedAPIVersionError(pkg.Manifest.GetSiloApiVersion(), s.siloAPIVersion)
		}

		platformKey := s.currentOS + "/" + s.currentArch
		binary, ok := pkg.Binaries[platformKey]
		if !ok {
			return nil, unsupportedPlatformError(pkg.Manifest, platformKey)
		}
		if strings.TrimSpace(binary.URL) == "" {
			return nil, packageError(fmt.Errorf("plugin binary url is required for platform %s", platformKey),
				"The catalog lists no download for this server's platform (%s).", platformKey)
		}

		resolvedURL, err := resolveRepositoryURL(repository.URL, binary.URL)
		if err != nil {
			return nil, invalidCatalogAddressError(err)
		}

		checksum := strings.TrimSpace(binary.Checksum)
		if checksum != "" {
			checksum, err = normalizeSHA256Checksum(checksum)
			if err != nil {
				return nil, packageError(err, "The catalog lists an invalid SHA-256 checksum for this plugin.")
			}
		} else {
			if strings.TrimSpace(pkg.ChecksumsURL) == "" {
				return nil, packageError(fmt.Errorf("plugin binary checksum is required for platform %s", platformKey),
					"The catalog lists no SHA-256 checksum for this plugin, so Silo can't verify it.")
			}
			resolvedChecksumsURL, err := resolveRepositoryURL(repository.URL, pkg.ChecksumsURL)
			if err != nil {
				return nil, invalidCatalogAddressError(err)
			}
			checksum, err = s.fetchChecksumForBinary(ctx, resolvedChecksumsURL, resolvedURL)
			if err != nil {
				return nil, err
			}
		}

		return &ResolvedCatalogInstall{
			RepositoryID:  repository.ID,
			ArchiveURL:    resolvedURL,
			Checksum:      checksum,
			LegacyArchive: false,
		}, nil
	}

	if err := ValidateManifest(pkg.Manifest); err != nil {
		return nil, packageError(err, "The catalog's entry for this plugin is invalid: %v.", err)
	}
	if pkg.Manifest.GetSiloApiVersion() != s.siloAPIVersion {
		return nil, unsupportedAPIVersionError(pkg.Manifest.GetSiloApiVersion(), s.siloAPIVersion)
	}
	if !supportsPlatform(pkg.Manifest, s.currentOS, s.currentArch) {
		return nil, unsupportedPlatformError(pkg.Manifest, s.currentOS+"/"+s.currentArch)
	}

	resolvedURL, err := resolveRepositoryURL(repository.URL, pkg.ArchiveURL)
	if err != nil {
		return nil, invalidCatalogAddressError(err)
	}
	return &ResolvedCatalogInstall{
		RepositoryID:  repository.ID,
		ArchiveURL:    resolvedURL,
		LegacyArchive: true,
	}, nil
}

func (s *CatalogService) fetchChecksumForBinary(ctx context.Context, checksumsURL, binaryURL string) (string, error) {
	data, err := fetchPackageResource(ctx, s.httpClient, checksumsURL, "this plugin's checksum file")
	if err != nil {
		return "", err
	}
	checksum, err := checksumForBinary(string(data), binaryURL)
	if err != nil {
		return "", packageError(err, "This plugin's checksum file has no valid SHA-256 checksum for its download, so Silo can't verify it.")
	}
	return checksum, nil
}

func checksumForBinary(contents string, binaryURL string) (string, error) {
	parsedURL, err := url.Parse(binaryURL)
	if err != nil {
		return "", fmt.Errorf("parse binary url %q: %w", binaryURL, err)
	}
	filename := path.Base(parsedURL.Path)
	if filename == "." || filename == "/" || filename == "" {
		return "", fmt.Errorf("resolve checksum target from binary url %q", binaryURL)
	}

	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		checksum, entryFilename, ok, err := parseChecksumLine(line)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		if path.Base(entryFilename) == filename {
			return checksum, nil
		}
	}

	return "", fmt.Errorf("binary checksum for %q not found", filename)
}

func parseChecksumLine(line string) (checksum string, filename string, ok bool, err error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", false, nil
	}
	if len(line) < 65 {
		return "", "", false, fmt.Errorf("invalid checksum line %q", line)
	}

	checksum = strings.TrimSpace(line[:64])
	checksum, err = normalizeSHA256Checksum(checksum)
	if err != nil {
		return "", "", false, err
	}

	rest := strings.TrimLeft(line[64:], " \t")
	rest = strings.TrimPrefix(rest, "*")
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", false, fmt.Errorf("invalid checksum line %q", line)
	}
	return checksum, rest, true, nil
}

func normalizeSHA256Checksum(checksum string) (string, error) {
	checksum = strings.ToLower(strings.TrimSpace(checksum))
	if len(checksum) != 64 {
		return "", fmt.Errorf("invalid sha256 checksum %q", checksum)
	}
	if _, err := hex.DecodeString(checksum); err != nil {
		return "", fmt.Errorf("invalid sha256 checksum %q", checksum)
	}
	return checksum, nil
}

func manifestPluginID(manifest *pluginv1.PluginManifest) string {
	if manifest == nil {
		return ""
	}
	return manifest.GetPluginId()
}

func manifestVersion(manifest *pluginv1.PluginManifest) string {
	if manifest == nil {
		return ""
	}
	return manifest.GetVersion()
}

func unsupportedPlatformError(manifest *pluginv1.PluginManifest, platform string) error {
	return packageError(fmt.Errorf("plugin %s@%s does not support platform %s", manifest.GetPluginId(), manifest.GetVersion(), platform),
		"This plugin has no build for this server's platform (%s).", platform)
}

func invalidCatalogAddressError(err error) error {
	return packageError(err, "The catalog lists an invalid download address for this plugin.")
}
