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
	if discovered[1] != BuiltinArrWebhookSource() {
		t.Fatalf("expected builtin appended, got %+v", discovered[1])
	}
}

func TestWithBuiltinSourcesNilInner(t *testing.T) {
	lister := WithBuiltinSources(nil, BuiltinArrWebhookSource())

	discovered, err := lister.ListScanSources(context.Background())
	if err != nil {
		t.Fatalf("ListScanSources: %v", err)
	}
	if len(discovered) != 1 || discovered[0] != BuiltinArrWebhookSource() {
		t.Fatalf("expected only builtin, got %+v", discovered)
	}
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
