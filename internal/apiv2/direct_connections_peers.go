package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/directconn"
)

// peerHostRole is the role of the API host in a peer row.
const peerHostRole = "api"

// DirectConnectionPeer is one device with a direct path to the API host now.
type DirectConnectionPeer struct {
	Host          NetworkAccessHostRef `json:"host"`
	UserID        ID                   `json:"user_id"`
	Username      string               `json:"username" doc:"Empty when the account's name could not be read"`
	DeviceID      string               `json:"device_id"`
	DeviceName    string               `json:"device_name,omitempty" doc:"Absent when the device has no registered name"`
	Class         string               `json:"class" enum:"direct" doc:"The path class; only direct exists today. A client treats an unknown value as direct"`
	AddressFamily string               `json:"address_family" enum:"ipv4,ipv6"`
	PeerEndpoint  string               `json:"peer_endpoint" doc:"The device's observed UDP address as host:port; not validated as a URI"`
	LocalPort     int                  `json:"local_port" doc:"The API host's UDP port"`
	ConnectedAt   Instant              `json:"connected_at" doc:"When the device's path last became direct"`
}

// DirectConnectionPeerCollection is the whole list: it is bounded by the host's
// peer cache, so it has no pagination.
type DirectConnectionPeerCollection struct {
	Items []DirectConnectionPeer `json:"items"`
}

type DirectConnectionPeerCollectionOutput struct {
	Body DirectConnectionPeerCollection
}

func registerDirectConnectionPeers(reg *Registry) {
	op := humaOp(http.MethodGet, Prefix+"/admin/direct-connections/peers", "listAdminDirectConnectionPeers", "direct-connections", "List the devices that have a direct path to the API host now, with their observed addresses and ports. Needs the acting administrator; no other caller sees an endpoint.")
	Register(reg, Operation{Operation: op, Class: ClassActingAdmin, ServiceBacked: true}, reg.listAdminDirectConnectionPeers)
}

// peerNameReader reads the names a peer row shows. It reads each account's
// device list once, however many of its devices are peers.
type peerNameReader struct {
	reg    *Registry
	byUser map[int][]handlers.AdminUserDeviceView
}

// read returns the account's username and the device's name; each is empty when
// the account's device list cannot be read or does not hold the device.
func (r peerNameReader) read(ctx context.Context, userID int, deviceID string) (username, deviceName string) {
	rows, ok := r.byUser[userID]
	if !ok {
		if r.reg.deps.AdminAccountDevices != nil {
			rows, _ = r.reg.deps.AdminAccountDevices.ReadAdminUserDevices(ctx, userID)
		}
		r.byUser[userID] = rows
	}
	for _, row := range rows {
		username = row.Username
		if row.DeviceID == deviceID {
			deviceName = row.DeviceName
		}
	}
	return username, deviceName
}

func (reg *Registry) listAdminDirectConnectionPeers(ctx context.Context, _ *struct{}) (*DirectConnectionPeerCollectionOutput, error) {
	if reg.deps.DirectConnections == nil {
		return nil, unavailable("direct connections")
	}
	host := NetworkAccessHostRef{ID: directconn.CapabilityHostID, Role: peerHostRole, Name: "API"}
	peers := reg.deps.DirectConnections.Peers(ctx)
	items := make([]DirectConnectionPeer, 0, len(peers))
	names := peerNameReader{reg: reg, byUser: map[int][]handlers.AdminUserDeviceView{}}
	for _, peer := range peers {
		family := "ipv6"
		if peer.Endpoint.Addr().Unmap().Is4() {
			family = "ipv4"
		}
		item := DirectConnectionPeer{Host: host, UserID: IDFromInt(int64(peer.UserID)), DeviceID: peer.DeviceID, Class: "direct", AddressFamily: family,
			PeerEndpoint: peer.Endpoint.String(), LocalPort: int(peer.LocalPort), ConnectedAt: NewInstant(peer.Since)}
		item.Username, item.DeviceName = names.read(ctx, peer.UserID, peer.DeviceID)
		items = append(items, item)
	}
	return &DirectConnectionPeerCollectionOutput{Body: DirectConnectionPeerCollection{Items: items}}, nil
}
