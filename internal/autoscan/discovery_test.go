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

func TestWithBuiltinSourcesAppendsToInner(t *testing.T) {
	inner := fakeLister{sources: []DiscoveredSource{
		{PluginID: "sonarr", CapabilityID: "arr-a", DisplayName: "Sonarr"},
	}}
	lister := WithBuiltinSources(inner, BuiltinArrWebhookSource())

	discovered, err := lister.ListScanSources(context.Background())
	if err != nil {
		t.Fatalf("ListScanSources: %v", err)
	}
	if len(discovered) != 2 {
		t.Fatalf("expected 2 discovered, got %d: %+v", len(discovered), discovered)
	}
	if discovered[0].PluginID != "sonarr" {
		t.Fatalf("plugin entries must pass through first, got %+v", discovered[0])
	}
	if !isBuiltinArrWebhookSource(discovered[1]) {
		t.Fatalf("expected builtin appended, got %+v", discovered[1])
	}
}

func TestWithBuiltinSourcesNilInner(t *testing.T) {
	lister := WithBuiltinSources(nil, BuiltinArrWebhookSource())

	discovered, err := lister.ListScanSources(context.Background())
	if err != nil {
		t.Fatalf("ListScanSources: %v", err)
	}
	if len(discovered) != 1 || !isBuiltinArrWebhookSource(discovered[0]) {
		t.Fatalf("expected only builtin, got %+v", discovered)
	}
}

// isBuiltinArrWebhookSource compares the identifying fields of a discovered
// source. DiscoveredSource carries slices (delivery modes, connection kinds) so
// it is no longer comparable with ==, and these tests only care that the
// builtin identity came through.
func isBuiltinArrWebhookSource(got DiscoveredSource) bool {
	want := BuiltinArrWebhookSource()
	return got.PluginID == want.PluginID &&
		got.CapabilityID == want.CapabilityID &&
		got.DisplayName == want.DisplayName
}

func TestIsBuiltinArrWebhookIdentity(t *testing.T) {
	if !IsBuiltinArrWebhookIdentity(BuiltinArrWebhookPluginID, BuiltinArrWebhookCapabilityID) {
		t.Fatal("builtin identity must match")
	}
	if IsBuiltinArrWebhookIdentity("sonarr", "arr") {
		t.Fatal("plugin identity must not match builtin")
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
