package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/directconn"
)

// manyPeers lists three direct devices on two accounts.
type manyPeers struct{ fakeDirectConnections }

func (manyPeers) Peers(context.Context) []directconn.Peer {
	endpoint := netip.MustParseAddrPort("203.0.113.9:41641")
	return []directconn.Peer{
		{UserID: 7, DeviceID: "device-tv", Endpoint: endpoint, LocalPort: 8094, Since: fixedTime()},
		{UserID: 7, DeviceID: "device-settings-only", Endpoint: endpoint, LocalPort: 8094, Since: fixedTime()},
		{UserID: 8, DeviceID: "device-tv", Endpoint: endpoint, LocalPort: 8094, Since: fixedTime()},
	}
}

func TestAdminDirectConnectionsCapabilities(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.DirectConnections = fakeDirectConnections{}
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/direct-connections/capabilities"
	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	rec := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var got struct {
		Revision string `json:"revision"`
		State    string `json:"state"`
		Allowed  bool   `json:"allowed"`
		Status   bool   `json:"status"`
		Refresh  bool   `json:"refresh"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Revision == "" || got.State != StateAvailable || !got.Allowed || !got.Status || !got.Refresh {
		t.Fatalf("capabilities = %+v", got)
	}
	deps.DirectConnections = nil
	rec = do(t, newTestHandler(t, deps), http.MethodGet, path, "", actingRequestAdmin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != StateNotConfigured || got.Allowed || !got.Status || !got.Refresh {
		t.Fatalf("unconfigured capabilities = %+v", got)
	}
}

func TestAdminDirectConnectionsStatus(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, Prefix+"/admin/direct-connections/status", "", actingRequestAdmin)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

type failingDirectConnections struct{}

func (failingDirectConnections) Status(context.Context, bool) (directconn.Status, error) {
	return directconn.Status{}, errors.New("settings read: connection refused")
}

func (failingDirectConnections) Peers(context.Context) []directconn.Peer { return nil }

// A failed settings read is a temporary dependency failure, not an internal error.
func TestAdminDirectConnectionsStatusSettingsFailure(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.DirectConnections = failingDirectConnections{}
	rec := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/admin/direct-connections/status", "", actingRequestAdmin)
	requireProblem(t, rec, TypeDependencyUnavailable)
	if rec.Header().Get("Retry-After") == "" {
		t.Fatalf("503 without Retry-After: %v", rec.Header())
	}
}

// TestAdminDirectConnectionPeers: only the acting administrator reads the peers,
// since they carry observed addresses and ports.
func TestAdminDirectConnectionPeers(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.DirectConnections = fakeDirectConnections{}
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/direct-connections/peers"
	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	rec := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var got DirectConnectionPeerCollection
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("peers = %+v, want one", got.Items)
	}
	peer := got.Items[0]
	if peer.Host.ID != "api" || peer.UserID != "7" || peer.DeviceID != "living-room-tv" || peer.Class != "direct" ||
		peer.AddressFamily != "ipv4" || peer.PeerEndpoint != "127.0.0.1:41641" || peer.LocalPort != 8094 {
		t.Fatalf("peer = %+v", peer)
	}
	deps.DirectConnections = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}

// TestAdminDirectConnectionPeerNames: the read lists each account's devices once,
// however many of its devices are peers, and names each peer from that list.
func TestAdminDirectConnectionPeerNames(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.DirectConnections = manyPeers{}
	accountDevices := &fakeAdminAccountDevices{rows: fixtureAdminUserDevices()}
	for i := range accountDevices.rows {
		accountDevices.rows[i].Username = "alice"
	}
	deps.AdminAccountDevices = accountDevices
	rec := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/admin/direct-connections/peers", "", actingRequestAdmin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var got DirectConnectionPeerCollection
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if accountDevices.calls != 2 {
		t.Fatalf("account device reads = %d for three peers on two accounts, want 2", accountDevices.calls)
	}
	names := make([][2]string, len(got.Items))
	for i, item := range got.Items {
		names[i] = [2]string{item.Username, item.DeviceName}
	}
	want := [][2]string{{"alice", "Apple TV"}, {"alice", "Living Room"}, {"alice", "Apple TV"}}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Fatalf("peer names = %v, want %v", names, want)
	}
}

func TestAdminDirectConnectionPeerAccountNameWithoutInventory(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.DirectConnections = fakeDirectConnections{}
	deps.AdminAccountDevices = &fakeAdminAccountDevices{rows: []handlers.AdminUserDeviceView{}}
	deps.AdminAccounts = fixtureAdminAccounts()
	rec := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/admin/direct-connections/peers", "", actingRequestAdmin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var got DirectConnectionPeerCollection
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("peers = %d, want one", len(got.Items))
	}
	if got.Items[0].Username != "sample" {
		t.Fatalf("peer username = %q, want sample without device inventory", got.Items[0].Username)
	}
}
