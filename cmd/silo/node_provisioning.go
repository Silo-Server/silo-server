package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// provisionTranscodeNodes applies SILO_TRANSCODE_NODES (or its _FILE form) on
// an API server before it loads its node pools and credentials. A
// declaration that cannot be read or applied stops startup. When it changed
// anything, node_pool_changed moves every other API server and proxy to it;
// a missed event is caught by their periodic reloads.
func provisionTranscodeNodes(ctx context.Context, pool *pgxpool.Pool, cipher *secret.Cipher, bus cache.EventBus) {
	declaration, err := nodepool.ReadNodeProvisioning(os.Getenv)
	if err != nil {
		log.Fatalf("transcode node declaration: %v", err)
	}
	if declaration == nil {
		return
	}
	result, err := nodepool.ProvisionTranscodeNodes(ctx, pool, cipher, declaration.Nodes)
	if err != nil {
		log.Fatalf("%s: %v", declaration.Source, err)
	}
	slog.InfoContext(ctx, "applied the transcode node declaration", "source", declaration.Source,
		"nodes", len(declaration.Nodes), "created", result.Created, "rotated", result.Rotated,
		"adopted", result.Adopted, "released", result.Released)
	if !result.Changed() {
		return
	}
	publishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := bus.Publish(publishCtx, cache.ChannelAdmin, cache.Event{Type: cache.EventNodePoolChanged}); err != nil {
		slog.WarnContext(ctx, "node pool change not published; other servers catch up on their periodic reload", "error", err)
	}
}

const nodeKeyUsage = "usage: silo node-key generate --id <n>"

// runNodeKeyCommand serves "silo node-key generate --id <n>": it prints a new
// key for transcode node n, for the node's SILO_NODE_KEY and the API servers'
// SILO_TRANSCODE_NODES declaration.
func runNodeKeyCommand(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "generate" {
		return errors.New(nodeKeyUsage)
	}
	flags := flag.NewFlagSet("node-key generate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.Int("id", 0, "node id")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return errors.New(nodeKeyUsage)
	}
	if *id < 1 || *id > nodepool.MaxProvisionedNodeID {
		return fmt.Errorf("--id must be between 1 and %d", nodepool.MaxProvisionedNodeID)
	}
	key, err := nodepool.GenerateNodeKey(*id)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, key)
	return err
}
