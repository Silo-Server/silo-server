package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/joho/godotenv"

	"github.com/Silo-Server/silo-server/internal/database"
)

const postgresTuneConfUsage = "usage: silo postgres-tune-conf [-env .env] -postgres-major <version>"

// runPostgresTuneConfCommand prints, as postgresql.conf lines, the settings
// POSTGRES_TUNE would apply that PostgreSQL reads only at start, and nothing
// when tuning is off. The omnibus image writes them into its bundled cluster
// before the server starts, so a new cluster runs with them from its first
// start instead of after a restart.
func runPostgresTuneConfCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("postgres-tune-conf", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	envFile := flags.String("env", ".env", "path to .env bootstrap file")
	major := flags.Int("postgres-major", 0, "major version of the PostgreSQL server")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w; %s", err, postgresTuneConfUsage)
	}
	if *major <= 0 || flags.NArg() > 0 {
		return errors.New(postgresTuneConfUsage)
	}
	// Read the environment as the server does: .env fills in only what is unset.
	if *envFile != "" {
		_ = godotenv.Load(*envFile)
	}
	// The pool size sizes only max_connections, which the startup settings leave out.
	opts, err := database.LoadPostgresTuneOptionsFromEnv(0)
	if err != nil {
		return err
	}
	if !opts.Enabled {
		return nil
	}
	for _, setting := range database.RecommendPostgresStartupSettings(opts, *major) {
		if _, err := fmt.Fprintf(out, "%s = '%s'\n", setting.Name, setting.Value); err != nil {
			return err
		}
	}
	return nil
}
