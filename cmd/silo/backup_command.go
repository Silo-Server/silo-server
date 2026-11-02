package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/migrations"
)

const backupCommandUsage = `usage:
  silo backup restore --from <backup> [--database-only] [--accept-lost-credentials] [--keep-redis] [-env .env]
      Replace the live database (DATABASE_URL) and restore files into the live storage, then
      clear the Redis database Silo uses (FLUSHDB) unless --keep-redis.
      Stop Silo on every server and turn off automatic restarts first.
  silo backup restore --from <backup> --into <database URL> (--blobs-into <dir> | --database-only)
                      [--accept-lost-credentials] [-env .env]
      Restore into a new, empty database and a separate location; nothing live changes.
  silo backup restore --recover <time> [-env .env]
      Finish a replace that stopped, from silo-restore-<time>.journal in this directory.
  silo backup verify --from <backup> --scratch <database URL> [-env .env]
      Restore into an empty scratch database and check it against the manifest.

<backup> is a backup folder on this computer: the folder at the backup destination, or a
downloaded copy of its objects`

// The `silo backup` subcommands.
const (
	backupRestoreCommand = "restore"
	backupVerifyCommand  = "verify"
)

// runBackupMain runs `silo backup ...` for main. The first SIGINT or SIGTERM
// cancels the command, which stops what it is doing and runs its cleanup
// (the working folder with decrypted files, a decrypt's .partial file, a
// swap's rollback) before returning; a second signal exits at once.
func runBackupMain(args []string, out, errOut io.Writer) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-signals:
			// The default handling is back, so the next signal ends the
			// process.
			signal.Stop(signals)
			_, _ = fmt.Fprintln(errOut, interruptNotice())
			cancel()
		case <-ctx.Done():
		}
	}()
	return runBackupCommand(ctx, args, out)
}

// swapJournal is the journal time of a swap-mode restore or recovery under
// way in this process: from then on, stopping can leave the databases
// closed.
var swapJournal atomic.Pointer[string]

// interruptNotice is what the first interrupt prints. During a swap it says
// what stopping at once risks and the command that finishes the swap.
func interruptNotice() string {
	if stamp := swapJournal.Load(); stamp != nil {
		return fmt.Sprintf("Interrupted: finishing or rolling back the restore, which can take a few minutes. "+
			"Interrupting again stops at once and may leave the databases closed; then run: silo backup restore --recover %s", *stamp)
	}
	return "Interrupted: cleaning up. Interrupt again to stop at once."
}

// runBackupCommand runs `silo backup ...`, the offline full restore and the
// restore drill. Full restore is a command, not an API operation: it replaces
// the database the API server runs on.
func runBackupCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(backupCommandUsage)
	}
	switch args[0] {
	case backupRestoreCommand:
		return runBackupRestore(ctx, args[1:], out)
	case backupVerifyCommand:
		return runBackupVerify(ctx, args[1:], out)
	default:
		return errors.New(backupCommandUsage)
	}
}

type backupRestoreFlags struct {
	envFile, from, into, blobsInto, recoverTime string
	databaseOnly, acceptLostCredentials         bool
	keepRedis                                   bool
}

