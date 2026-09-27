package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	ownerSetCommand   = "set"
	ownerCommandUsage = "usage: silo owner " + ownerSetCommand + " [-env .env] <username>"
)

// runOwnerCommand recovers the server Owner from the server's command line
// when the Owner account is lost or locked out. `silo owner set <username>`
// makes that account the Owner, enabling it and granting the admin role if
// needed; the previous Owner stays an admin. It needs shell access to a node
// and the same DATABASE_URL the server uses, so it has no API route.
func runOwnerCommand(ctx context.Context, args []string, out io.Writer) error {
	envFile, username, err := parseOwnerCommand(args)
	if err != nil {
		return err
	}
	// Recovery needs only the database; it must work from a maintenance shell
	// that does not carry the server's SECRET_KEY.
	dbURL, err := config.LoadDatabaseURL(envFile)
	if err != nil {
		return err
	}
	pool, err := database.NewPoolForRole(ctx, config.DatabaseConfig{URL: dbURL, MaxConnections: 2}, "application")
	if err != nil {
		return fmt.Errorf("database pool: %w", err)
	}
	defer database.ClosePool(pool)

	users := auth.NewUserRepository(pool)
	user, previous, err := users.SetOwner(ctx, username)
	if errors.Is(err, auth.ErrNotFound) {
		return fmt.Errorf("no account has the username %q", username)
	}
	if err != nil {
		return err
	}
	return reportOwnerChange(ctx, out, user, previous, users)
}

// parseOwnerCommand reads `set [-env file] <username>`.
func parseOwnerCommand(args []string) (envFile, username string, err error) {
	if len(args) == 0 || args[0] != ownerSetCommand {
		return "", "", errors.New(ownerCommandUsage)
	}
	flags := flag.NewFlagSet("owner set", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	env := flags.String("env", ".env", "path to .env bootstrap file")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 1 || flags.Arg(0) == "" {
		return "", "", errors.New(ownerCommandUsage)
	}
	return *env, flags.Arg(0), nil
}

func reportOwnerChange(ctx context.Context, out io.Writer, user *models.User, previous int, users *auth.UserRepository) error {
	switch previous {
	case user.ID:
		_, err := fmt.Fprintf(out, "%s is already the server Owner.\n", user.Username)
		return err
	case 0:
		_, err := fmt.Fprintf(out, "%s is now the server Owner.\n", user.Username)
		return err
	}
	name := fmt.Sprintf("account %d", previous)
	if prior, err := users.GetByID(ctx, previous); err == nil {
		name = prior.Username
	}
	_, err := fmt.Fprintf(out, "%s is now the server Owner in place of %s, who stays an admin.\n", user.Username, name)
	return err
}
