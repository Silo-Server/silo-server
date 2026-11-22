package plugins

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"golang.org/x/mod/semver"
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
	// BundleURL is the release's Sigstore bundle (plugin.sigstore.json),
	// which attests every binary in Binaries.
	BundleURL string `json:"bundle_url,omitempty"`
	Tag       string `json:"tag,omitempty"`
}

type RepositoryIndex struct {
	Plugins     []CatalogPackage    `json:"plugins"`
	Withdrawals []CatalogWithdrawal `json:"withdrawals,omitempty"`
}

type CatalogWithdrawal struct {
	PluginID string `json:"plugin_id"`
	Version  string `json:"version"`
}

type catalogSnapshot struct {
	entries     []CatalogEntry
	withdrawals map[int][]CatalogWithdrawal
}

func (s catalogSnapshot) withdrawn(repositoryID int, pluginID, version string) bool {
	return slices.Contains(s.withdrawals[repositoryID], CatalogWithdrawal{PluginID: pluginID, Version: version})
}

func (i *RepositoryIndex) withdrawn(pluginID, version string) bool {
	return slices.Contains(i.Withdrawals, CatalogWithdrawal{PluginID: pluginID, Version: version})
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
	automatic     bool
	RepositoryID  int
	PluginID      string
	Version       string
	ArchiveURL    string
	Checksum      string
	LegacyArchive bool
	// BundleURL is the resolved bundle_url, empty when the entry has none.
	BundleURL string
	// BundleURLErr is set when the entry's bundle_url doesn't resolve. The
	// signature check decides what that means, like a bundle that fails.
	BundleURLErr error
	// RepoURL is the entry's repo_url, which the advisory signature policy
	// checks a third-party release against.
	RepoURL string
}

// forAutomaticInstall marks the request for restore admission without changing
// the target shared with catalog readers or manual callers.
func (t *ResolvedCatalogInstall) forAutomaticInstall() *ResolvedCatalogInstall {
	if t == nil {
		return nil
	}
	automatic := *t
	automatic.automatic = true
	return &automatic
}

// ArchiveRequest is the installer request for a legacy archive target, with
// the same catalog identity as BinaryRequest.
func (t *ResolvedCatalogInstall) ArchiveRequest() InstallArchiveRequest {
	repositoryID := t.RepositoryID
	return InstallArchiveRequest{
		automatic:    t.automatic,
		ArchiveURL:   t.ArchiveURL,
		RepositoryID: &repositoryID,
		PluginID:     t.PluginID,
		Version:      t.Version,
	}
}

// BinaryRequest is the installer request for a non-legacy target. It carries
// the catalog's plugin ID and version so the installer refuses a binary that
// names another plugin, and what the installer needs to check its release
// signature.
func (t *ResolvedCatalogInstall) BinaryRequest() InstallBinaryRequest {
	repositoryID := t.RepositoryID
	return InstallBinaryRequest{
		automatic:     t.automatic,
		BinaryURL:     t.ArchiveURL,
		Checksum:      t.Checksum,
		BundleURL:     t.BundleURL,
		BundleURLErr:  t.BundleURLErr,
		SourceRepoURL: t.RepoURL,
		RepositoryID:  &repositoryID,
		PluginID:      t.PluginID,
		Version:       t.Version,
	}
}

type CatalogServiceOptions struct {
	HTTPClient     *http.Client
	SiloAPIVersion string
	CurrentOS      string
	CurrentArch    string
	// Signatures is the release signature policy, shared with the
	// installer. Nil refuses nothing.
	Signatures *SignatureChecker
}

type CatalogService struct {
	repositories   *RepositoryStore
	httpClient     *http.Client
	siloAPIVersion string
	currentOS      string
	currentArch    string
	signatures     *SignatureChecker
}

func NewCatalogService(repositories *RepositoryStore, opts CatalogServiceOptions) *CatalogService {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = newPackageFetchClient(defaultPackageFetchTimeouts)
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
		signatures:     opts.Signatures,
	}
}

func (s *CatalogService) Fetch(ctx context.Context) ([]CatalogEntry, error) {
	snapshot, err := s.fetchSnapshot(ctx)
	return snapshot.entries, err
}

