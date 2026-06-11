package convert_test

import (
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/convert"
)

func TestCapabilityRecordsFromManifestRoundTrips(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{
		"provider": "example",
		"priority": float64(5),
	})
	if err != nil {
		t.Fatalf("structpb.NewStruct() returned error: %v", err)
	}

	manifest := &pluginv1.PluginManifest{
		Capabilities: []*pluginv1.CapabilityDescriptor{
			{
				Type:        "metadata_provider.v1",
				Id:          "example",
				DisplayName: "Example",
				Description: "Example provider",
				Subscriptions: []string{
					"catalog.updated",
				},
				ConfigSchema: []*pluginv1.ConfigSchema{
					{
						Key:         "connection",
						Title:       "Connection",
						Description: "API key",
						JsonSchema:  `{"type":"object"}`,
						Required:    true,
					},
				},
				Metadata: metadata,
			},
		},
	}

	records, err := convert.CapabilityRecordsFromManifest(manifest)
	if err != nil {
		t.Fatalf("CapabilityRecordsFromManifest() returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}

	decoded, err := convert.DecodeCapability(records[0])
	if err != nil {
		t.Fatalf("DecodeCapability() returned error: %v", err)
	}
	if got := decoded.GetDisplayName(); got != "Example" {
		t.Fatalf("display_name = %q, want Example", got)
	}
	if got := decoded.GetConfigSchema()[0].GetKey(); got != "connection" {
		t.Fatalf("config_schema key = %q, want connection", got)
	}
	if got := decoded.GetMetadata().AsMap()["provider"]; got != "example" {
		t.Fatalf("metadata provider = %#v, want example", got)
	}
}
