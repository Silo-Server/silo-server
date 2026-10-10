package partman

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const indexPublicSchema = "public"

// EnsureIndexes finishes parent indexes created ON ONLY by a SQL migration.
// Historical leaf indexes are built concurrently, then attached to the parent.
// The SQL migration remains the source of the index definition; this maintenance
// never holds its column-addition transaction open while scanning history.
func (m *Manager) EnsureIndexes(ctx context.Context, names ...string) error {
	for _, name := range names {
		if err := m.ensureIndex(ctx, name); err != nil {
			return fmt.Errorf("ensure partition index %s: %w", name, err)
		}
	}
	return nil
}

func (m *Manager) ensureIndex(ctx context.Context, name string) error {
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	lock := "silo:partition-index:public:" + name
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lock); err != nil {
			// Never return a session holding the maintenance lock to the shared pool.
			_ = conn.Conn().Close(cleanup)
		}
	}()
	// A blocking advisory-lock query holds a snapshot while waiting, which
	// can deadlock the holder's concurrent index build during its snapshot wait.
	// Finish every try-lock query before waiting for another startup to finish.
	wait := 50 * time.Millisecond
	for {
		var acquired bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, lock).Scan(&acquired); err != nil {
			return err
		}
		if acquired {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Second)
	}
	var oid uint32
	var valid bool
	var definition string
	if err := conn.QueryRow(ctx, `SELECT i.indexrelid,i.indisvalid,pg_get_indexdef(i.indexrelid)
 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE i.indrelid=to_regclass($1) AND n.nspname='public' AND c.relname=$2`, pgx.Identifier{indexPublicSchema, m.table}.Sanitize(), name).Scan(&oid, &valid, &definition); err != nil {
		return err
	}
	if valid {
		return nil
	}
	_, tail, found := strings.Cut(definition, " USING ")
	if !found {
		return fmt.Errorf("unrecognized parent index definition")
	}
	type leaf struct {
		schema, name string
		oid          uint32
	}
	rows, err := conn.Query(ctx, `SELECT n.nspname,c.relname,c.oid FROM pg_inherits t
 JOIN pg_class c ON c.oid=t.inhrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE t.inhparent=to_regclass($1) AND NOT EXISTS (
 SELECT 1 FROM pg_inherits p JOIN pg_index i ON i.indexrelid=p.inhrelid
 WHERE p.inhparent=$2 AND i.indrelid=c.oid AND i.indisvalid)
 ORDER BY c.oid`, pgx.Identifier{indexPublicSchema, m.table}.Sanitize(), oid)
	if err != nil {
		return err
	}
	var leaves []leaf
	for rows.Next() {
		var l leaf
		if err := rows.Scan(&l.schema, &l.name, &l.oid); err != nil {
			rows.Close()
			return err
		}
		leaves = append(leaves, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, l := range leaves {
		if err := buildLeafIndex(ctx, conn, name, tail, l.schema, l.name, l.oid); err != nil {
			return err
		}
	}
	if err := conn.QueryRow(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid=$1`, oid).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("parent index remains incomplete; retry maintenance")
	}
	return nil
}

func buildLeafIndex(ctx context.Context, conn *pgxpool.Conn, parent, tail, schema, table string, tableOID uint32) error {
	name := fmt.Sprintf("%s_p_%d", parent, tableOID)
	qualified := pgx.Identifier{schema, name}.Sanitize()
	var exists, valid bool
	var indexedTable uint32
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class WHERE oid=to_regclass($1))`, qualified).Scan(&exists); err != nil {
		return err
	}
	if exists {
		if err := conn.QueryRow(ctx, `SELECT indrelid,indisvalid FROM pg_index WHERE indexrelid=to_regclass($1)`, qualified).Scan(&indexedTable, &valid); err != nil {
			return err
		}
		if indexedTable != tableOID {
			return fmt.Errorf("leaf index name belongs to another table")
		}
		if !valid {
			// A canceled concurrent build leaves an unattached invalid index. Rebuild
			// it rather than mistaking IF NOT EXISTS for a successful prior attempt.
			if _, err := conn.Exec(ctx, "DROP INDEX CONCURRENTLY "+qualified); err != nil {
				return err
			}
		}
	}
	if !exists || !valid {
		if _, err := conn.Exec(ctx, "CREATE INDEX CONCURRENTLY "+pgx.Identifier{name}.Sanitize()+" ON "+pgx.Identifier{schema, table}.Sanitize()+" USING "+tail); err != nil {
			return err
		}
	}
	_, err := conn.Exec(ctx, "ALTER INDEX "+pgx.Identifier{indexPublicSchema, parent}.Sanitize()+" ATTACH PARTITION "+qualified)
	return err
}