func (s *CatalogService) fetchSnapshot(ctx context.Context) (catalogSnapshot, error) {
	repositories, err := s.repositories.List(ctx)
	if err != nil {
		return catalogSnapshot{}, err
	}

	snapshot := catalogSnapshot{withdrawals: make(map[int][]CatalogWithdrawal)}
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
			return catalogSnapshot{}, err
		}
		if isManagedOfficialRepository(repository) {
			snapshot.withdrawals[repository.ID] = index.Withdrawals
		}

		for _, pkg := range index.Plugins {
			if snapshot.withdrawn(repository.ID, manifestPluginID(pkg.Manifest), manifestVersion(pkg.Manifest)) {
				continue
			}
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
			leftVersion, rightVersion := left.Manifest.GetVersion(), right.Manifest.GetVersion()
			return cmp.Or(compareVersions(leftVersion, rightVersion), strings.Compare(leftVersion, rightVersion))
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

	snapshot.entries = entries
	return snapshot, nil
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

	if isManagedOfficialRepository(repository) && index.withdrawn(req.PluginID, req.Version) {
		return nil, withdrawnCatalogRelease(req.PluginID, req.Version)
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

var officialVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)

func validateOfficialReleaseRequest(pluginID, version string) (string, error) {
	repoURL, ok := officialPluginRepository(pluginID)
	if !ok {
		return "", packageError(nil, "The requested plugin is outside the official catalog.")
	}
	if version != "" && (len(version) > 128 || !officialVersionPattern.MatchString(version) || !semver.IsValid("v"+version)) {
		return "", packageError(nil, "The requested version is not a valid official plugin release.")
	}
	return repoURL, nil
}

func isManagedOfficialRepository(repository *Repository) bool {
	return repository != nil && repository.ManagedKey != nil &&
		*repository.ManagedKey == OfficialRepositoryManagedKey &&
		repository.SourceKind == RepositorySourceSilo && repository.URL == DefaultRepositoryURL
}

func (s *CatalogService) OfficialRepository(ctx context.Context) (*Repository, error) {
	repository, err := s.repositories.getByManagedKey(ctx, OfficialRepositoryManagedKey)
	if err != nil {
		return nil, err
	}
	if !isManagedOfficialRepository(repository) || !repository.Enabled {
		return nil, errors.New("the managed official plugin catalog is unavailable")
	}
	return repository, nil
}

func withdrawnCatalogRelease(pluginID, version string) error {
	return packageError(fmt.Errorf("official plugin %s %s has been withdrawn", pluginID, version),
		"The Silo catalog has withdrawn this plugin release.")
}

// ResolveOfficialInstall resolves the current official release for an empty
// version or an immutable release descriptor for an exact version.
func (s *CatalogService) ResolveOfficialInstall(ctx context.Context, pluginID, version string) (*ResolvedCatalogInstall, error) {
	if _, err := validateOfficialReleaseRequest(pluginID, version); err != nil {
		return nil, err
	}
	repository, err := s.OfficialRepository(ctx)
	if err != nil {
		return nil, err
	}
	return s.resolveOfficialInstall(ctx, repository, pluginID, version)
}

func (s *CatalogService) resolveOfficialInstall(ctx context.Context, repository *Repository, pluginID, version string) (*ResolvedCatalogInstall, error) {
	repoURL, err := validateOfficialReleaseRequest(pluginID, version)
	if err != nil {
		return nil, err
	}
	const maxCatalogMetadata = 4 << 20
	index, err := s.fetchRepositoryIndexLimited(ctx, repository.URL, "the Silo catalog", maxCatalogMetadata)
	if err != nil {
		return nil, err
	}
	var pkg CatalogPackage
	historical := version != ""
	if !historical {
		match := slices.IndexFunc(index.Plugins, func(current CatalogPackage) bool {
			return current.Manifest.GetPluginId() == pluginID
		})
		if match < 0 {
			return nil, packageError(fmt.Errorf("plugin %s is not listed", pluginID), "The Silo catalog doesn't list this plugin.")
		}
		pkg = index.Plugins[match]
		version = pkg.Manifest.GetVersion()
		if _, err := validateOfficialReleaseRequest(pluginID, version); err != nil {
			return nil, err
		}
	}
	if index.withdrawn(pluginID, version) {
		return nil, withdrawnCatalogRelease(pluginID, version)
	}
	if historical {
		pkg, err = s.fetchOfficialDescriptor(ctx, repository, pluginID, version, maxCatalogMetadata)
		if err != nil {
			return nil, err
		}
	}
	if err := validateOfficialPackage(pkg, pluginID, version, repoURL, historical); err != nil {
		return nil, packageError(err, "The official plugin release metadata is invalid.")
	}
	// The current catalog can predate the per-binary checksum fields.
	platform := s.currentOS + "/" + s.currentArch
	if binary, ok := pkg.Binaries[platform]; ok && binary.Checksum == "" && pkg.ChecksumsURL != "" {
		data, err := s.fetchCatalogMetadata(ctx, pkg.ChecksumsURL, "the plugin's checksums", 64<<10)
		if err != nil {
			return nil, err
		}
		binary.Checksum, err = checksumForBinary(string(data), binary.URL)
		if err != nil {
			return nil, packageError(err, "The catalog lists no valid SHA-256 checksum for this plugin.")
		}
		pkg.Binaries[platform] = binary
	}
	target, err := s.installTargetFromPackage(ctx, repository, pkg)
	if err != nil {
		return nil, err
	}
	target.PluginID, target.Version = pluginID, version
	return target, nil
}

func (s *CatalogService) fetchOfficialDescriptor(ctx context.Context, repository *Repository, pluginID, version string, limit int64) (CatalogPackage, error) {
	descriptorURL, err := resolveRepositoryURL(repository.URL, "releases/"+pluginID+"/"+version+".json")
	if err != nil {
		return CatalogPackage{}, err
	}
	data, err := s.fetchCatalogMetadata(ctx, descriptorURL, "the official plugin release", limit)
	if err != nil {
		return CatalogPackage{}, err
	}
	var pkg CatalogPackage
	if err := jsonv2.Unmarshal(data, &pkg); err != nil {
		return CatalogPackage{}, packageError(err, "Silo couldn't read the official plugin release.")
	}
	return pkg, nil
}

func validateOfficialPackage(pkg CatalogPackage, pluginID, version, repoURL string, historical bool) error {
	tag := "v" + version
	if version == "" || pkg.Manifest.GetPluginId() != pluginID || pkg.Manifest.GetVersion() != version ||
		pkg.RepoURL != repoURL || (historical || pkg.Tag != "") && pkg.Tag != tag {
		return fmt.Errorf("official release identity does not match %s %s", pluginID, version)
	}
	prefix := repoURL + "/releases/download/" + tag + "/"
	if len(pkg.Binaries) == 0 || pkg.BundleURL != prefix+"plugin.sigstore.json" || pkg.ArchiveURL != "" {
		return errors.New("official release must contain binaries and a signature bundle")
	}
	if pkg.ChecksumsURL != "" && pkg.ChecksumsURL != prefix+"checksums.txt" {
		return errors.New("official release checksum URL does not match its repository and tag")
	}
	for platform, binary := range pkg.Binaries {
		osName, arch, ok := strings.Cut(platform, "/")
		if !ok || !validOfficialPlatformPart(osName) || !validOfficialPlatformPart(arch) ||
			binary.URL != prefix+"plugin-"+osName+"-"+arch {
			return errors.New("official release binary URL does not match its repository, tag and platform")
		}
		if historical || pkg.Tag != "" || binary.Checksum != "" {
			if _, err := normalizeSHA256Checksum(binary.Checksum); err != nil {
				return fmt.Errorf("invalid official release binary checksum: %w", err)
			}
		}
	}
	return nil
}

func validOfficialPlatformPart(value string) bool {
	return value != "" && !strings.ContainsFunc(value, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// fetchRepositoryIndex reads a catalog index. subject names the catalog for
// the admin in the PackageError a failure returns.
func (s *CatalogService) fetchRepositoryIndex(ctx context.Context, repositoryURL, subject string) (*RepositoryIndex, error) {
	return s.fetchRepositoryIndexLimited(ctx, repositoryURL, subject, 0)
}

func (s *CatalogService) fetchRepositoryIndexLimited(ctx context.Context, repositoryURL, subject string, limit int64) (*RepositoryIndex, error) {
	data, err := s.fetchCatalogMetadata(ctx, repositoryURL, subject, limit)
	if err != nil {
		return nil, err
	}
	var index RepositoryIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, packageError(fmt.Errorf("decode repository index %q: %w", repositoryURL, err), "Silo couldn't read %s: it isn't a valid plugin catalog.", subject)
	}
	return &index, nil
}

func (s *CatalogService) fetchCatalogMetadata(ctx context.Context, rawURL, subject string, limit int64) ([]byte, error) {
	data, err := fetchPackageResourceLimited(ctx, s.httpClient, rawURL, subject, limit)
	if errors.Is(err, errPackageResourceTooLarge) {
		return nil, packageError(err, "Silo couldn't read %s because it exceeds the size limit.", subject)
	}
	return data, err
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
		// A bundle_url that doesn't resolve is a signature outcome decided at
		// install, not a broken entry.
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
		if err := s.signatures.checkCatalogPackage(ctx, pkg.Manifest.GetPluginId(), false, pkg.BundleURL); err != nil {
			return nil, err
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

		var bundleURL string
		var bundleURLErr error
		if strings.TrimSpace(pkg.BundleURL) != "" {
			bundleURL, bundleURLErr = resolveRepositoryURL(repository.URL, pkg.BundleURL)
		}

		return &ResolvedCatalogInstall{
			RepositoryID:  repository.ID,
			ArchiveURL:    resolvedURL,
			Checksum:      checksum,
			LegacyArchive: false,
			BundleURL:     bundleURL,
			BundleURLErr:  bundleURLErr,
			RepoURL:       pkg.RepoURL,
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
	// A legacy archive has no release signature and its manifest is read
	// from the archive, so a first-party plugin can't take this path.
	if err := s.signatures.checkCatalogPackage(ctx, pkg.Manifest.GetPluginId(), true, ""); err != nil {
		return nil, err
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
