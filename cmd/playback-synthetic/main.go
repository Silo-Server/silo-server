// playback-synthetic runs an explicitly disposable, synthetic initial-playback
// server. Database migrations must be applied before invoking this command.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/testfixture"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

type options struct {
	syntheticOnly     bool
	listen, bootstrap string
	duration          int
}
type bootstrap struct {
	DirectOnly     bool   `json:"direct_only"`
	URL            string `json:"url"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	AccountID      int    `json:"account_id"`
	ProfileID      string `json:"profile_id"`
	FileID         string `json:"file_id"`
	ContentID      string `json:"content_id"`
	InstallationID string `json:"installation_id"`
	MediaPath      string `json:"media_path"`
}

func main() {
	var opts options
	flag.BoolVar(&opts.syntheticOnly, "synthetic-only", false, "acknowledge fresh disposable synthetic data only")
	flag.StringVar(&opts.listen, "listen", "127.0.0.1:0", "HTTP listen address; override explicitly for device access")
	flag.StringVar(&opts.bootstrap, "bootstrap", "", "new private bootstrap JSON path (default: temporary directory)")
	flag.IntVar(&opts.duration, "duration", 3600, "harness lifetime in seconds (1..3600)")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, opts options) (result error) {
	if !opts.syntheticOnly {
		return errors.New("--synthetic-only is required; this executable cannot adopt existing accounts")
	}
	if opts.duration < 1 || opts.duration > 3600 {
		return errors.New("--duration must be between 1 and 3600 seconds")
	}
	dsn, redisURL := os.Getenv("SILO_SYNTHETIC_DATABASE_URL"), os.Getenv("SILO_SYNTHETIC_REDIS_URL")
	if dsn == "" || redisURL == "" {
		return errors.New("SILO_SYNTHETIC_DATABASE_URL and SILO_SYNTHETIC_REDIS_URL are required")
	}
	cipher, err := secret.New([]byte(os.Getenv("SECRET_KEY")))
	if err != nil {
		return errors.New("a stable SECRET_KEY of at least 32 bytes is required")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(opts.duration)*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return errors.New("invalid disposable database configuration")
	}
	defer pool.Close()
	// Serialize harness instances before checking or creating any synthetic rows.
	lock, err := pool.Acquire(ctx)
	if err != nil {
		return errors.New("cannot connect to disposable database")
	}
	defer lock.Release()
	var acquired bool
	if err = lock.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('playback-synthetic-harness',0))`).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return errors.New("another synthetic harness owns this database")
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, err := lock.Exec(cleanup, `SELECT pg_advisory_unlock(hashtextextended('playback-synthetic-harness',0))`)
		result = errors.Join(result, err)
	}()
	var occupied bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users) OR EXISTS(SELECT 1 FROM media_items) OR EXISTS(SELECT 1 FROM media_files) OR EXISTS(SELECT 1 FROM media_folders) OR EXISTS(SELECT 1 FROM playback_v3_attempts)`).Scan(&occupied); err != nil {
		return fmt.Errorf("check migrated disposable database: %w", err)
	}
	if occupied {
		return errors.New("refusing database containing accounts, media, libraries, or playback attempts; use a fresh disposable migrated database")
	}
	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		return errors.New("invalid disposable Redis configuration")
	}
	redisClient := redis.NewClient(redisOpts)
	defer func() { result = errors.Join(result, redisClient.Close()) }()
	if err = redisClient.Ping(ctx).Err(); err != nil {
		return errors.New("cannot connect to disposable Redis")
	}
	dir, err := os.MkdirTemp("", "silo-playback-synthetic-")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(dir)) }()
	source, err := testfixture.ProvisionPostgres(ctx, pool)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, err := pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, source.AccountID)
		result = errors.Join(result, err)
	}()
	password := randomSecret()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err = pool.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1`, source.AccountID, string(hash)); err != nil {
		return err
	}
	media, err := createMedia(ctx, pool, dir)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		result = errors.Join(result, media.remove(cleanup, pool))
	}()
	settings := catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher)
	values, err := settings.GetAll(ctx)
	if err != nil {
		return errors.New("cannot read encrypted settings; reuse the original SECRET_KEY")
	}
	if values["auth.jwt_secret"] == "" {
		values["auth.jwt_secret"] = randomSecret()
		if err = settings.Set(ctx, "auth.jwt_secret", values["auth.jwt_secret"]); err != nil {
			return err
		}
	}
	cfg, err := config.LoadFromDB(values)
	if err != nil {
		return err
	}
	cfg.Playback.TranscodeEnabled = false
	cfg.Playback.TranscodeDir = filepath.Join(dir, "transcodes")
	installation, err := diagnostics.ServerInstanceID(ctx, catalog.NewServerSettingsRepo(pool))
	if err != nil {
		return err
	}
	provider := pgstore.NewPostgresProvider(pool)
	ownerPolicy := playback.RuntimeGrantPolicyV3{MaxDuration: 30 * time.Second, SafetyMargin: time.Second, RenewBefore: 10 * time.Second, PollInterval: 10 * time.Millisecond}
	grantPolicy := playback.RuntimeGrantPolicyV3{MaxDuration: time.Second, SafetyMargin: 100 * time.Millisecond, RenewBefore: 200 * time.Millisecond, PollInterval: 10 * time.Millisecond}
	flow, err := api.NewInitialPlaybackRuntime(ctx, pool, redisClient, provider, installation, ownerPolicy, grantPolicy)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", opts.listen)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	manager := playback.NewSessionManager(0, 0)
	handler := api.NewRouter(api.Dependencies{Config: cfg, AppContext: ctx, DB: pool, SecretCipher: cipher, UserStoreProvider: provider, RedisClient: redisClient, SessionMgr: manager, FileRepo: scanner.NewFileRepository(pool), FolderRepo: catalog.NewFolderRepository(pool), ClientIPResolver: clientip.NewResolver(nil), NodeID: "synthetic-playback", InitialPlayback: flow})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	defer func() {
		// One self-contained line so a transcript shows the harness began its
		// bounded shutdown and owned-fixture cleanup, whether from a signal or expiry.
		fmt.Fprintf(os.Stderr, "playback-synthetic: shutting down (%v); removing owned synthetic fixtures\n", context.Cause(ctx))
		cancel()
		for _, session := range manager.AllSessions() {
			_ = manager.StopSession(session.ID)
		}
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		err := server.Shutdown(shutdown)
		if err != nil {
			err = errors.Join(err, server.Close())
		}
		for _, session := range manager.AllSessions() {
			_ = manager.StopSession(session.ID)
		}
		result = errors.Join(result, err)
	}()
	path := opts.bootstrap
	if path == "" {
		path = filepath.Join(dir, "bootstrap.json")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	info := bootstrap{DirectOnly: true, URL: "http://" + listener.Addr().String(), Username: source.Username, Password: password, AccountID: source.AccountID, ProfileID: source.ProfileID, FileID: strconv.Itoa(media.fileID), ContentID: media.contentID, InstallationID: installation, MediaPath: media.path}
	if err = writeBootstrap(path, info); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.Remove(path)) }()
	fmt.Println(path)
	select {
	case <-ctx.Done():
		return nil
	case err = <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func randomSecret() string {
	var data [32]byte
	_, _ = rand.Read(data[:])
	return hex.EncodeToString(data[:])
}
func writeBootstrap(path string, info bootstrap) (result error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, file.Close())
		if result != nil {
			_ = os.Remove(path)
		}
	}()
	if err = json.NewEncoder(file).Encode(info); err != nil {
		return err
	}
	return file.Sync()
}
