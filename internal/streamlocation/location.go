// Package streamlocation classifies a playback request using the server's
// trusted client-IP and network-access middleware results.
package streamlocation

import (
	"context"
	"net"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/netaccess"
)

type Location string

const (
	Local  Location = "local"
	Remote Location = "remote"
)

// FromMetadata uses the same classification for persisted playback observations
// as for the start request. A provider path or unknown address is remote.
func FromMetadata(clientIP, provider string) Location {
	if provider != "" {
		return Remote
	}
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return Remote
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return Local
	}
	return Remote
}

// IsRemote fails closed to remote when the resolved client address is absent
// or invalid. A validated network-access provider is remote even when its
// proxy connects from a private address.
func IsRemote(ctx context.Context) bool {
	return FromContext(ctx) == Remote
}

// FromContext classifies the trusted network metadata on a playback request.
func FromContext(ctx context.Context) Location {
	return FromMetadata(clientip.FromContext(ctx), netaccess.PathFromContext(ctx).Provider)
}

// BitrateCap selects the administrator ceiling for this request's network
// location. The caller persists the selected value with the playback session.
func BitrateCap(ctx context.Context, localKbps, remoteKbps int) int {
	if IsRemote(ctx) {
		return remoteKbps
	}
	return localKbps
}
