package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/workerregistration"
)

func TestWorkerRegistryCanonicalOrigins(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		stored string
		want   string
	}{
		{"https://registry.example.test/", "https://registry.example.test"},
		{"HTTPS://REGISTRY.EXAMPLE.TEST:443", "https://registry.example.test"},
		{"http://registry.example.test:80/", "http://registry.example.test"},
		{"https://registry.example.test/prefix", "https://registry.example.test/prefix"},
		{"https://registry.example.test?edited=1", "https://registry.example.test?edited=1"},
		{"https://registry.example.test#edited", "https://registry.example.test#edited"},
	} {
		t.Run(tc.stored, func(t *testing.T) {
			host := "registry-" + uuid.NewString() + ".example.test"
			stored := strings.ReplaceAll(tc.stored, "registry.example.test", host)
			stored = strings.ReplaceAll(stored, "REGISTRY.EXAMPLE.TEST", strings.ToUpper(host))
			want := strings.ReplaceAll(tc.want, "registry.example.test", host)
			var id int
			if err := pool.QueryRow(t.Context(), "INSERT INTO stream_nodes(type,name,url,enabled) VALUES('transcode',$1,$2,TRUE) RETURNING id", "worker-registry-"+uuid.NewString(), stored).Scan(&id); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := pool.Exec(context.Background(), "DELETE FROM stream_nodes WHERE id=$1", id); err != nil {
					t.Error(err)
				}
			})
			if _, err := pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key,allow_http) VALUES($1,$2,TRUE)", id, []byte(public)); err != nil {
				t.Fatal(err)
			}
			registry := &coreWorkerRegistry{repo: workerregistration.New(pool, nil)}
			if err := registry.refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			origin := workerRegistryOrigin(stored)
			lookups := []string{origin, origin + "/"}
			if strings.HasPrefix(origin, "https://") {
				lookups = append(lookups, "HTTPS://"+strings.ToUpper(host)+":443")
			} else {
				lookups = append(lookups, "HTTP://"+strings.ToUpper(host)+":80/")
			}
			for _, lookup := range lookups {
				node, ok, err := registry.ByURL(t.Context(), lookup)
				if err != nil || !ok || node.ID != id || node.URL != want {
					t.Fatalf("cached lookup %q: node=%+v ok=%v err=%v; want id=%d URL=%q", lookup, node, ok, err, id, want)
				}
			}
			registry.mu.RLock()
			_, cached := registry.nodes[origin]
			registry.mu.RUnlock()
			if !cached {
				t.Fatal("canonical authority is missing from the preloaded cache")
			}
		})
	}
}
