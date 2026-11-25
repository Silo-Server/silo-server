package downloads

import (
	"context"
	"net/netip"
	"time"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

// lastClientIPInterval is how long a recorded last_client_ip stands before a
// file request from another address may replace it. A client that alternates
// addresses (IPv4 and IPv6 connections, say) rewrites the row at most this
// often, however many range requests it sends; a status report, already a
// write, records its address whenever it is accepted.
const lastClientIPInterval = time.Minute

// clientAddress is the requesting client's address as the trusted-proxy
// resolution (clientip.Middleware) read it, in the form an inet column
// stores, or "" when ctx carries none: a background task, or no usable peer.
func clientAddress(ctx context.Context) string {
	addr, err := netip.ParseAddr(clientip.FromContext(ctx))
	if err != nil || addr.IsUnspecified() {
		return ""
	}
	return addr.Unmap().WithZone("").String()
}

func addrString(addr *netip.Addr) string {
	if addr == nil || !addr.IsValid() {
		return ""
	}
	return addr.String()
}

// recordCreatedAddress stamps rows about to be inserted with the registering
// request's address as where they were created from and, for a managed row,
// where its device was last seen. An ephemeral web row has no device to see.
func recordCreatedAddress(ctx context.Context, ds []*Download) {
	ip := clientAddress(ctx)
	if ip == "" {
		return
	}
	now := time.Now()
	for _, d := range ds {
		if d.CreatedClientIP != "" {
			continue
		}
		d.CreatedClientIP = ip
		if d.IsManaged() {
			d.LastClientIP, d.LastClientIPAt = ip, &now
		}
	}
}

// staleLastClientIP reports whether a file request from ip should rewrite
// d's last_client_ip: the address changed and the recorded one has stood for
// lastClientIPInterval.
func staleLastClientIP(d *Download, ip string, now time.Time) bool {
	if ip == "" || ip == d.LastClientIP {
		return false
	}
	return d.LastClientIPAt == nil || now.Sub(*d.LastClientIPAt) >= lastClientIPInterval
}
