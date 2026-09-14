package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// NetworkAccessProviderHost drives the network access provider plugin
// instances running beside this proxy. *plugins.Service implements it; nil
// (a proxy without a plugin host) answers 503 on the routes below.
type NetworkAccessProviderHost interface {
	HostNetworkAccessStatus(ctx context.Context) (netaccess.HostStatusReport, error)
	HostNetworkAccessConnect(ctx context.Context, provider string) (netaccess.Status, error)
	HostNetworkAccessDisconnect(ctx context.Context, provider string) (netaccess.Status, error)
}

// SetNetworkAccessProviderHost wires the plugin service the bearer
// network-access routes act on. Call it during construction.
func (s *Server) SetNetworkAccessProviderHost(host NetworkAccessProviderHost) {
	s.networkAccessHost = host
}

// handleNetworkAccessStatus answers the API's fan-out with the live status of
// every provider instance on this node. Unlike /health it takes the node
// bearer, so the report carries the auth URL and error text.
func (s *Server) handleNetworkAccessStatus(w http.ResponseWriter, r *http.Request) {
	if s.networkAccessHost == nil {
		http.Error(w, "network access providers are not hosted on this node", http.StatusServiceUnavailable)
		return
	}
	report, err := s.networkAccessHost.HostNetworkAccessStatus(r.Context())
	if err != nil {
		http.Error(w, "network access status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeNetworkAccessJSON(w, report)
}

// handleNetworkAccessConnect brings the named provider up on this node.
func (s *Server) handleNetworkAccessConnect(w http.ResponseWriter, r *http.Request) {
	s.applyNetworkAccessCommand(w, r, "connect", func(ctx context.Context, provider string) (netaccess.Status, error) {
		return s.networkAccessHost.HostNetworkAccessConnect(ctx, provider)
	})
}

// handleNetworkAccessDisconnect tears the named provider down on this node.
func (s *Server) handleNetworkAccessDisconnect(w http.ResponseWriter, r *http.Request) {
	s.applyNetworkAccessCommand(w, r, "disconnect", func(ctx context.Context, provider string) (netaccess.Status, error) {
		return s.networkAccessHost.HostNetworkAccessDisconnect(ctx, provider)
	})
}

func (s *Server) applyNetworkAccessCommand(w http.ResponseWriter, r *http.Request, verb string, apply func(context.Context, string) (netaccess.Status, error)) {
	if s.networkAccessHost == nil {
		http.Error(w, "network access providers are not hosted on this node", http.StatusServiceUnavailable)
		return
	}
	provider := chi.URLParam(r, "provider")
	status, err := apply(r.Context(), provider)
	if err != nil {
		if errors.Is(err, netaccess.ErrProviderNotFound) {
			http.Error(w, "network access provider not found", http.StatusNotFound)
			return
		}
		http.Error(w, "network access "+verb+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	// State transitions only; the status may carry an auth URL, which is
	// never logged.
	slog.InfoContext(r.Context(), "network access command applied", slog.String("component", "proxy"),
		slog.String("command", verb), slog.String("provider", provider), slog.String("state", status.State))
	writeNetworkAccessJSON(w, status)
}

func writeNetworkAccessJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Warn("encode network access response", slog.String("component", "proxy"), slog.Any("error", err))
	}
}
