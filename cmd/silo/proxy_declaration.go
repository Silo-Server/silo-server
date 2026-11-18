package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/nodedeclare"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// proxyDeclarationTimeout bounds a proxy's startup declaration.
const proxyDeclarationTimeout = 30 * time.Second

// proxyNodeIdentity is a proxy's own URL and the name it was given:
// SILO_NODE_URL and SILO_NODE_NAME, or the older NODE_URL and NODE_NAME. name
// is "" when neither is set.
func proxyNodeIdentity(getenv func(string) string) (nodeURL, name string) {
	first := func(names ...string) string {
		for _, n := range names {
			if v := strings.TrimSpace(getenv(n)); v != "" {
				return v
			}
		}
		return ""
	}
	return first("SILO_NODE_URL", envNodeURL), first("SILO_NODE_NAME", envNodeName)
}

// declareProxyNode registers this proxy and writes the configuration it
// declares from its environment to its row, before the configuration watcher
// first reads it. A proxy nobody registered creates its own row. It returns
// the row, or nil when the proxy starts on its row as the administrator left
// it (a database the main server has not migrated yet, or an identity that
// matches a row it cannot register as while it declares nothing). An error
// is one the proxy must not start with: a value the store refuses, named by
// its variable, or a database that cannot take the write.
func declareProxyNode(ctx context.Context, pool *pgxpool.Pool, eventBus cache.EventBus, nodeURL, name string) (*nodepool.Node, error) {
	input, err := nodedeclare.FromEnv(nodepool.NodeTypeProxy, os.Getenv)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, proxyDeclarationTimeout)
	defer cancel()
	node, created, err := nodepool.NewAdminConfigurationStore(pool).DeclareProxy(callCtx, nodeURL, name, input)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "42703" {
		// The main server has not migrated the database to a version with
		// env_managed: upgrade it first. Until then the proxy runs as before.
		slog.WarnContext(ctx, "the database predates node declarations; upgrade the main server first. Starting without this proxy's declaration",
			"component", "nodeconfig")
		return nil, nil
	}
	if declErr, ok := errors.AsType[*nodepool.DeclarationError](err); ok {
		variable := nodedeclare.VariableFor(nodepool.NodeTypeProxy, declErr.Field)
		if variable == "" {
			variable = "SILO_NODE_URL"
		}
		return nil, &nodedeclare.Error{Variable: variable, Detail: declErr.Detail}
	}
	if errors.Is(err, nodepool.ErrNodeIdentityConflict) && len(input.SetFields()) == 0 {
		slog.WarnContext(ctx, "this proxy's URL or name matches a row it cannot register as; starting without registering",
			"component", "nodeconfig", "error", err)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("register this proxy: %w", err)
	}
	if created {
		slog.InfoContext(ctx, "registered this proxy as a new stream node", "component", "nodeconfig",
			"node_id", node.ID, "name", node.Name, "url", node.URL, "env_managed", node.EnvManaged)
	} else {
		slog.InfoContext(ctx, "applied this proxy's node declaration", "component", "nodeconfig",
			"node_id", node.ID, "name", node.Name, "env_managed", node.EnvManaged)
	}
	// The API servers also notice through the pool generation within their
	// five-second reconciliation; the event makes it immediate.
	if eventBus != nil {
		if pubErr := eventBus.Publish(callCtx, cache.ChannelAdmin, cache.Event{Type: cache.EventNodePoolChanged}); pubErr != nil {
			slog.WarnContext(ctx, "node pool change not published; API servers apply this proxy's declaration on their periodic reload",
				"component", "nodeconfig", "error", pubErr)
		}
	}
	return node, nil
}
