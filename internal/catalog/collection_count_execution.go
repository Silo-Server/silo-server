package catalog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const collectionCountNodeStatements = 4

// Admission is process-local. A deployment with N API processes can run up to
// 4*N count transactions (and 2*N per account), independent of other catalog
// traffic. Pool capacity can lower the bound. Fail-fast admission creates no
// shared queue of waiting requests. Canceled connection cleanup owns its lease.
var collectionCountAdmission = collectionCountAdmissionState{
	accounts: make(map[int]int), pools: make(map[*pgxpool.Pool]int),
}

type collectionCountAdmissionState struct {
	mu       sync.Mutex
	active   int
	accounts map[int]int
	pools    map[*pgxpool.Pool]int
}

func (a *collectionCountAdmissionState) acquire(pool *pgxpool.Pool, accountID int, poolCapacity int) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active >= collectionCountNodeStatements || a.accounts[accountID] >= personalCollectionCountStatements || a.pools[pool] >= poolCapacity {
		return nil, fmt.Errorf("%w: count execution admission", ErrCollectionCountBudget)
	}
	a.active++
	a.accounts[accountID]++
	a.pools[pool]++
	return sync.OnceFunc(func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.active--
		a.accounts[accountID]--
		a.pools[pool]--
		if a.accounts[accountID] == 0 {
			delete(a.accounts, accountID)
		}
		if a.pools[pool] == 0 {
			delete(a.pools, pool)
		}
	}), nil
}

func executeCollectionCountBatch(ctx context.Context, pool *pgxpool.Pool, accountID int, sql string, args []any) (map[string]int, error) {
	if err := collectionCountContextError(ctx); err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, errors.New("collection counts require a database pool")
	}
	if len(sql) > collectionCountMaxSQLBytes || len(args) > collectionCountMaxArgs {
		return nil, ErrCollectionCountBudget
	}
	release, err := collectionCountAdmission.acquire(pool, accountID, int(pool.Config().MaxConns))
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		release()
		return nil, classifyCollectionCountError(ctx, err)
	}
	defer releaseCollectionCountConnection(conn, release)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, classifyCollectionCountError(ctx, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := setCollectionCountDeadline(ctx, tx); err != nil {
		return nil, classifyCollectionCountError(ctx, err)
	}
	counts, err := readCollectionCountBatch(ctx, tx, sql, args)
	if err != nil {
		return nil, classifyCollectionCountError(ctx, err)
	}
	return counts, nil
}

func setCollectionCountDeadline(ctx context.Context, tx pgx.Tx) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return fmt.Errorf("%w: count deadline required", ErrCollectionCountBudget)
	}
	remaining := time.Until(deadline).Milliseconds()
	if remaining < 1 {
		return ErrCollectionCountBudget
	}
	// The read-only transaction owns this setting. The duration is computed
	// after acquisition/BEGIN, so pool wait and construction consume the same
	// request deadline. Rolling back also restores any prior session setting.
	_, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", strconv.FormatInt(remaining, 10)+"ms")
	return err
}

func readCollectionCountBatch(ctx context.Context, tx pgx.Tx, sql string, args []any) (map[string]int, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		counts[id] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

func classifyCollectionCountError(ctx context.Context, err error) error {
	if contextErr := collectionCountContextError(ctx); contextErr != nil {
		return errors.Join(contextErr, err)
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch pgErr.Code {
		case "57014", "54000", "54001", "54011", "53200", "53300", "53400":
			return errors.Join(ErrCollectionCountBudget, err)
		}
	}
	return err
}

func releaseCollectionCountConnection(conn *pgxpool.Conn, release func()) {
	pgConn := conn.Conn().PgConn()
	needsCleanup := pgConn.IsClosed() || pgConn.IsBusy() || pgConn.TxStatus() != 'I'
	conn.Release()
	if !needsCleanup {
		release()
		return
	}
	// Pinned pgx sends CancelRequest asynchronously on cancellation. Keep the
	// lease until that cleanup finishes, even after this request returns. At
	// most four such owners exist because they retain node admission. pgx's
	// cleanup itself has a finite 15-second deadline; do not release early on a
	// separate timer and replace a backend that may still be canceling.
	releaseCollectionCountAfterCleanup(pgConn.CleanupDone(), release)
}

func releaseCollectionCountAfterCleanup(done <-chan struct{}, release func()) {
	select {
	case <-done:
		release()
	default:
		go func() {
			<-done
			release()
		}()
	}
}
