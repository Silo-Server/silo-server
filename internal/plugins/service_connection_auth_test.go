package plugins

import (
	"errors"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestPluginConnectionCheckCapabilityUsesAdvertisedAuthProvider(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{"connection_test": true})
	if err != nil {
		t.Fatal(err)
	}
	manifest := &pluginv1.PluginManifest{Capabilities: []*pluginv1.CapabilityDescriptor{
		{
			Type:     "auth_provider.v1",
			Id:       "ldap",
			Metadata: metadata,
		},
	}}

	capability, err := pluginConnectionCheckCapabilityForManifest(manifest)
	if err != nil {
		t.Fatalf("pluginConnectionCheckCapabilityForManifest returned an error: %v", err)
	}
	if capability.kind != connectionCheckKindAuth || capability.id != "ldap" {
		t.Fatalf("capability = %#v, want auth provider ldap", capability)
	}
}

func TestPluginConnectionCheckCapabilityRejectsUnadvertisedAuthProvider(t *testing.T) {
	manifest := &pluginv1.PluginManifest{Capabilities: []*pluginv1.CapabilityDescriptor{
		{
			Type: "auth_provider.v1",
			Id:   "legacy-auth",
		},
	}}

	_, err := pluginConnectionCheckCapabilityForManifest(manifest)
	if !errors.Is(err, ErrConnectionTestUnsupported) {
		t.Fatalf("error = %v, want ErrConnectionTestUnsupported", err)
	}
}

func TestPluginConnectionCheckCapabilityPreservesMetadataProviderPriority(t *testing.T) {
	authMetadata, err := structpb.NewStruct(map[string]any{"connection_test": true})
	if err != nil {
		t.Fatal(err)
	}
	manifest := &pluginv1.PluginManifest{Capabilities: []*pluginv1.CapabilityDescriptor{
		{
			Type:     "auth_provider.v1",
			Id:       "ldap",
			Metadata: authMetadata,
		},
		{
			Type: "metadata_provider.v1",
			Id:   "metadata",
		},
	}}

	capability, err := pluginConnectionCheckCapabilityForManifest(manifest)
	if err != nil {
		t.Fatalf("pluginConnectionCheckCapabilityForManifest returned an error: %v", err)
	}
	if capability.kind != connectionCheckKindMetadata || capability.id != "metadata" {
		t.Fatalf("capability = %#v, want metadata provider", capability)
	}
}
