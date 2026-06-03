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

func TestListAvailableScanSourcesEnumeratesInstalled(t *testing.T) {
	lister := fakeLister{sources: []DiscoveredSource{
		{InstallationID: 1, CapabilityID: "arr-a", PluginID: "sonarr", DisplayName: "Sonarr"},
		{InstallationID: 2, CapabilityID: "arr-b", PluginID: "radarr", DisplayName: "Radarr"},
	}}
	svc := &Service{lister: lister}

	available, err := svc.ListAvailableScanSources(context.Background())
	if err != nil {
		t.Fatalf("ListAvailableScanSources: %v", err)
	}
	if len(available) != 2 {
		t.Fatalf("expected 2 available, got %d: %+v", len(available), available)
	}
	if available[0] != (AvailableScanSource{InstallationID: 1, CapabilityID: "arr-a", PluginID: "sonarr", DisplayName: "Sonarr"}) {
		t.Fatalf("unexpected first available: %+v", available[0])
	}
}

func TestListAvailableScanSourcesNilListerEmpty(t *testing.T) {
	svc := &Service{lister: nil}
	available, err := svc.ListAvailableScanSources(context.Background())
	if err != nil {
		t.Fatalf("ListAvailableScanSources: %v", err)
	}
	if len(available) != 0 {
		t.Fatalf("nil lister must return empty, got %+v", available)
	}
}

func TestInstalledScanSourcesSetMembership(t *testing.T) {
	lister := fakeLister{sources: []DiscoveredSource{
		{InstallationID: 1, CapabilityID: "arr-a"},
	}}
	svc := &Service{lister: lister}

	present, err := svc.installedScanSources(context.Background())
	if err != nil {
		t.Fatalf("installedScanSources: %v", err)
	}
	if _, ok := present[installedKey{1, "arr-a"}]; !ok {
		t.Fatalf("expected 1/arr-a present: %+v", present)
	}
	if _, ok := present[installedKey{2, "arr-b"}]; ok {
		t.Fatalf("did not expect 2/arr-b present: %+v", present)
	}
}

func TestInstalledScanSourcesNilListerNilSet(t *testing.T) {
	svc := &Service{lister: nil}
	present, err := svc.installedScanSources(context.Background())
	if err != nil {
		t.Fatalf("installedScanSources: %v", err)
	}
	if present != nil {
		t.Fatalf("nil lister must return a nil set, got %+v", present)
	}
}
