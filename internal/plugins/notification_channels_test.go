package plugins

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// Direct archive installs can leave several enabled installations of one
// plugin. A channel lists once, from the oldest installation that provides
// it, never from an older one without it: sends and settings checks resolve
// the channel through this list, so they reach the installation whose form
// the client was shown.
func TestNotificationChannelsResolveTheProvidingInstallation(t *testing.T) {
	manifest := testPluginManifest(t, "example.ntfy", "1.0.0")
	manifest.Capabilities = append(manifest.Capabilities, &pluginv1.CapabilityDescriptor{
		Type: "notification_channel.v1", Id: "ntfy",
		NotificationChannel: &pluginv1.NotificationChannelDescriptor{DisplayName: "ntfy", DestinationSchema: &pluginv1.ConfigSchema{
			Key: "destination", JsonSchema: `{"type":"object","properties":{"topic":{"type":"string"}}}`,
			AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{
				{Key: "topic", Label: "Topic", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT},
			}},
		}},
	})
	records, err := CapabilityRecordsFromManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var channel *Capability
	for i := range records {
		if records[i].Type == "notification_channel.v1" {
			channel = &records[i]
		}
	}
	if channel == nil {
		t.Fatal("manifest has no notification channel record")
	}
	store := newFakeServiceInstallationStore(
		&Installation{ID: 1, PluginID: "example.ntfy", Enabled: true},
		&Installation{ID: 2, PluginID: "example.ntfy", Enabled: true},
		&Installation{ID: 3, PluginID: "example.ntfy", Enabled: true},
	)
	// Installation 1 is an older build without the channel.
	store.capabilitiesByID = map[int][]*Capability{2: {channel}, 3: {channel}}
	service := &Service{installations: store}

	channels, err := service.NotificationChannels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].InstallationID != 2 || channels[0].CapabilityID != "ntfy" {
		t.Fatalf("channels = %+v, want ntfy once, from installation 2", channels)
	}
}

// A stored destination form whose JSON Schema doesn't compile could never
// check a value, so its channel isn't offered.
func TestUsableDestinationSchemaRefusesAnUncompilableSchema(t *testing.T) {
	form := &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{Key: "topic", Label: "Topic", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT}}}
	good := &pluginv1.ConfigSchema{Key: "destination", JsonSchema: `{"type":"object","properties":{"topic":{"type":"string","pattern":"^[a-z]+$"}}}`, AdminForm: form}
	bad := &pluginv1.ConfigSchema{Key: "destination", JsonSchema: `{"type":"object","properties":{"topic":{"type":"string","pattern":"("}}}`, AdminForm: form}
	if err := usableDestinationSchema(good, "ntfy"); err != nil {
		t.Fatalf("usableDestinationSchema(good) = %v", err)
	}
	if err := usableDestinationSchema(bad, "ntfy"); err == nil {
		t.Fatal("usableDestinationSchema accepted a pattern that doesn't compile")
	}
}
