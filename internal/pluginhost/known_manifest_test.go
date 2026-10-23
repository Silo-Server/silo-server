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
	      {"key": "accent", "control": "ADMIN_FORM_CONTROL_FROM_A_NEWER_SDK"},
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
	live := &pluginv1.PluginManifest{}
	proto.Merge(live, &installed)
	unknownField := protowire.AppendVarint(protowire.AppendTag(nil, 9999, protowire.VarintType), 1)
	live.ProtoReflect().SetUnknown(unknownField)
	live.GetCapabilities()[0].ProtoReflect().SetUnknown(unknownField)
	live.GetCapabilities()[0].GetConfigSchema()[0].GetAdminForm().GetFields()[0].Control = pluginv1.AdminFormControl(99)
	if proto.Equal(&installed, live) {
		t.Fatal("live manifest matches before it is read; the test no longer covers anything")
	}

	if got := KnownManifest(live); !proto.Equal(KnownManifest(&installed), got) {
		t.Fatalf("KnownManifest(live) = %v, want the installed manifest %v", got, &installed)
	}
	if live.GetCapabilities()[0].GetConfigSchema()[0].GetAdminForm().GetFields()[0].GetControl() != 99 {
		t.Fatal("KnownManifest changed the manifest it was given")
	}
}

// An installed manifest can carry an enum value the server doesn't know as a
// JSON number, which protojson keeps even with DiscardUnknown. Both copies
// still compare equal once each is read through KnownManifest.
func TestKnownManifestMatchesAnInstalledEnumNumber(t *testing.T) {
	installedJSON := `{
	  "pluginId": "test.plugin", "version": "1.0.0", "siloApiVersion": "v1",
	  "checksum": "` + strings.Repeat("a", 64) + `",
	  "supportedPlatforms": [{"os": "linux", "arch": "amd64"}],
	  "capabilities": [{"type": "metadata_provider.v1", "id": "fixture",
	    "configSchema": [{"key": "connection", "adminForm": {"fields": [{"key": "accent", "control": 99}]}}]}]
	}`
	var installed pluginv1.PluginManifest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(installedJSON), &installed); err != nil {
		t.Fatal(err)
	}
	if installed.GetCapabilities()[0].GetConfigSchema()[0].GetAdminForm().GetFields()[0].GetControl() != 99 {
		t.Fatal("protojson dropped the enum number; the test no longer covers anything")
	}
	live := &pluginv1.PluginManifest{}
	proto.Merge(live, &installed)

	if !proto.Equal(KnownManifest(&installed), KnownManifest(live)) {
		t.Fatalf("KnownManifest(installed) = %v, KnownManifest(live) = %v, want them equal", KnownManifest(&installed), KnownManifest(live))
	}
	if KnownManifest(nil) != nil {
		t.Fatal("KnownManifest(nil) is not nil")
	}
}
