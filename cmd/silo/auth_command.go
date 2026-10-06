package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
)

const (
	authLocalLoginCommand = "local-login"
	authEnableAction      = "enable"
	authCommandUsage      = "usage: silo auth " + authLocalLoginCommand + " " + authEnableAction + " [-user <name or email> [-temporary-password]] [-env .env]"
)

// authCommand is one parsed `silo auth` invocation.
type authCommand struct {
	envFile string
	// user names one account to recover; empty recovers the server switch.
	user string
	// temporaryPassword also gives that account a temporary password.
	temporaryPassword bool
}

// runAuthCommand recovers local password sign-in from the server's command
// line, for a server whose external sign-in provider is down or removed.
// Like `silo owner set` it needs shell access to a node and the DATABASE_URL
// the server uses, so it has no API route. Every node reads the policy from
// the database at each sign-in, so it applies at once.
//
//   - `silo auth local-login enable` turns the auth.local_password_login
//     setting back on.
//   - `silo auth local-login enable -user <name>` turns one account's local
//     password sign-in back on (linking an account to a provider turns it
//     off, the Owner's too); with -temporary-password the account also gets
//     a random temporary password, printed once, which it must replace at
//     its next sign-in, and its sign-ins are revoked.
func runAuthCommand(ctx context.Context, args []string, out io.Writer) error {
	cmd, err := parseAuthCommand(args)
	if err != nil {
		return err
	}
	// Recovery needs only the database; it must work from a maintenance shell
	// that does not carry the server's SECRET_KEY.
	dbURL, err := config.LoadDatabaseURL(cmd.envFile)
	if err != nil {
		return err
	}
	pool, err := database.NewPoolForRole(ctx, config.DatabaseConfig{URL: dbURL, MaxConnections: 2}, "application")
	if err != nil {
		return fmt.Errorf("database pool: %w", err)
	}
	defer database.ClosePool(pool)

	users := auth.NewUserRepository(pool)
	allowed, err := users.LocalPasswordLoginAllowed(ctx)
	if err != nil {
		return err
	}
	if cmd.user != "" {
		return enableAccountLocalLogin(ctx, users, cmd, allowed, out)
	}
	if allowed {
		_, err := fmt.Fprintln(out, "Local password sign-in is already on.")
		return err
	}
	if err := users.EnableLocalPasswordLogin(ctx); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Local password sign-in is on. Accounts with a local password can sign in again.")
	return err
}

func enableAccountLocalLogin(ctx context.Context, users *auth.UserRepository, cmd authCommand, serverAllows bool, out io.Writer) error {
	password := ""
	if cmd.temporaryPassword {
		var err error
		if password, err = randomTemporaryPassword(); err != nil {
			return err
		}
	}
	user, err := users.EnableAccountLocalLogin(ctx, cmd.user, password)
	if auth.IsNotFound(err) {
		return fmt.Errorf("no account named %q", cmd.user)
	}
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Local password sign-in is on for %s.\n", user.Username); err != nil {
		return err
	}
	if password != "" {
		if _, err := fmt.Fprintf(out, "Temporary password (shown once; it must be changed at the next sign-in): %s\n", password); err != nil {
			return err
		}
	}
	if !serverAllows && !user.BreakGlass {
		_, err := fmt.Fprintln(out, "Local password sign-in is off on this server, and this account is not a break-glass admin. Run `silo auth local-login enable` too.")
		return err
	}
	return nil
}

// randomTemporaryPassword returns 24 URL-safe characters (144 random bits),
// within bcrypt's 72-byte limit.
func randomTemporaryPassword() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating temporary password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// parseAuthCommand reads `local-login enable [-user name [-temporary-password]] [-env file]`.
func parseAuthCommand(args []string) (authCommand, error) {
	if len(args) < 2 || args[0] != authLocalLoginCommand || args[1] != authEnableAction {
		return authCommand{}, errors.New(authCommandUsage)
	}
	flags := flag.NewFlagSet("auth local-login enable", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	env := flags.String("env", ".env", "path to .env bootstrap file")
	user := flags.String("user", "", "username or email of one account to recover")
	temporary := flags.Bool("temporary-password", false, "also give the account a temporary password")
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
		return authCommand{}, errors.New(authCommandUsage)
	}
	cmd := authCommand{envFile: *env, user: strings.TrimSpace(*user), temporaryPassword: *temporary}
	if cmd.temporaryPassword && cmd.user == "" {
		return authCommand{}, errors.New(authCommandUsage)
	}
	return cmd, nil
}
