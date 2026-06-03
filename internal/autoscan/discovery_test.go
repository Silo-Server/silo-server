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

	present, err := svc.DiscoverSources(context.Background())
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	if len(seeder.ensured) != 2 {
		t.Fatalf("expected 2 EnsureSource calls, got %d: %+v", len(seeder.ensured), seeder.ensured)
	}
	if seeder.ensured[0] != (DiscoveredSource{1, "arr-a"}) || seeder.ensured[1] != (DiscoveredSource{2, "arr-b"}) {
		t.Fatalf("unexpected seeded sources: %+v", seeder.ensured)
	}
	if len(present) != 2 {
		t.Fatalf("expected discovered set of 2, got %d: %+v", len(present), present)
	}
	if _, ok := present[discoveredKey{1, "arr-a"}]; !ok {
		t.Fatalf("discovered set missing 1/arr-a: %+v", present)
	}
}

func TestDiscoverSourcesNilListerNoop(t *testing.T) {
	seeder := &spySeeder{}
	svc := &Service{lister: nil, seeder: seeder}
	present, err := svc.DiscoverSources(context.Background())
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	if len(seeder.ensured) != 0 {
		t.Fatalf("nil lister must seed nothing, got %+v", seeder.ensured)
	}
	if present != nil {
		t.Fatalf("nil lister must return a nil discovered set, got %+v", present)
	}
}
