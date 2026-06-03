package autoscan

import (
	"context"
	"fmt"
)

// DiscoveredSource identifies one installed scan_source.v1 capability instance.
type DiscoveredSource struct {
	InstallationID int
	CapabilityID   string
}

// ScanSourceLister enumerates every installed scan_source.v1 capability so the
// engine can seed a disabled source row per capability before an operator binds
// a connection.
type ScanSourceLister interface {
	// ListScanSources returns (installationID, capabilityID) for every installed
	// scan_source.v1 capability.
	ListScanSources(ctx context.Context) ([]DiscoveredSource, error)
}

// sourceSeeder is the persistence subset DiscoverSources needs; the repository
// implements it.
type sourceSeeder interface {
	EnsureSource(ctx context.Context, installationID int, capabilityID string) error
}

// DiscoverSources lists every installed scan_source capability and seeds a
// disabled, connection-less source row for each (idempotent: an existing row is
// left untouched). It is called at the start of each poll cycle so the source
// list stays in sync with installed plugins on the poll cadence.
func (s *Service) DiscoverSources(ctx context.Context) error {
	if s.lister == nil {
		return nil
	}
	discovered, err := s.lister.ListScanSources(ctx)
	if err != nil {
		return fmt.Errorf("list scan sources: %w", err)
	}
	for _, d := range discovered {
		if err := s.seeder.EnsureSource(ctx, d.InstallationID, d.CapabilityID); err != nil {
			return fmt.Errorf("ensure source %d/%s: %w", d.InstallationID, d.CapabilityID, err)
		}
	}
	return nil
}