func parseBackupRestoreFlags(args []string) (backupRestoreFlags, error) {
	var f backupRestoreFlags
	flags := flag.NewFlagSet("backup "+backupRestoreCommand, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&f.envFile, "env", ".env", "path to .env file")
	flags.StringVar(&f.from, "from", "", "backup folder")
	flags.StringVar(&f.into, "into", "", "empty database to restore into (separate mode)")
	flags.StringVar(&f.blobsInto, "blobs-into", "", "directory for the restored files (separate mode)")
	flags.BoolVar(&f.databaseOnly, "database-only", false, "skip files")
	flags.BoolVar(&f.acceptLostCredentials, "accept-lost-credentials", false, "restore a backup taken with another SECRET_KEY")
	flags.BoolVar(&f.keepRedis, "keep-redis", false, "leave Redis as it is after a replace")
	flags.StringVar(&f.recoverTime, "recover", "", "finish an interrupted replace from its journal")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return f, errors.New(backupCommandUsage)
	}
	switch {
	case f.recoverTime != "":
		if f.from != "" || f.into != "" || f.blobsInto != "" || f.databaseOnly || f.acceptLostCredentials || f.keepRedis {
			return f, errors.New("--recover takes no other options\n" + backupCommandUsage)
		}
	case f.from == "":
		return f, errors.New("--from is required\n" + backupCommandUsage)
	case strings.Contains(f.from, "://"):
		return f, errors.New("--from must be a backup folder on this computer")
	case f.blobsInto != "" && f.into == "":
		return f, errors.New("--blobs-into needs --into\n" + backupCommandUsage)
	case f.into != "" && f.blobsInto == "" && !f.databaseOnly:
		return f, errors.New("--into needs --blobs-into, or --database-only\n" + backupCommandUsage)
	case f.into != "" && f.databaseOnly && f.blobsInto != "":
		return f, errors.New("--blobs-into and --database-only exclude each other")
	case strings.Contains(f.blobsInto, "://"):
		return f, errors.New("--blobs-into must be a directory on this computer")
	}
	return f, nil
}

func runBackupRestore(ctx context.Context, args []string, out io.Writer) error {
	f, err := parseBackupRestoreFlags(args)
	if err != nil {
		return err
	}
	_ = godotenv.Load(f.envFile)
	if f.recoverTime != "" {
		return runBackupRecover(ctx, f.recoverTime, out)
	}
	masterKey, keyCheck, err := backupMasterKey(true)
	if err != nil {
		return err
	}
	src, tools, validate, err := openBackupSource(ctx, f.from, keyCheck, f.acceptLostCredentials)
	if err != nil {
		return err
	}
	loader := backup.PGLoader{Tools: tools}
	if f.into != "" {
		opts := backup.SeparateOptions{
			Validate: validate, Into: f.into, BlobsInto: f.blobsInto, DatabaseOnly: f.databaseOnly, Out: out,
		}
		if _, err := backup.RestoreSeparate(ctx, src, loader, opts); err != nil {
			return err
		}
		if !f.keepRedis {
			_, _ = fmt.Fprintln(out, separateRestoreRedisNote)
		}
		return nil
	}
	liveURL, err := liveDatabaseURL()
	if err != nil {
		return err
	}
	cipher, err := secret.New(masterKey)
	if err != nil {
		return err
	}
	// The live deployment's storage and Redis, read from its configuration
	// before anything changes; this connection closes before the preflight
	// checks that no other client is connected. The journal keeps the
	// storage so --recover never reopens a database to find it.
	var storeConfig backup.StoreConfig
	afterRedis := redisPlan{keep: f.keepRedis}
	if !f.databaseOnly || (!f.keepRedis && os.Getenv("REDIS_URL") == "") {
		cfg, err := liveConfigFromDatabase(ctx, liveURL, cipher)
		switch {
		case err != nil && !f.databaseOnly:
			return fmt.Errorf("reading the live blob storage settings: %w", err)
		case err != nil:
			afterRedis.readErr = err
		default:
			storeConfig = backupStoreConfig(cfg)
			afterRedis.config = liveRedisConfig(cfg)
		}
	} else {
		afterRedis.config = liveRedisConfig(&config.Config{})
	}
	ctl, err := backup.ConnectControl(ctx, liveURL)
	if err != nil {
		return err
	}
	defer closeControl(ctx, ctl)
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := backup.RestoreSwap(ctx, src, loader, ctl, backup.SwapOptions{
		Validate: validate, LiveURL: liveURL, DatabaseOnly: f.databaseOnly,
		StoreConfig: storeConfig, OpenStores: storesFromConfig, Cipher: cipher,
		RestoredSettings: plainSettings, WorkDir: workDir, Out: out,
		Journaled: func(stamp string) { swapJournal.Store(&stamp) },
	}); err != nil {
		return err
	}
	clearRedisAfterRestore(ctx, afterRedis, dialRedis, out)
	return nil
}

