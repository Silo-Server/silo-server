package main

import (
	"errors"
	"math"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestWorkerDownloadBatchChecksGenerationBoundsBeforeSQL(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	queries := &viewerQueryCounter{}
	cfg.ConnConfig.Tracer = queries
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, generation := range []uint64{0, uint64(math.MaxInt64) + 1, math.MaxUint64} {
		t.Run(strconv.FormatUint(generation, 10), func(t *testing.T) {
			// A bad generation in a later chunk must abort before even the
			// first valid chunk queries SQL or produces a partial decision.
			assignments := make([]workerwire.Assignment, workerContentBatchSize+1)
			for i := range assignments {
				assignments[i] = workerwire.Assignment{Path: workerPreparePath, WorkID: "generation-bound-fixture", Generation: 1}
			}
			assignments[len(assignments)-1].Generation = generation
			queries.count.Store(0)
			allowed, err := workerDownloadStatuses(t.Context(), pool, 1, assignments)
			if !errors.Is(err, workercontrolcore.ErrUnavailable) || allowed != nil || queries.count.Load() != 0 {
				t.Fatalf("invalid generation reached SQL or became a denial: generation=%d queries=%d decisions=%d err=%v", generation, queries.count.Load(), len(allowed), err)
			}
		})
	}
	queries.count.Store(0)
	allowed, err := workerDownloadStatuses(t.Context(), pool, 1, []workerwire.Assignment{{Path: workerPreparePath, WorkID: "generation-bound-fixture", Generation: math.MaxInt64}})
	if err != nil || len(allowed) != 1 || allowed[0] || queries.count.Load() != 1 {
		t.Fatalf("largest signed generation did not get an authoritative SQL decision: queries=%d decisions=%v err=%v", queries.count.Load(), allowed, err)
	}
}
