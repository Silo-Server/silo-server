package plugins

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"sync"
	"testing"
	"time"
)

func catalogSettingsTestStore(t *testing.T) *RepositoryStore {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	owner, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("catalog_settings_%d", time.Now().UnixNano())
	if _, err := owner.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		owner.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := owner.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
		owner.Close()
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
	_, err = pool.Exec(t.Context(), `CREATE TABLE server_settings (key text PRIMARY KEY,value text NOT NULL);
 CREATE TABLE plugin_repositories (id serial PRIMARY KEY,url text UNIQUE,display_name text,enabled boolean,managed_key text,source_kind text,updated_at timestamptz);
 CREATE TABLE plugin_installations (repository_id int);`)
	if err != nil {
		t.Fatal(err)
	}
	return NewRepositoryStore(pool)
}
func TestCatalogSettingsConditionalConcurrentCreation(t *testing.T) {
	s := catalogSettingsTestStore(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			_, err := s.SetIncludeApprovedCommunityConditional(t.Context(), true, new(false))
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrCatalogSettingsChanged) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
	current, err := s.GetCatalogSettings(t.Context())
	if err != nil || !current.IncludeApprovedCommunityPlugins {
		t.Fatal(current, err)
	}
	var enabled bool
	if err := s.pool.QueryRow(t.Context(), `SELECT bool_and(enabled) FROM plugin_repositories`).Scan(&enabled); err != nil || !enabled {
		t.Fatal(enabled, err)
	}
	// An unguarded bridge write still participates in the same row lock and
	// invalidates an older v2 snapshot.
	if _, err := s.SetIncludeApprovedCommunity(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIncludeApprovedCommunityConditional(t.Context(), true, new(true)); !errors.Is(err, ErrCatalogSettingsChanged) {
		t.Fatal(err)
	}
}
func TestCatalogSettingsConditionalReconcileRollback(t *testing.T) {
	s := catalogSettingsTestStore(t)
	if _, err := s.SetIncludeApprovedCommunity(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `ALTER TABLE plugin_repositories ADD CONSTRAINT test_reject_enable CHECK (enabled = false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIncludeApprovedCommunityConditional(t.Context(), true, new(false)); err == nil {
		t.Fatal("expected reconciliation failure")
	}
	value, err := readIncludeApprovedCommunity(t.Context(), s.pool)
	if err != nil || value {
		t.Fatal("configuration did not roll back", value, err)
	}
}
