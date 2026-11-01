package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/tcpcongestion"
)

// startCompatServer binds synchronously so the caller can release the helper
// after all listeners are configured, then serves in the background.
func startCompatServer(ctx context.Context, srv *http.Server, label string, opts tcpcongestion.Options, errCh chan<- error) {
	if srv == nil {
		return
	}
	listener, _, err := tcpcongestion.Listen(ctx, "tcp", srv.Addr, label, opts)
	if err != nil {
		errCh <- fmt.Errorf("%s compat server error: %w", label, err)
		return
	}
	go func() {
		switch label {
		case tcpcongestion.ListenerJellyfin:
			slog.InfoContext(ctx, "Jellyfin compat server listening", "component", "app", "addr", srv.Addr)
		case tcpcongestion.ListenerABS:
			slog.InfoContext(ctx, "ABS compat server listening", "component", "app", "addr", srv.Addr)
		}
		if serveErr := srv.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- fmt.Errorf("%s compat server error: %w", label, serveErr)
		}
	}()
}
