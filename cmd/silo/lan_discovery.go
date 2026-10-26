package main

import (
	"context"
	"errors"
	"log/slog"
	"net"

	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/landiscovery"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
)

// advertiseOnLAN announces the bound API listener with DNS-SD until ctx ends,
// then returns once the advertisement is withdrawn. Discovery is a
// convenience: the advertiser retries its own failures, and nothing here
// stops the server from serving clients that already know its address.
func advertiseOnLAN(ctx context.Context, listenAddr net.Addr, identity *serveridentity.Service, brand *branding.Service) {
	port, ipv4, ipv6, err := landiscovery.Listener(listenAddr)
	if err != nil {
		switch {
		case errors.Is(err, landiscovery.ErrLoopbackOnly):
			slog.InfoContext(ctx, "LAN discovery off: the API listener accepts loopback connections only", "addr", listenAddr.String())
		case errors.Is(err, landiscovery.ErrSingleAddress):
			slog.InfoContext(ctx, "LAN discovery off: the API listener is bound to one address; bind to all addresses to advertise", "addr", listenAddr.String())
		default:
			slog.WarnContext(ctx, "LAN discovery off", "error", err)
		}
		return
	}
	err = landiscovery.Advertise(ctx, landiscovery.Options{
		Port:     port,
		IPv4:     ipv4,
		IPv6:     ipv6,
		ServerID: identity.ServerID,
		Name:     brand.ServerName,
	})
	if err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "LAN discovery stopped", "error", err)
	}
}
