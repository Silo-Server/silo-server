package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerregistration"
)

type cliEnrollmentRepo struct {
	mu     sync.Mutex
	ticket workerregistration.Enrollment
	calls  int
}

func TestWorkerCommandHelpSucceedsBeforeBootstrap(t *testing.T) {
	for _, name := range []string{workerDatabaseURL, workerRedisURL, workerSecretKey, workerJWTSecret} {
		t.Setenv(name, "unusable-bootstrap-value")
	}
	for _, command := range []struct {
		name, option string
		run          func(context.Context, []string) error
	}{
		{standaloneWorkerCommand, "-scratch", runStandaloneWorker},
		{"worker-enroll", "-ticket", runWorkerEnroll},
		{"worker-enrollment", "-node-id", runWorkerEnrollment},
	} {
		t.Run(command.name, func(t *testing.T) {
			output, err := os.CreateTemp(t.TempDir(), "help")
			if err != nil {
				t.Fatal(err)
			}
			previous := os.Stdout
			os.Stdout = output
			t.Cleanup(func() { os.Stdout = previous; _ = output.Close() })
			if err := command.run(t.Context(), []string{"--help"}); err != nil {
				t.Fatalf("help reached error/bootstrap handling: %v", err)
			}
			if err := output.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(output.Name())
			if err != nil || !bytes.Contains(data, []byte("Usage of "+command.name)) || !bytes.Contains(data, []byte(command.option)) || bytes.Contains(data, []byte("unusable-bootstrap-value")) {
				t.Fatalf("missing or unsafe command help: %q, %v", data, err)
			}
		})
	}
}

func (r *cliEnrollmentRepo) Redeem(_ context.Context, e workerregistration.Enrollment, key ed25519.PublicKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls != 1 || e.Ticket != r.ticket.Ticket || e.NodeID != r.ticket.NodeID || len(key) != ed25519.PublicKeySize {
		return errors.New("invalid ticket")
	}
	return nil
}

func TestWorkerEnrollCLIUsesHTTPAndPersistsPrivateBinding(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "REDIS_URL", "SECRET_KEY", "JWT_SECRET"} {
		t.Setenv(name, "")
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer server.Close()
	ticket := workerregistration.Enrollment{Version: 1, NodeID: 17, CoreURL: server.URL, CorePublicKey: public, NodeURL: "http://worker.example.test", AllowHTTP: true, Ticket: strings.Repeat("34", 32), ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
	repo := &cliEnrollmentRepo{ticket: ticket}
	handler, err = workerregistration.NewHandler(workerregistration.Options{Repository: repo, Origin: server.URL, PrivateKey: key, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ticketPath := filepath.Join(dir, "ticket.json")
	bindingPath := filepath.Join(dir, "binding.json")
	if err = workerregistration.SaveEnrollment(ticketPath, ticket); err != nil {
		t.Fatal(err)
	}
	if err = runWorkerEnroll(t.Context(), []string{"--ticket", ticketPath, "--binding", bindingPath}); err != nil {
		t.Fatal(err)
	}
	binding, err := workeridentity.LoadBinding(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	if binding.NodeID != ticket.NodeID || binding.CoreURL != ticket.CoreURL || !bytes.Equal(binding.CorePublicKey, ticket.CorePublicKey) || len(binding.NodePrivateKey) != ed25519.PrivateKeySize {
		t.Fatal("CLI persisted wrong binding")
	}
	if err = runWorkerEnroll(t.Context(), []string{"--ticket", ticketPath, "--binding", bindingPath}); err == nil {
		t.Fatal("CLI overwrote existing binding")
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.calls != 1 {
		t.Fatalf("overwrite check sent second enrollment: %d", repo.calls)
	}
}

func TestEnrollmentCLIRejectsMissingPathsAndCredentialEnvironment(t *testing.T) {
	if runWorkerEnrollment(t.Context(), nil) == nil {
		t.Fatal("core enrollment accepted missing node/output")
	}
	if runWorkerEnroll(t.Context(), nil) == nil {
		t.Fatal("worker enrollment accepted missing ticket/binding")
	}
	t.Setenv("DATABASE_URL", "postgres://invalid.example.test/never-contact")
	if err := runWorkerEnroll(t.Context(), []string{"--ticket", "absent.json", "--binding", filepath.Join(t.TempDir(), "binding.json")}); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatal("worker accepted infrastructure credential environment")
	}
	if _, err := os.Stat("absent.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("test expected absent ticket")
	}
}

func TestWorkerEnrollmentCLIUsesLocalSQLAndExportsNoBootstrapCredentials(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "worker_enrollment_cli_" + hex.EncodeToString(random[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = pool.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	configuration, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	configuration.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(t.Context(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	if _, err = isolated.Exec(t.Context(), "CREATE TABLE stream_nodes(id INTEGER PRIMARY KEY,url TEXT,type TEXT,enabled BOOLEAN);CREATE TABLE server_settings(key TEXT PRIMARY KEY,value TEXT);CREATE TABLE download_artifacts(id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20261013183440_standalone_http_workers.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err = isolated.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	if _, err = isolated.Exec(t.Context(), "INSERT INTO stream_nodes VALUES(17,'https://worker.example.test','transcode',TRUE);INSERT INTO server_settings VALUES('server.public_url','https://core.example.test')"); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	t.Setenv("DATABASE_URL", parsed.String())
	master := strings.Repeat("test-master-key-", 3)
	t.Setenv("SECRET_KEY", master)
	t.Setenv("REDIS_URL", "redis://never-contact.example.test")
	t.Setenv("JWT_SECRET", "never-export-jwt")
	path := filepath.Join(t.TempDir(), "ticket.json")
	if err = runWorkerEnrollment(t.Context(), []string{"--node-id", "17", "--env", "/dev/null", "--out", path}); err != nil {
		t.Fatal(err)
	}
	ticket, err := workerregistration.LoadEnrollment(path)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.CoreURL != "https://core.example.test" || ticket.NodeURL != "https://worker.example.test" || ticket.NodeID != 17 || ticket.ExpiresAt <= time.Now().Unix() || ticket.ExpiresAt > time.Now().Add(5*time.Minute).Unix() {
		t.Fatal("CLI changed enrollment authority")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{parsed.String(), master, "never-export-jwt", "redis://never-contact.example.test", "database_url", "redis_url", "secret_key", "jwt_secret", "private_key"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("ticket contains forbidden bootstrap field %q", forbidden)
		}
	}
}
