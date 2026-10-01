package apiv2

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

func TestEventsCapabilities(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.EventsCapability = &handlers.EventsHandler{}
	h := NewHandler(deps)
	path := Prefix + "/events/capabilities"
	if rec := do(t, h, http.MethodGet, path, "", nil); rec.Code != 401 {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	rec := do(t, h, http.MethodGet, path, "", bearer(memberToken))
	var out EventsCapabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	expected := deps.EventsCapability.EventsCapability()
	if rec.Code != 200 || out.SubscribeGracePeriodSeconds != expected.SubscribeGracePeriodSeconds || out.MaxRequestedChannels != expected.MaxRequestedChannels || !out.SubscribeFrame || !out.DeclaredChannels || len(out.Channels) != len(expected.Channels) {
		t.Fatalf("capabilities: %d %+v", rec.Code, out)
	}
	for i, c := range expected.Channels {
		if out.Channels[i] != string(c) || out.Channels[i] == "plugins" {
			t.Fatal("channel projection changed")
		}
	}
	deps.EventsCapability = nil
	if rec := do(t, NewHandler(deps), http.MethodGet, path, "", bearer(memberToken)); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"not_configured"`) {
		t.Fatalf("unconfigured: %d", rec.Code)
	}
}
func eventsCapabilityFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "events_capabilities", operationID: "getEventsCapabilities", method: "GET", path: Prefix + "/events/capabilities", headers: bearer(memberToken), status: 200, schema: "#/components/schemas/EventsCapabilities", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "An authenticated account reads shared realtime subscription limits without requiring an active profile."}}
}
