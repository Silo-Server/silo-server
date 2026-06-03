package autoscan

import (
	"context"
	"testing"
)

// fakeLister returns a fixed set of discovered scan sources.
type fakeLister struct{ sources []DiscoveredSource }

func (f fakeLister) ListScanSources(context.Context) ([]DiscoveredSource, error) {
	return f.sources, nil
}

// spySeeder records EnsureSource calls.
type spySeeder struct{ ensured []DiscoveredSource }

func (s *spySeeder) EnsureSource(_ context.Context, installationID int, capabilityID string) error {
	s.ensured = append(s.ensured, DiscoveredSource{InstallationID: installationID, CapabilityID: capabilityID})
	return nil
}

func TestDiscoverSourcesSeedsEachCapability(t *testing.T) {
	lister := fakeLister{sources: []DiscoveredSource{
		{InstallationID: 1, CapabilityID: "arr-a"},
		{InstallationID: 2, CapabilityID: "arr-b"},
	}}
	seeder := &spySeeder{}
	svc := &Service{lister: lister, seeder: seeder}

	if err := svc.DiscoverSources(context.Background()); err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	if len(seeder.ensured) != 2 {
		t.Fatalf("expected 2 EnsureSource calls, got %d: %+v", len(seeder.ensured), seeder.ensured)
	}
	if seeder.ensured[0] != (DiscoveredSource{1, "arr-a"}) || seeder.ensured[1] != (DiscoveredSource{2, "arr-b"}) {
		t.Fatalf("unexpected seeded sources: %+v", seeder.ensured)
	}
}

func TestDiscoverSourcesNilListerNoop(t *testing.T) {
	seeder := &spySeeder{}
	svc := &Service{lister: nil, seeder: seeder}
	if err := svc.DiscoverSources(context.Background()); err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	if len(seeder.ensured) != 0 {
		t.Fatalf("nil lister must seed nothing, got %+v", seeder.ensured)
	}
}