// closeControl closes the control connection after the command's context
// may have been canceled, with a bound so a stalled close cannot keep the
// command from exiting.
func closeControl(ctx context.Context, ctl *backup.PGControl) {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = ctl.Close(closeCtx)
}

func runBackupRecover(ctx context.Context, stamp string, out io.Writer) error {
	swapJournal.Store(&stamp)
	masterKey, _, err := backupMasterKey(true)
	if err != nil {
		return err
	}
	cipher, err := secret.New(masterKey)
	if err != nil {
		return err
	}
	liveURL, err := liveDatabaseURL()
	if err != nil {
		return err
	}
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	ctl, err := backup.ConnectControl(ctx, liveURL)
	if err != nil {
		return err
	}
	defer closeControl(ctx, ctl)
	// Neither database is opened: the journal holds the storage
	// configuration, and the control connection reads which database is
	// which from pg_database.
	if err := backup.RecoverSwap(ctx, backup.JournalPath(workDir, stamp), ctl, cipher, storesFromConfig, out); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "If the restore went ahead, run FLUSHDB on the Redis database Silo uses before starting Silo.")
	return nil
}

func runBackupVerify(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("backup "+backupVerifyCommand, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	envFile := flags.String("env", ".env", "path to .env file")
	from := flags.String("from", "", "backup folder")
	scratch := flags.String("scratch", "", "empty scratch database URL")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *from == "" || *scratch == "" {
		return errors.New(backupCommandUsage)
	}
	_ = godotenv.Load(*envFile)
	// The drill runs anywhere: without SECRET_KEY it only cannot say whether
	// the key matches.
	_, keyCheck, err := backupMasterKey(false)
	if err != nil {
		return err
	}
	src, tools, validate, err := openBackupSource(ctx, *from, keyCheck, true)
	if err != nil {
		return err
	}
	return backup.VerifyBackup(ctx, src, backup.PGLoader{Tools: tools}, *scratch, validate, out)
}

// liveDatabaseURL reads DATABASE_URL, the database a replace or recovery
// works on.
func liveDatabaseURL() (string, error) {
	liveURL := os.Getenv("DATABASE_URL")
	if liveURL == "" {
		return "", errors.New("DATABASE_URL is required (set in .env or environment)")
	}
	return liveURL, nil
}

// backupMasterKey reads SECRET_KEY and its check value.
func backupMasterKey(required bool) ([]byte, string, error) {
	key := os.Getenv("SECRET_KEY")
	if key == "" && !required {
		return nil, "", nil
	}
	if len(key) < secret.MinMasterKeyLen {
		return nil, "", fmt.Errorf("SECRET_KEY is required (>=%d chars): the key the backup's server used", secret.MinMasterKeyLen)
	}
	check, err := secret.KeyCheckValue([]byte(key))
	return []byte(key), check, err
}

// findBackupTools discovers pg_dump and pg_restore; tests replace it.
var findBackupTools = backup.FindTools

// openBackupSource opens the backup folder at from, the tools that restore
// it, and the checks a restore runs: keyCheck is this SECRET_KEY's check
// value, and acceptLostCredentials restores a backup taken with another key.
func openBackupSource(ctx context.Context, from, keyCheck string, acceptLostCredentials bool) (*backup.Source, backup.Tools, backup.ValidateOptions, error) {
	src, err := backup.OpenSource(from)
	if err != nil {
		return nil, backup.Tools{}, backup.ValidateOptions{}, err
	}
	tools, err := findBackupTools(ctx)
	if err != nil {
		return nil, backup.Tools{}, backup.ValidateOptions{}, err
	}
	if err := backup.CheckRestoreTools(tools, src.Manifest); err != nil {
		return nil, backup.Tools{}, backup.ValidateOptions{}, err
	}
	carried, err := database.MigrationVersions(migrations.FS, "sql")
	if err != nil {
		return nil, backup.Tools{}, backup.ValidateOptions{}, err
	}
	validate := backup.ValidateOptions{KeyCheck: keyCheck, BinaryMigrations: carried, AcceptLostCredentials: acceptLostCredentials}
	if len(carried) > 0 {
		validate.BinaryGooseVersion = carried[len(carried)-1]
	}
	return src, tools, validate, nil
}

// liveConfigFromDatabase reads the configuration a database's settings
// hold, the way the server reads it at startup: the blob storage and Redis
// a replace works with.
func liveConfigFromDatabase(ctx context.Context, dbURL string, cipher *secret.Cipher) (*config.Config, error) {
	pool, err := database.NewPoolForRole(ctx, config.DatabaseConfig{URL: dbURL, MaxConnections: 2}, "application")
	if err != nil {
		return nil, err
	}
	defer database.ClosePool(pool)
	settings, err := catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher).GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the settings: %w", err)
	}
	cfg, err := config.LoadFromDB(settings)
	if err != nil {
		return nil, err
	}
	normalizeLoadedConfig(cfg)
	return cfg, nil
}

