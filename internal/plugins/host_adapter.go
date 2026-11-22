package plugins

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type hostAdapter struct {
	host *pluginhost.Host
}

func NewHostAdapter(host *pluginhost.Host) Host {
	if host == nil {
		return nil
	}
	return &hostAdapter{host: host}
}

func (h *hostAdapter) Start(ctx context.Context, req pluginhost.StartRequest) (pluginClient, error) {
	return h.host.Start(ctx, req)
}

func (h *hostAdapter) Client(installationID int) (pluginClient, error) {
	return h.host.Client(installationID)
}

func (h *hostAdapter) Stop(installationID int) error {
	return h.host.Stop(installationID)
}

func (h *hostAdapter) Shutdown(ctx context.Context) error {
	return h.host.Shutdown(ctx)
}

func (h *hostAdapter) Running(installationID int) bool {
	return h.host.Running(installationID)
}
