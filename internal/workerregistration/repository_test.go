package workerregistration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
)

func registrationDB(t *testing.T) *Repository {
	return registrationDBWithRegistryFence(t, true)
}

func registrationDBWithRegistryFence(t *testing.T, withFence bool) *Repository {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	base, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "worker_registration_" + hex.EncodeToString(random[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		base.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(t.Context(), "CREATE TABLE stream_nodes(id INTEGER PRIMARY KEY,url TEXT NOT NULL,type TEXT NOT NULL,enabled BOOLEAN NOT NULL); CREATE TABLE download_artifacts(id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"20261013183440_standalone_http_workers.sql", "20261014164751_worker_registry_classification_fence.sql"} {
		if !withFence && name == "20261014164751_worker_registry_classification_fence.sql" {
			continue
		}
		migration, readErr := os.ReadFile("../../migrations/sql/" + name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		up, _, _ := strings.Cut(string(migration), "-- +goose Down")
		if _, err = pool.Exec(t.Context(), up); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(t.Context(), "INSERT INTO stream_nodes(id,url,type,enabled) VALUES(17,'https://worker.example.test','transcode',TRUE),(18,'https://other-worker.example.test','transcode',TRUE)"); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New(bytes.Repeat([]byte{0x53}, secret.MinMasterKeyLen))
	if err != nil {
		t.Fatal(err)
	}
	return New(pool, cipher)
}

func TestSigningKeyConcurrentCreationIsEncryptedAndStable(t *testing.T) {
	r := registrationDB(t)
	keys := make(chan ed25519.PrivateKey, 16)
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); key, err := r.SigningKey(t.Context()); keys <- key; errs <- err }()
	}
	wg.Wait()
	close(keys)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first ed25519.PrivateKey
	for key := range keys {
		if first == nil {
			first = key
		}
		if !bytes.Equal(first, key) {
			t.Fatal("concurrent callers received different signing keys")
		}
	}
	var count int
	var stored string
	if err := r.pool.QueryRow(t.Context(), "SELECT count(*) FROM worker_http_keys").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("key rows=%d", count)
	}
	if err := r.pool.QueryRow(t.Context(), "SELECT private_key FROM worker_http_keys").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !secret.IsEncrypted(stored) || strings.Contains(stored, string(first)) {
		t.Fatal("core private key not encrypted at rest")
	}
	again, err := New(r.pool, r.cipher).SigningKey(t.Context())
	if err != nil || !bytes.Equal(first, again) {
		t.Fatal("signing key changed across repository reconstruction")
	}
}

func TestEnrollmentRedeemsAtomicallyAndCannotReplay(t *testing.T) {
	r := registrationDB(t)
	ticket, err := r.Issue(t.Context(), 17, "https://core.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.Redeem(t.Context(), ticket, public) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("ticket consumed %d times", accepted.Load())
	}
	node, found, err := r.ByID(t.Context(), 17)
	if err != nil || !found || !bytes.Equal(node.PublicKey, public) {
		t.Fatal("redeemed public key was not persisted")
	}
	if r.Redeem(t.Context(), ticket, public) == nil {
		t.Fatal("lost-response retry reused consumed ticket")
	}
}

func TestEnrollmentRetiresOtherOutstandingTicketsForNode(t *testing.T) {
	r := registrationDB(t)
	exposed, err := r.Issue(t.Context(), 17, "https://core.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	reissued, err := r.Issue(t.Context(), 17, "https://core.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	otherNode, err := r.Issue(t.Context(), 18, "https://core.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	enrolled, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	intruder, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Redeem(t.Context(), reissued, enrolled); err != nil {
		t.Fatal(err)
	}
	if r.Redeem(t.Context(), exposed, intruder) == nil {
		t.Fatal("an older outstanding ticket replaced the enrolled worker key")
	}
	node, found, err := r.ByID(t.Context(), 17)
	if err != nil || !found || !bytes.Equal(node.PublicKey, enrolled) {
		t.Fatal("enrolled public key was not retained")
	}
	if err := r.Redeem(t.Context(), otherNode, intruder); err != nil {
		t.Fatalf("another node's ticket was retired: %v", err)
	}
}

func TestEnrollmentBindsEntireIssuedContextAndChangedNode(t *testing.T) {
	r := registrationDB(t)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*Enrollment){"node": func(e *Enrollment) { e.NodeID = 18 }, "node_url": func(e *Enrollment) { e.NodeURL = "https://other-worker.example.test" }, "core_url": func(e *Enrollment) { e.CoreURL = "https://other-core.example.test" }, "core_public_key": func(e *Enrollment) { e.CorePublicKey = public }, "http_policy": func(e *Enrollment) { e.AllowHTTP = true }}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			ticket, err := r.Issue(t.Context(), 17, "https://core.example.test", false)
			if err != nil {
				t.Fatal(err)
			}
			changed := ticket
			mutate(&changed)
			if r.Redeem(t.Context(), changed, public) == nil {
				t.Fatal("modified enrollment context accepted")
			}
			if err = r.Redeem(t.Context(), ticket, public); err != nil {
				t.Fatal("invalid attempt consumed original ticket")
			}
		})
	}
	ticket, err := r.Issue(t.Context(), 17, "https://core.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.pool.Exec(t.Context(), "UPDATE stream_nodes SET url='https://replacement.example.test' WHERE id=17"); err != nil {
		t.Fatal(err)
	}
	if r.Redeem(t.Context(), ticket, public) == nil {
		t.Fatal("ticket enrolled changed node URL")
	}
	changed := ticket
	changed.NodeURL = "https://replacement.example.test"
	if r.Redeem(t.Context(), changed, public) == nil {
		t.Fatal("rewritten ticket followed changed node URL")
	}
}

func TestEditedEnrolledOriginNeverFallsBackToLegacyWire(t *testing.T) {
	repo := registrationDB(t)
	var wireCalls atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wireCalls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
	}))
	defer node.Close()
	if _, err := repo.pool.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=17", node.URL); err != nil {
		t.Fatal(err)
	}
	ticket, err := repo.Issue(t.Context(), 17, "https://core.example.test", true)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err = repo.Redeem(t.Context(), ticket, pub); err != nil {
		t.Fatal(err)
	}
	key, err := repo.SigningKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	service, err := workercontrolcore.New(workercontrolcore.Options{Origin: ticket.CoreURL, PrivateKey: key, Nodes: repo, Store: workercontrolcore.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"/prefix", "?query=changed", "#fragment"} {
		t.Run(suffix, func(t *testing.T) {
			if _, err := repo.pool.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=17", node.URL+suffix); err != nil {
				t.Fatal(err)
			}
			registered, ok, err := repo.ByURL(t.Context(), node.URL)
			if err != nil || !ok || registered.ID != 17 {
				t.Fatal("edited enrolled node became an unknown legacy peer")
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, node.URL+"/transcode/start", strings.NewReader(`{"session_id":"work","input_path":"/core-only-fixture/private.mkv"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer fixture-original-customer-secret")
			if res, err := service.WrapTransport(http.DefaultTransport).RoundTrip(req); err == nil {
				if res != nil {
					_ = res.Body.Close()
				}
				t.Fatal("invalid enrolled origin reached legacy wire")
			}
			if wireCalls.Load() != 0 {
				t.Fatal("private core path or credential reached enrolled worker")
			}
		})
	}
}
