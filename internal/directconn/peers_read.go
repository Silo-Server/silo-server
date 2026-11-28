package directconn

import (
	"cmp"
	"context"
	"net/netip"
	"slices"
	"time"
)

// Peer is one enrolled device that has a direct path to the API host now.
type Peer struct {
	UserID   int
	DeviceID string
	// Endpoint is the device's observed UDP address.
	Endpoint netip.AddrPort
	// LocalPort is the API engine's UDP port.
	LocalPort uint16
	// Since is when the device's path last became direct.
	Since time.Time
}

// Peers lists the devices whose path to the running engine is direct, ordered by
// user and device. The collection is bounded by the peer cache. It returns nothing
// while the engine is off or no coordination service is attached.
func (s *Service) Peers(context.Context) []Peer {
	l := s.lifecycle
	l.mu.Lock()
	e, c := l.current, l.coord
	l.mu.Unlock()
	if e == nil || c == nil {
		return nil
	}
	var peers []Peer
	for _, entry := range c.directory.Devices() {
		userID, ok := accountNumber(entry.Ref)
		if !ok {
			continue
		}
		record, direct := s.devicePath(entry.NodeKey)
		if !direct || !record.endpoint.IsValid() {
			continue
		}
		peers = append(peers, Peer{UserID: userID, DeviceID: string(entry.Ref.Device), Endpoint: record.endpoint, LocalPort: e.LocalPort(), Since: record.since})
	}
	slices.SortFunc(peers, func(a, b Peer) int {
		return cmp.Or(cmp.Compare(a.UserID, b.UserID), cmp.Compare(a.DeviceID, b.DeviceID))
	})
	return peers
}
