package plugins

import (
	"fmt"
	"path/filepath"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

const DefaultSiloAPIVersion = "v1"

func ValidateManifest(manifest *pluginv1.PluginManifest) error {
	if err := validateManifestShared(manifest); err != nil {
		return err
	}
	if err := validateHostShellPageContract(manifest); err != nil {
		return err
	}
	if manifest.GetChecksum() == "" {
		return fmt.Errorf("plugin manifest checksum is required")
	}
	if len(manifest.GetSupportedPlatforms()) == 0 {
		return fmt.Errorf("plugin manifest supported_platforms is required")
	}
	return nil
}

func ValidateCatalogManifest(manifest *pluginv1.PluginManifest) error {
	return validateManifestShared(manifest)
}

func LoadManifestFile(path string) (*pluginv1.PluginManifest, error) {
	manifest, err := publicmanifest.LoadFromDisk(path)
	if err != nil {
		return nil, fmt.Errorf("load manifest file %q: %w", path, err)
	}
	if err := ValidateManifest(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func InstalledManifestPath(installPath string) string {
	return filepath.Join(filepath.Dir(installPath), "manifest.json")
}

func validateRelativePath(path string, label string) error {
	if path == "" {
		return fmt.Errorf("%s path is required", label)
	}
	cleaned := filepath.Clean(path)
	if filepath.IsAbs(cleaned) || cleaned == "." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || cleaned == ".." {
		return fmt.Errorf("%s path %q must stay within the plugin package", label, path)
	}
	return nil
}

func capabilityKey(kind, id string) string {
	return kind + "\x00" + id
}

// isReservedPluginID reports whether a plugin id is reserved for the host and
// must never be installable. Only the exact builtin registration id is
// reserved: first-party plugins legitimately use the "silo." prefix
// (silo.tmdb, silo.tvdb).
func isReservedPluginID(pluginID string) bool {
	return pluginID == "silo.builtin"
}

func validateManifestShared(manifest *pluginv1.PluginManifest) error {
	if err := publicmanifest.Validate(manifest); err != nil {
		return err
	}
	if isReservedPluginID(manifest.GetPluginId()) {
		return fmt.Errorf("plugin id %q is reserved for built-in host providers", manifest.GetPluginId())
	}
	if manifest.GetSiloApiVersion() == "" {
		return fmt.Errorf("plugin manifest silo_api_version is required")
	}
	if len(manifest.GetCapabilities()) == 0 {
		return fmt.Errorf("plugin manifest capabilities are required")
	}

	seenCapabilities := make(map[string]struct{}, len(manifest.GetCapabilities()))
	for _, capability := range manifest.GetCapabilities() {
		key := capabilityKey(capability.GetType(), capability.GetId())
		if _, ok := seenCapabilities[key]; ok {
			return fmt.Errorf("duplicate plugin capability %s", key)
		}
		seenCapabilities[key] = struct{}{}
	}

	seenAssets := make(map[string]struct{}, len(manifest.GetAssets()))
	for _, asset := range manifest.GetAssets() {
		if err := validateRelativePath(asset.GetPath(), "plugin asset"); err != nil {
			return err
		}
		if _, ok := seenAssets[asset.GetPath()]; ok {
			return fmt.Errorf("duplicate plugin asset %q", asset.GetPath())
		}
		seenAssets[asset.GetPath()] = struct{}{}
	}

	for _, route := range manifest.GetHttpRoutes() {
		if route == nil {
			continue
		}
		switch route.GetAccess() {
		case "public", "authenticated", "admin":
		default:
			return fmt.Errorf("plugin route %q must declare access as public, authenticated, or admin", route.GetPath())
		}
		if !strings.HasPrefix(route.GetPath(), "/") {
			return fmt.Errorf("plugin route %q must start with /", route.GetPath())
		}
		if route.GetStaticAsset() && route.GetMethod() != "GET" {
			return fmt.Errorf("plugin static asset route %q must use GET", route.GetPath())
		}
	}

	return nil
}

func validateHostShellPageContract(manifest *pluginv1.PluginManifest) error {
	for _, route := range manifest.GetHttpRoutes() {
		if route == nil || !route.GetNavigable() {
			continue
		}
		if route.GetAccess() == "public" {
			return fmt.Errorf("plugin navigable route %q must not declare public access", route.GetPath())
		}
	}
	return nil
}
