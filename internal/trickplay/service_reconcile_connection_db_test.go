package trickplay

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type reconcilePoolSettings struct {
	pool  *pgxpool.Pool
	reads int
}

func (s *reconcilePoolSettings) Get(ctx context.Context, key string) (string, error) {
	s.reads++
	var value string
	err := s.pool.QueryRow(ctx, `SELECT CASE WHEN $1::text = $2 THEN '320' ELSE '12' END`, key, WidthSetting).Scan(&value)
	return value, err
}

func TestReconcileUsesOneConnectionDB(t *testing.T) {
	for _, c := range []struct {
		maxConns         int32
		databaseSettings bool
	}{{1, false}, {1, true}, {2, false}, {2, true}} {
		t.Run(fmt.Sprintf("max-%d/settings-%v", c.maxConns, c.databaseSettings), func(t *testing.T) {
			f := newFixture(t)
			folder := f.library(t, "movies", true)
			file := f.file(t, folder, "single-reconcile")
			config := f.pool.Config().Copy()
			config.MaxConns = c.maxConns
			pool, err := pgxpool.NewWithConfig(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			settings := &reconcilePoolSettings{pool: pool}
			var reader SettingsReader
			if c.databaseSettings {
				reader = settings
			}
			service := NewService(pool, &fakeStore{}, reader, &fakeExtractor{}, "server")
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			stats, ran, err := service.Reconcile(ctx)
			if err != nil || !ran || stats.Added < 1 {
				t.Fatalf("reconcile: stats=%+v ran=%v error=%v", stats, ran, err)
			}
			if c.databaseSettings && settings.reads != 2 {
				t.Fatalf("settings reads=%d, want 2", settings.reads)
			}
			if row, ok := f.row(t, file); !ok || row.state != statePending {
				t.Fatalf("file not queued: row=%+v present=%v", row, ok)
			}
			if pool.Stat().AcquiredConns() != 0 {
				t.Fatalf("reconcile retained %d pool connections", pool.Stat().AcquiredConns())
			}
			var acquired bool
			if err := pool.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, reconcileLock).Scan(&acquired); err != nil || !acquired {
				t.Fatalf("reconcile lock was not released: acquired=%v error=%v", acquired, err)
			}
		})
	}
}
