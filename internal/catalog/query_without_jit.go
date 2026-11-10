package catalog

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/readtx"
)

// queryWithoutJIT runs one read statement with JIT disabled and scans each
// row. Pool connections already start with JIT off unless the operator
// configured it, so SET LOCAL only matters where JIT was turned on.
func queryWithoutJIT(ctx context.Context, pool *pgxpool.Pool, query string, args []any, scan func(pgx.Rows) error) error {
	return readtx.Query(ctx, pool, query, args, eachRow(scan), readtx.NoJIT)
}

// queryGenericPlanWithoutJIT is queryWithoutJIT for a statement whose generic
// plan suits every call. The connection's cached statement then keeps one plan
// instead of planning again for each call's values.
func queryGenericPlanWithoutJIT(ctx context.Context, pool *pgxpool.Pool, query string, args []any, scan func(pgx.Rows) error) error {
	return readtx.Query(ctx, pool, query, args, eachRow(scan), readtx.NoJIT, readtx.GenericPlan)
}

func eachRow(scan func(pgx.Rows) error) func(pgx.Rows) error {
	return func(rows pgx.Rows) error {
		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
}
