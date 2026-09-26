package apiv2

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeWatchTogetherCapability struct{ available bool }

func (f *fakeWatchTogetherCapability) WatchTogetherAvailable() bool { return f.available }

func TestWatchTogetherCapabilities(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherCapability = &fakeWatchTogetherCapability{available: true}
	wireWatchTogetherCapabilityFakes(&deps)
	h := NewHandler(deps)
	path := Prefix + "/watch-together/capabilities"
	if rec := do(t, h, http.MethodGet, path, "", nil); rec.Code != 401 {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	rec := do(t, h, http.MethodGet, path, "", bearer(memberToken))
	var out WatchTogetherCapabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// The synthetic member token carries no bounded expiry, so the effective
	// allowed answer is false here; the state and feature flags are what this
	// document promises.
	if rec.Code != 200 || out.State != StateAvailable || out.Allowed == nil || !out.StagedSelection || !out.LobbyReady || !out.SelectionModeSwitch || !out.MemberState || !out.Picker || !out.VoteHostOverride || out.MaxMemberStateIDs != watchtogether.MaxMemberStateIDs || out.SocketProtocol != watchtogether.RoomSocketProtocol || out.Revision == "" {
		t.Fatalf("capabilities: %d %+v", rec.Code, out)
	}
	if !out.ConnectionReplaced {
		t.Fatal("wired v2 socket omitted connection replacement capability")
	}
	tag := rec.Header().Get("ETag")
	if cached := do(t, h, http.MethodGet, path, "", with(bearer(memberToken), "If-None-Match", tag)); cached.Code != http.StatusNotModified {
		t.Fatalf("revalidation: %d", cached.Code)
	}
	for _, dep := range []WatchTogetherCapabilityService{nil, &fakeWatchTogetherCapability{available: false}} {
		deps.WatchTogetherCapability = dep
		rec := do(t, NewHandler(deps), http.MethodGet, path, "", bearer(memberToken))
		var out WatchTogetherCapabilities
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || out.State != StateNotConfigured || out.Allowed == nil || *out.Allowed || out.StagedSelection || out.ConnectionReplaced {
			t.Fatalf("unconfigured: %d %+v", rec.Code, out)
		}
	}
}

func wireWatchTogetherCapabilityFakes(deps *Dependencies) {
	deps.WatchTogetherStage = new(fakeRoomStage)
	deps.WatchTogetherStart = new(fakeRoomStart)
	deps.WatchTogetherStop = new(fakeRoomStop)
	deps.WatchTogetherSelectionMode = new(fakeRoomSelectionMode)
	deps.WatchTogetherMemberState = new(fakeMemberState)
	deps.WatchTogetherPicker = new(fakePicker)
	deps.WatchTogetherSuggestionPromote = new(fakePromotion)
	deps.WatchTogetherSocket = new(fakeRoomSocket)
	deps.CatalogAccess = new(fakeCatalog)
}

func watchTogetherCapabilityFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "watch_together_capabilities", operationID: "getWatchTogetherCapabilities", method: "GET", path: Prefix + "/watch-together/capabilities", headers: bearer(memberToken), status: 200, schema: "#/components/schemas/WatchTogetherCapabilities", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "An authenticated account reads which watch-together room behaviors the server supports."}}
}

func TestWatchTogetherCapabilitiesWithoutOptionalDependencies(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherCapability = &fakeWatchTogetherCapability{available: true}
	rec := do(t, NewHandler(deps), http.MethodGet, Prefix+"/watch-together/capabilities", "", bearer(memberToken))
	var out WatchTogetherCapabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || out.MemberState || out.Picker || out.LobbyReady || out.ConnectionReplaced || out.SocketProtocol != "" || out.MaxMemberStateIDs != 0 {
		t.Fatalf("advertised unwired operations: %d %+v", rec.Code, out)
	}
}
