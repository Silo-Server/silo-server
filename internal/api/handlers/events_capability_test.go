package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEventsCapabilityAdvertisesOnlySubscribableChannels is the point of
// publishing the list at all: a client that requests everything the endpoint
// names must not be refused any of it. The plugins channel is the case — it is
// in evt.AllChannels but granted to no role, admin included, so advertising it
// would send a client to a request that can only fail.
func TestEventsCapabilityAdvertisesOnlySubscribableChannels(t *testing.T) {
	handler := &EventsHandler{}
	rec := httptest.NewRecorder()
	handler.HandleCapability(rec, httptest.NewRequest(http.MethodGet, "/events/capability", nil))

	var got eventsCapabilityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding capability: %v (%s)", err, rec.Body.String())
	}

	// An admin, on a profile-bound connection, is the most permissive caller
	// there is. Every advertised channel must resolve for them.
	_, accepted, rejected := resolveChannelSelection(
		got.Channels, allowedChannelsForRole("admin"), "profile-1")

	if len(rejected) != 0 {
		t.Errorf("capability advertises channels an admin cannot subscribe to: %v", rejected)
	}
	if len(accepted) != len(got.Channels) {
		t.Errorf("accepted %d of %d advertised channels", len(accepted), len(got.Channels))
	}
}
