package autoscan

import (
	"context"
	"testing"
)

// fakeLister returns a fixed set of discovered scan sources, or err.
type fakeLister struct {
	sources []DiscoveredSource
	err     error
}

func (f fakeLister) ListScanSources(context.Context) ([]DiscoveredSource, error) {
	return f.sources, f.err
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
	got := available[0]
	if got.PluginID != "sonarr" || got.CapabilityID != "arr-a" || got.DisplayName != "Sonarr" {
		t.Fatalf("unexpected first available: %+v", got)
	}
	// A lister that supplies no descriptor must still yield the default one, so
	// every consumer can rely on the field being populated.
	if got.Descriptor.Connection != ConnectionOptional {
		t.Fatalf("expected default connection requirement, got %q", got.Descriptor.Connection)
	}
	if !got.Descriptor.SupportsDeliveryMode(DeliveryModePoll) {
		t.Fatalf("expected default poll delivery mode, got %+v", got.Descriptor.DeliveryModes)
	}
}
