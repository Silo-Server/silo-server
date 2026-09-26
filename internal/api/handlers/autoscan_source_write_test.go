package handlers

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

func TestAdminAutoscanSourceWriteStore(t *testing.T) {
	var writes []autoscan.Source
	store := &fakeAutoscanStore{
		createSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			writes = append(writes, s)
			s.ID = "created"
			return s, nil
		},
		getSourceFn: func(string) (autoscan.Source, error) {
			return autoscan.Source{ID: "existing", PluginID: "plugin", CapabilityID: "cap", DeliveryMode: autoscan.DeliveryModePoll}, nil
		},
		updateSourceFn: func(s autoscan.Source) (autoscan.Source, error) { writes = append(writes, s); return s, nil },
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{available: []autoscan.AvailableScanSource{{PluginID: "plugin", CapabilityID: "cap"}}})
	in := AdminAutoscanSourceWrite{PluginID: " plugin ", CapabilityID: " cap ", ConnectionID: new(" connection "), Enabled: true, Label: " label ", PathRewrites: []autoscan.PathRewrite{{From: " /old ", To: " /new "}}, SourceConfig: map[string]string{" key ": " value "}}
	out, err := h.CreateAdminAutoscanSource(t.Context(), in)
	if err != nil || out.ID != "created" || out.Label != "label" || out.DeliveryMode != autoscan.DeliveryModePoll || len(writes) != 1 || *writes[0].ConnectionID != "connection" || writes[0].PathRewrites[0].From != "/old" || writes[0].SourceConfig["key"] != "value" {
		t.Fatal(out, err, writes)
	}
	// Update takes identity from the stored row and treats a blank connection as unbound.
	in.PluginID, in.CapabilityID = "attacker", "different"
	in.ConnectionID = new(" ")
	out, err = h.UpdateAdminAutoscanSource(t.Context(), " existing ", in)
	if err != nil || out.PluginID != "plugin" || out.CapabilityID != "cap" || out.ConnectionID != nil || out.ID != "existing" || len(writes) != 2 {
		t.Fatal(out, err, writes)
	}
	// Orphan updates do not require a currently installed plugin.
	h.svc = nil
	if _, err = h.UpdateAdminAutoscanSource(t.Context(), "existing", in); err != nil {
		t.Fatal(err)
	}
}
func TestAdminAutoscanSourceWriteValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*AdminAutoscanSourceWrite)
	}{
		{"missing identity", func(in *AdminAutoscanSourceWrite) { in.PluginID = " " }},
		{"unknown capability", func(in *AdminAutoscanSourceWrite) { in.CapabilityID = "unknown" }},
		{"interval", func(in *AdminAutoscanSourceWrite) { in.PollIntervalSeconds = new(0) }},
		{"rewrite", func(in *AdminAutoscanSourceWrite) { in.PathRewrites = []autoscan.PathRewrite{{From: " ", To: "x"}} }},
		{"wrong mode", func(in *AdminAutoscanSourceWrite) { in.DeliveryMode = autoscan.DeliveryModeWebhook }},
		{"provider", func(in *AdminAutoscanSourceWrite) {
			in.PluginID = autoscan.BuiltinArrWebhookPluginID
			in.CapabilityID = autoscan.BuiltinArrWebhookCapabilityID
			in.SourceConfig = map[string]string{"webhook_provider": "unknown"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := NewAutoscanHandler(&fakeAutoscanStore{createSourceFn: func(s autoscan.Source) (autoscan.Source, error) { calls++; return s, nil }}, &fakeAutoscanTriggerer{available: []autoscan.AvailableScanSource{{PluginID: "plugin", CapabilityID: "cap"}}})
			in := AdminAutoscanSourceWrite{PluginID: "plugin", CapabilityID: "cap"}
			tc.change(&in)
			_, err := h.CreateAdminAutoscanSource(t.Context(), in)
			if !errors.Is(err, ErrAdminAutoscanSourceWriteInvalid) || calls != 0 {
				t.Fatal(err, calls)
			}
		})
	}
}
func TestAdminAutoscanSourceWriteWebhookAndFailure(t *testing.T) {
	store := &fakeAutoscanStore{getSourceFn: func(string) (autoscan.Source, error) {
		return autoscan.Source{PluginID: autoscan.BuiltinArrWebhookPluginID, CapabilityID: autoscan.BuiltinArrWebhookCapabilityID, DeliveryMode: autoscan.DeliveryModeWebhook}, nil
	}, updateSourceFn: func(s autoscan.Source) (autoscan.Source, error) { return s, nil }, getWebhookFn: func(string) (autoscan.WebhookEndpoint, error) {
		return autoscan.WebhookEndpoint{SourceID: "source", SecretSuffix: "tail"}, nil
	}, revealTokenFn: func(string) (string, error) { return "synthetic-existing-token", nil }}
	h := NewAutoscanHandler(store, nil)
	out, err := h.UpdateAdminAutoscanSource(t.Context(), "source", AdminAutoscanSourceWrite{SourceConfig: map[string]string{"webhook_provider": " SONARR "}})
	if err != nil || out.DeliveryMode != autoscan.DeliveryModeWebhook || out.SourceConfig["webhook_provider"] != "sonarr" || !out.WebhookConfigured || out.WebhookURL != "/api/v2/autoscan/webhooks/synthetic-existing-token" {
		t.Fatal(out, err)
	}
	failure := errors.New("private store failure")
	store.updateSourceFn = func(autoscan.Source) (autoscan.Source, error) { return autoscan.Source{}, failure }
	if _, err = h.UpdateAdminAutoscanSource(t.Context(), "source", AdminAutoscanSourceWrite{}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	store.getSourceFn = func(string) (autoscan.Source, error) { return autoscan.Source{}, autoscan.ErrNotFound }
	if _, err = h.UpdateAdminAutoscanSource(t.Context(), "missing", AdminAutoscanSourceWrite{}); !errors.Is(err, autoscan.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = (*AutoscanHandler)(nil).CreateAdminAutoscanSource(t.Context(), AdminAutoscanSourceWrite{}); !errors.Is(err, ErrAdminAutoscanSourceWriteUnavailable) {
		t.Fatal(err)
	}
}
