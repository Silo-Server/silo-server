package autoscan

import (
	"context"
	"fmt"
)

// DiscoveredSource identifies one installed scan_source.v1 capability instance,
// enriched with the metadata the Add-source picker needs (plugin id + a
// human-friendly display name).
type DiscoveredSource struct {
	InstallationID int
	CapabilityID   string
	// PluginID is the installation's plugin id (e.g. "sonarr"); empty when the
	// lister cannot supply it.
	PluginID string
	// DisplayName is a human-friendly label for the capability (from the
	// capability's manifest display_name, falling back to plugin/capability ids).
	DisplayName string
}

// ScanSourceLister enumerates every installed scan_source.v1 capability so the
// engine can (a) offer them in the Add-source picker and (b) detect orphaned
// source rows whose plugin has been uninstalled.
type ScanSourceLister interface {
	// ListScanSources returns one entry per installed scan_source.v1 capability,
	// enriched with plugin id + display name.
	ListScanSources(ctx context.Context) ([]DiscoveredSource, error)
}

// AvailableScanSource is one installed scan_source capability an operator can
// create a source against (the Add-source picker list).
type AvailableScanSource struct {
	InstallationID int    `json:"installation_id"`
	CapabilityID   string `json:"capability_id"`
	PluginID       string `json:"plugin_id"`
	DisplayName    string `json:"display_name"`
}

// installedKey identifies an installed scan_source capability for set
// membership tests (orphan detection in PollOnce).
type installedKey struct {
	InstallationID int
	CapabilityID   string
}

// ListAvailableScanSources enumerates every installed scan_source capability so
// an operator can pick one when creating a source. When no lister is configured
// it returns an empty list. The handler also uses this to validate that a
// create request targets a currently-installed capability.
func (s *Service) ListAvailableScanSources(ctx context.Context) ([]AvailableScanSource, error) {
	if s.lister == nil {
		return []AvailableScanSource{}, nil
	}
	discovered, err := s.lister.ListScanSources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list scan sources: %w", err)
	}
	out := make([]AvailableScanSource, 0, len(discovered))
	for _, d := range discovered {
		out = append(out, AvailableScanSource{
			InstallationID: d.InstallationID,
			CapabilityID:   d.CapabilityID,
			PluginID:       d.PluginID,
			DisplayName:    d.DisplayName,
		})
	}
	return out, nil
}

// installedScanSources returns the set of currently-installed scan_source
// capabilities so PollOnce can skip orphaned source rows (their plugin is
// gone). A nil set means the set is unavailable (no lister) — callers must treat
// that as "discovery unavailable" and NOT prune.
func (s *Service) installedScanSources(ctx context.Context) (map[installedKey]struct{}, error) {
	if s.lister == nil {
		return nil, nil
	}
	discovered, err := s.lister.ListScanSources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list scan sources: %w", err)
	}
	present := make(map[installedKey]struct{}, len(discovered))
	for _, d := range discovered {
		present[installedKey{InstallationID: d.InstallationID, CapabilityID: d.CapabilityID}] = struct{}{}
	}
	return present, nil
}
