package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"

	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/landiscovery"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
)

// advertiseOnLAN announces the bound API listener with DNS-SD until ctx ends,
// then returns once the goodbye packets are sent.
// Discovery is a convenience: any failure is logged once and the server keeps
// serving clients that already know its address.
func advertiseOnLAN(ctx context.Context, listenAddr net.Addr, identity *serveridentity.Service, settings interface {
	Get(ctx context.Context, key string) (string, error)
}) {
	port, err := landiscovery.PortFromAddr(listenAddr)
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
	serverID, err := identity.ServerID(ctx)
	if err != nil {
		slog.WarnContext(ctx, "LAN discovery off: server identity unavailable", "error", err)
		return
	}
	name, err := settings.Get(ctx, branding.KeyServerName)
	if err != nil || strings.TrimSpace(name) == "" {
		name = branding.DefaultServerName
	}
	err = landiscovery.Advertise(ctx, landiscovery.Config{Name: name, ServerID: serverID, Port: port})
	if err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "LAN discovery stopped", "error", err)
	}
}
