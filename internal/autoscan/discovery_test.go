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
		{PluginID: "sonarr", CapabilityID: "arr-a", DisplayName: "Sonarr"},
		{PluginID: "radarr", CapabilityID: "arr-b", DisplayName: "Radarr"},
	}}
	svc := &Service{lister: lister}

	available, err := svc.ListAvailableScanSources(context.Background())
	if err != nil {
		t.Fatalf("ListAvailableScanSources: %v", err)
	}
	if len(available) != 2 {
		t.Fatalf("expected 2 available, got %d: %+v", len(available), available)
	}
	if available[0] != (AvailableScanSource{PluginID: "sonarr", CapabilityID: "arr-a", DisplayName: "Sonarr"}) {
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