func backupStoreConfig(cfg *config.Config) backup.StoreConfig {
	bucket := func(endpoint, region, name, prefix, access, secretKey string, pathStyle bool) *backup.BucketConfig {
		if name == "" {
			return nil
		}
		return &backup.BucketConfig{Endpoint: endpoint, Region: region, Bucket: name, KeyPrefix: prefix, AccessKey: access, SecretKey: secretKey, PathStyle: pathStyle}
	}
	pub, priv := cfg.S3.Public, cfg.S3.Private
	return backup.StoreConfig{
		Backend: cfg.Artwork.StorageBackend, LocalPath: cfg.Artwork.LocalPath,
		Public:  bucket(pub.Endpoint, pub.Region, pub.Bucket, pub.KeyPrefix, pub.AccessKey, pub.SecretKey, pub.PathStyle),
		Private: bucket(priv.Endpoint, priv.Region, priv.Bucket, priv.KeyPrefix, priv.AccessKey, priv.SecretKey, priv.PathStyle),
	}
}

// storesFromConfig opens the stores a StoreConfig names without recording
// anything or changing bucket settings: a restore must not write to the
// database it reads, and reconfiguring a bucket is the server's business.
func storesFromConfig(ctx context.Context, sc backup.StoreConfig) (blobstore.Stores, error) {
	client := func(role string, b *backup.BucketConfig) *s3client.Client {
		if b == nil {
			return nil
		}
		return s3client.NewClient(s3client.BucketConfig{Role: role, Endpoint: b.Endpoint, Region: b.Region, Bucket: b.Bucket,
			KeyPrefix: b.KeyPrefix, AccessKey: b.AccessKey, SecretKey: b.SecretKey, PathStyle: b.PathStyle})
	}
	stores, _, err := blobstore.Open(ctx, blobstore.Options{
		Backend: sc.Backend, LocalPath: sc.LocalPath,
		S3: client("metadata", sc.Public), S3Private: client("operational", sc.Private),
	})
	return stores, err
}

// plainSettings reads settings without decrypting: the storage identity rows
// a swap compares are not encrypted, and a backup taken with another key
// must still be checkable.
func plainSettings(ctx context.Context, dbURL string) (map[string]string, error) {
	pool, err := database.NewPoolForRole(ctx, config.DatabaseConfig{URL: dbURL, MaxConnections: 1}, "application")
	if err != nil {
		return nil, err
	}
	defer database.ClosePool(pool)
	return catalog.NewServerSettingsRepo(pool).GetAll(ctx)
}

// runSecretCommand runs `silo secret check`: it reads a SECRET_KEY on stdin
// and prints its check value, the one every backup manifest records, so an
// operator can confirm which key a backup needs without showing the key.
func runSecretCommand(args []string, in io.Reader, out io.Writer) error {
	if len(args) != 1 || args[0] != "check" {
		return errors.New("usage: printf %s \"$SECRET_KEY\" | silo secret check")
	}
	key, err := bufio.NewReader(io.LimitReader(in, 64<<10)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	key = strings.TrimRight(key, "\r\n")
	check, err := secret.KeyCheckValue([]byte(key))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, check)
	return err
}
