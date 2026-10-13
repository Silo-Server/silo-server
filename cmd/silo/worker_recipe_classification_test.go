package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workerregistration"
)

func TestWorkerRecipeClassificationUsesSQLRegistrationAndFailsClosed(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, enrolled := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "enrolled"}[enrolled], func(t *testing.T) {
			origin := "https://recipe-" + uuid.NewString() + ".example.test"
			var id int
			if err := pool.QueryRow(t.Context(), "INSERT INTO stream_nodes(type,name,url,enabled) VALUES('transcode',$1,$2,TRUE) RETURNING id", "recipe-classification-"+uuid.NewString(), origin).Scan(&id); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM stream_nodes WHERE id=$1", id) })
			if enrolled {
				if _, err := pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key,allow_http) VALUES($1,$2,FALSE)", id, []byte(public)); err != nil {
					t.Fatal(err)
				}
			}
			registry := &coreWorkerRegistry{repo: workerregistration.New(pool, nil), pool: pool}
			service, err := workercontrolcore.New(workercontrolcore.Options{Origin: "https://core.example", PrivateKey: private, Nodes: registry, Store: workercontrolcore.NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			if required, err := service.RequiresRecipe(t.Context(), origin); err != nil || required != enrolled {
				t.Fatal("recipe policy did not match actual SQL enrollment", err)
			}
			if !enrolled {
				return
			}
			for _, edited := range []string{origin + "/prefix", origin + "?edited=1", origin + "#edited"} {
				if _, err := pool.Exec(t.Context(), "UPDATE stream_nodes SET url=$2 WHERE id=$1", id, edited); err != nil {
					t.Fatal(err)
				}
				if err := registry.refresh(t.Context()); err != nil {
					t.Fatal(err)
				}
				if required, err := service.RequiresRecipe(t.Context(), origin); err == nil || required {
					t.Fatal("edited enrolled origin fell back to a legacy node")
				}
			}
			if _, err := pool.Exec(t.Context(), "UPDATE stream_nodes SET url=$2,enabled=FALSE WHERE id=$1", id, origin); err != nil {
				t.Fatal(err)
			}
			if err := registry.refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			if required, err := service.RequiresRecipe(t.Context(), origin); err == nil || required {
				t.Fatal("disabled enrolled node fell back to legacy dispatch")
			}
		})
	}
}
