package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerregistration"
)

const workerJWTSecret = "JWT_SECRET"

// runWorkerEnrollment is a core-only maintenance command. It reads protected
// core bootstrap credentials locally and exports only a one-use enrollment
// ticket and the public core identity to the worker.
func runWorkerEnrollment(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("worker-enrollment", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	nodeID := flags.Int("node-id", 0, "enabled transcode node ID")
	envFile := flags.String("env", ".env", "core bootstrap environment file")
	output := flags.String("out", "", "new protected enrollment ticket file")
	coreURL := flags.String("core-url", "", "override the configured public core origin")
	allowHTTP := flags.Bool("allow-http", false, "allow HTTP on a trusted network")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *nodeID <= 0 || *output == "" {
		return errors.New("worker-enrollment requires --node-id and --out with no positional arguments")
	}
	if err := checkNewPrivateFile(*output); err != nil {
		return err
	}
	bootstrap, err := config.LoadBootstrap(*envFile)
	if err != nil {
		return err
	}
	cipher, err := secret.New(bootstrap.SecretKey)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pool, err := database.NewPoolForRole(ctx, config.DatabaseConfig{URL: bootstrap.DatabaseURL, MaxConnections: 2}, "worker-enrollment")
	if err != nil {
		return errors.New("core database unavailable")
	}
	defer database.ClosePool(pool)
	origin := *coreURL
	if origin == "" {
		if err = pool.QueryRow(ctx, "SELECT value FROM server_settings WHERE key=$1", "server.public_url").Scan(&origin); err != nil {
			return errors.New("core public origin unavailable; configure server.public_url or pass --core-url")
		}
	}
	origin, err = workeridentity.ValidateOrigin(origin, *allowHTTP)
	if err != nil {
		return err
	}
	ticket, err := workerregistration.New(pool, cipher).Issue(ctx, *nodeID, origin, *allowHTTP)
	if err != nil {
		return errors.New("cannot issue worker enrollment; verify the enabled node, node origin and migrations")
	}
	return workerregistration.SaveEnrollment(*output, ticket)
}

// runWorkerEnroll performs no .env loading or infrastructure bootstrap. The
// locally generated private key is persisted only after a verified core reply.
func runWorkerEnroll(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("worker-enroll", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	ticketPath := flags.String("ticket", "", "protected enrollment ticket file")
	bindingPath := flags.String("binding", "", "new protected worker binding file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *ticketPath == "" || *bindingPath == "" {
		return errors.New("worker-enroll requires --ticket and --binding with no positional arguments")
	}
	for _, name := range []string{workerDatabaseURL, "REDIS_URL", "SECRET_KEY", workerJWTSecret} {
		if os.Getenv(name) != "" {
			return fmt.Errorf("worker refuses infrastructure credential environment variable %s", name)
		}
	}
	if err := checkNewPrivateFile(*bindingPath); err != nil {
		return err
	}
	ticket, err := workerregistration.LoadEnrollment(*ticketPath)
	if err != nil {
		return err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("cannot generate worker signing key")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	binding, err := workerregistration.Enroll(ctx, ticket, key, nil)
	if err != nil {
		return err
	}
	if err = workeridentity.SaveBinding(*bindingPath, binding); err != nil {
		return errors.New("cannot persist worker binding; obtain a fresh enrollment ticket before retrying")
	}
	return nil
}

func checkNewPrivateFile(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return errors.New("protected output file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("protected output parent is not a directory")
	}
	probe, err := os.CreateTemp(parent, ".worker-output-check-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}
