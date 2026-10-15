package pluginhost

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// A plugin built on a newer SDK reports, over gRPC, fields and enum values
// this server doesn't know. Its installed manifest was read from JSON with
// them dropped, so the live copy must be read the same way to match it.
func TestKnownManifestMatchesHowInstallReadsIt(t *testing.T) {
	newer := `{
	  "pluginId": "test.plugin", "version": "1.0.0", "siloApiVersion": "v1",
	  "checksum": "` + strings.Repeat("a", 64) + `",
	  "supportedPlatforms": [{"os": "linux", "arch": "amd64"}],
	  "fieldFromANewerSdk": true,
	  "capabilities": [{
	    "type": "metadata_provider.v1", "id": "fixture",
	    "descriptorFromANewerSdk": {"flag": true},
	    "configSchema": [{"key": "connection", "adminForm": {"fields": [
	      {"key": "colour", "control": "ADMIN_FORM_CONTROL_FROM_A_NEWER_SDK"},
	      {"key": "name", "control": "ADMIN_FORM_CONTROL_TEXT"}
	    ]}}]
	  }]
	}`
	var installed pluginv1.PluginManifest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(newer), &installed); err != nil {
		t.Fatal(err)
	}

	// The plugin's own copy keeps the newer fields as unknown bytes and the
	// newer control as a bare number.
	live := proto.Clone(&installed).(*pluginv1.PluginManifest)
	unknownField := protowire.AppendVarint(protowire.AppendTag(nil, 9999, protowire.VarintType), 1)
	live.ProtoReflect().SetUnknown(unknownField)
	live.GetCapabilities()[0].ProtoReflect().SetUnknown(unknownField)
	live.GetCapabilities()[0].GetConfigSchema()[0].GetAdminForm().GetFields()[0].Control = pluginv1.AdminFormControl(99)
	if proto.Equal(&installed, live) {
		t.Fatal("live manifest matches before it is read; the test no longer covers anything")
	}

	if got := knownManifest(live); !proto.Equal(&installed, got) {
		t.Fatalf("knownManifest(live) = %v, want the installed manifest %v", got, &installed)
	}
	if live.GetCapabilities()[0].GetConfigSchema()[0].GetAdminForm().GetFields()[0].GetControl() != 99 {
		t.Fatal("knownManifest changed the manifest it was given")
	}
}
