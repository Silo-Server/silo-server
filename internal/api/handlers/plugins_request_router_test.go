package handlers

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// The admin form must offer what routing accepts, so a request router's view
// takes its descriptor from the stored capability that routing reads, not
// from the installed manifest. A host on an older SDK stored the capability
// without flags the manifest declares.
func TestApplyStoredRequestRoutersUsesTheStoredDescriptor(t *testing.T) {
	t.Parallel()

	installed := capabilitiesToJSON([]*pluginv1.CapabilityDescriptor{
		{Type: capability.RequestRouter, Id: "local", RequestRouter: &pluginv1.RequestRouterDescriptor{ApiKeyOptional: true, BaseUrlOptional: true}},
		{Type: capability.RequestRouter, Id: "gone", RequestRouter: &pluginv1.RequestRouterDescriptor{ApiKeyOptional: true}},
		{Type: capability.ScheduledTask, Id: "task"},
	})
	records, err := plugins.CapabilityRecordsFromManifest(&pluginv1.PluginManifest{Capabilities: []*pluginv1.CapabilityDescriptor{
		{Type: capability.RequestRouter, Id: "local", RequestRouter: &pluginv1.RequestRouterDescriptor{SupportsSeasons: true}},
		{Type: capability.ScheduledTask, Id: "task"},
	}})
	if err != nil {
		t.Fatalf("CapabilityRecordsFromManifest: %v", err)
	}
	stored := make([]*plugins.Capability, 0, len(records))
	for i := range records {
		stored = append(stored, &records[i])
	}

	if err := applyStoredRequestRouters(installed, stored); err != nil {
		t.Fatalf("applyStoredRequestRouters: %v", err)
	}
	local := installed[0].RequestRouter
	if !local.GetSupportsSeasons() || local.GetApiKeyOptional() || local.GetBaseUrlOptional() {
		t.Fatalf("local descriptor = %v, want the stored one without the optional flags", local)
	}
	if installed[1].RequestRouter != nil {
		t.Fatalf("gone descriptor = %v, want none when nothing is stored", installed[1].RequestRouter)
	}
}
