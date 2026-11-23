package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

const pluginRecoveryUsage = `usage: silo plugins restore-hold [-env .env]
       silo plugins acknowledge-restore [-env .env] <hold-id>`

func runPluginRecoveryCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(pluginRecoveryUsage)
	}
	command := args[0]
	flags := flag.NewFlagSet("plugins "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	envFile := flags.String("env", ".env", "path to .env bootstrap file")
	if err := flags.Parse(args[1:]); err != nil {
		return errors.New(pluginRecoveryUsage)
	}
	switch command {
	case "restore-hold":
		if flags.NArg() != 0 {
			return errors.New(pluginRecoveryUsage)
		}
	case "acknowledge-restore":
		if flags.NArg() != 1 || flags.Arg(0) == "" {
			return errors.New(pluginRecoveryUsage)
		}
	default:
		return errors.New(pluginRecoveryUsage)
	}

	dbURL, err := config.LoadDatabaseURL(*envFile)
	if err != nil {
		return err
	}
	pool, err := database.NewPoolForRole(ctx, config.DatabaseConfig{URL: dbURL, MaxConnections: 2}, "application")
	if err != nil {
		return fmt.Errorf("database pool: %w", err)
	}
	defer database.ClosePool(pool)

	if command == "acknowledge-restore" {
		if err := plugins.AcknowledgeProvisionRestoreHold(ctx, pool, flags.Arg(0)); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "Acknowledged the plugin provisioning restore hold. Restart the designated writer to apply the reviewed declaration.")
		return err
	}
	id, err := plugins.ProvisionRestoreHold(ctx, pool)
	if err != nil {
		return err
	}
	if id == "" {
		_, err = fmt.Fprintln(out, "No plugin provisioning restore hold is active.")
	} else {
		_, err = fmt.Fprintf(out, "Plugin provisioning restore hold %s is active. Review the declaration for plugin releases, external sign-in, and automatic account creation.\n", id)
	}
	return err
}
