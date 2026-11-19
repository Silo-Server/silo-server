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
	"github.com/Silo-Server/silo-server/internal/envutil"
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

// envNodeRegister lets a proxy that matches no stream node row create its
// own. It is opt-in; see nodepool.DeclareProxy.
const envNodeRegister = "SILO_NODE_REGISTER"

// proxyRegistersItself reports SILO_NODE_REGISTER.
func proxyRegistersItself() bool { return envutil.Bool(envNodeRegister) }

// proxyDeclarationIdentity is how a proxy finds or creates its row when it
// declares. A proxy with no name variable is found by the watcher's fallback
// name, so its declaration lands on the row the watcher binds it to, except
// when it registers itself: one of several unnamed proxies must create its
// own row then, not take over the one row that happens to carry that name.
func proxyDeclarationIdentity(nodeURL, givenName, fallbackName string, register bool) nodepool.ProxyIdentity {
	identity := nodepool.ProxyIdentity{URL: nodeURL, Name: givenName, NewRowName: givenName}
	if givenName == "" && !register {
		identity.Name = fallbackName
	}
	return identity
}

// proxyDeclarationRetryInterval spaces a proxy's retries while the database
// predates node declarations: the main server is usually still migrating.
const proxyDeclarationRetryInterval = 30 * time.Second

// declareProxyNode writes the configuration this proxy declares from its
// environment to its row, before the configuration watcher first reads it. A
// proxy nobody registered creates its own row when SILO_NODE_REGISTER is set.
// It returns the row, or nil when the proxy starts as it did before
// declarations: no row and nothing declared, an identity that matches a row
// it cannot register as while it declares nothing, or a database the main
// server has not migrated yet, in which case the declaration is retried in
// the background until the migration lands. An error is one the proxy must
// not start with: a value the store refuses, named by its variable, a
// declaration with no row to hold it, or a database that cannot take the
// write.
func declareProxyNode(ctx context.Context, pool *pgxpool.Pool, eventBus cache.EventBus, identity nodepool.ProxyIdentity) (*nodepool.Node, error) {
	input, err := nodedeclare.FromEnv(nodepool.NodeTypeProxy, os.Getenv)
	if err != nil {
		return nil, err
	}
	node, err := applyProxyDeclaration(ctx, pool, eventBus, identity, input)
	if predatesDeclarations(err) {
		// Compose starts a proxy beside the main server, so the migration
		// adding env_managed is often still running; an older main server
		// never adds it. Either way the proxy runs as before until it does.
		slog.WarnContext(ctx, "the database predates node declarations; starting without this proxy's declaration and retrying until the main server has migrated it",
			"component", "nodeconfig")
		go retryProxyDeclaration(ctx, pool, eventBus, identity, input)
		return nil, nil
	}
	if errors.Is(err, nodepool.ErrProxyNotRegistered) {
		if len(input.SetFields()) == 0 {
			// As before declarations: the watcher reports the missing row.
			return nil, nil
		}
		return nil, fmt.Errorf("this proxy declares configuration but matches no node on the main server by URL (%s) or name: add it there, or set %s=true to have it add itself", identity.URL, envNodeRegister)
	}
	if errors.Is(err, nodepool.ErrNodeIdentityConflict) && len(input.SetFields()) == 0 {
		slog.WarnContext(ctx, "this proxy's URL or name matches a row it cannot register as; starting without registering",
			"component", "nodeconfig", "error", err)
		return nil, nil
	}
	return node, err
}

// retryProxyDeclaration applies the declaration every
// proxyDeclarationRetryInterval until it lands or the store refuses it: a
// database that still lacks the column, or one that briefly cannot be
// reached, is tried again. The configuration watcher finds the row by URL or
// name on its next reload.
func retryProxyDeclaration(ctx context.Context, pool *pgxpool.Pool, eventBus cache.EventBus, identity nodepool.ProxyIdentity, input nodepool.UpdateNodeInput) {
	ticker := time.NewTicker(proxyDeclarationRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		_, err := applyProxyDeclaration(ctx, pool, eventBus, identity, input)
		switch {
		case err == nil:
			return
		case predatesDeclarations(err):
		case errors.Is(err, nodepool.ErrProxyNotRegistered) && len(input.SetFields()) == 0:
			return
		case proxyDeclarationRefused(err):
			slog.ErrorContext(ctx, "this proxy's node declaration was not applied; fix it and restart the proxy",
				"component", "nodeconfig", "error", err)
			return
		default:
			slog.WarnContext(ctx, "this proxy's node declaration was not applied; retrying",
				"component", "nodeconfig", "retry_in", proxyDeclarationRetryInterval, "error", err)
		}
	}
}

// proxyDeclarationRefused reports an answer another attempt cannot change: a
// value the store refuses, or an identity the proxy cannot declare as.
func proxyDeclarationRefused(err error) bool {
	_, invalid := errors.AsType[*nodedeclare.Error](err)
	return invalid || errors.Is(err, nodepool.ErrNodeIdentityConflict) || errors.Is(err, nodepool.ErrProxyNotRegistered)
}

// predatesDeclarations reports a database without stream_nodes.env_managed.
func predatesDeclarations(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "42703"
}

// applyProxyDeclaration writes the declaration once and tells the API
// servers. A value the store refuses comes back as a *nodedeclare.Error
// naming its variable.
func applyProxyDeclaration(ctx context.Context, pool *pgxpool.Pool, eventBus cache.EventBus, identity nodepool.ProxyIdentity, input nodepool.UpdateNodeInput) (*nodepool.Node, error) {
	callCtx, cancel := context.WithTimeout(ctx, proxyDeclarationTimeout)
	defer cancel()
	node, created, err := nodepool.NewAdminConfigurationStore(pool).DeclareProxy(callCtx, identity, input, proxyRegistersItself())
	if declErr, ok := errors.AsType[*nodepool.DeclarationError](err); ok {
		variable := nodedeclare.VariableFor(nodepool.NodeTypeProxy, declErr.Field)
		if variable == "" {
			variable = "SILO_NODE_URL"
		}
		return nil, &nodedeclare.Error{Variable: variable, Detail: declErr.Detail}
	}
	if err != nil {
		if predatesDeclarations(err) || errors.Is(err, nodepool.ErrNodeIdentityConflict) || errors.Is(err, nodepool.ErrProxyNotRegistered) {
			return nil, err
		}
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
