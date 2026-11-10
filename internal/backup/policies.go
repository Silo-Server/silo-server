package backup

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CarryPolicies gives the restored database (dbURL) the live server's
// (liveURL's) custom access policies as one unit: its documents, their
// versions, and which version each runs. A custom policy only tightens
// decisions, so a newer one lost to the backup would widen access. When
// the live server has none, or liveURL is "" (another deployment, or a
// separate restore), the backup's documents come back turned off, and are
// reported. It returns the documents it turned off.
func (l PGLoader) CarryPolicies(ctx context.Context, dbURL, liveURL string) ([]string, error) {
	var documents, versions string
	if liveURL != "" {
		live, err := pgx.Connect(ctx, liveURL)
		if err != nil {
			return nil, fmt.Errorf("reading the live server's policies: %w", err)
		}
		defer func() { _ = live.Close(context.WithoutCancel(ctx)) }()
		err = pgx.BeginTxFunc(ctx, live, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT coalesce(json_agg(d), '[]')::text FROM policy_documents d`).Scan(&documents); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT coalesce(json_agg(v), '[]')::text FROM policy_document_versions v`).Scan(&versions)
		})
		if err != nil {
			return nil, fmt.Errorf("reading the live server's policies: %w", err)
		}
	}
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var off []string
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if documents == "" || documents == "[]" {
			var err error
			off, err = collectStrings(ctx, tx, `UPDATE policy_documents SET enabled = false WHERE enabled RETURNING format('%s policy %L', domain, name)`)
			return err
		}
		// The live set replaces the backup's whole: documents first without
		// their running version, then versions (the authors the restored
		// data lacks dropped), then which version each runs. Their ids are
		// GENERATED ALWAYS identities, so the live ids override them.
		for _, step := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM policy_documents`, nil},
			{`INSERT INTO policy_documents (id, domain, name, enabled, active_version_id, created_at, updated_at, revision) OVERRIDING SYSTEM VALUE
				SELECT r.id, r.domain, r.name, r.enabled, NULL, r.created_at, r.updated_at, r.revision FROM json_populate_recordset(NULL::policy_documents, $1::json) r`,
				[]any{documents}},
			{`INSERT INTO policy_document_versions (id, document_id, version_number, rego_source, source_sha256, compiled_ok, compile_error, created_by_user_id, comment, created_at)
				OVERRIDING SYSTEM VALUE
				SELECT r.id, r.document_id, r.version_number, r.rego_source, r.source_sha256, r.compiled_ok, r.compile_error,
					(SELECT u.id FROM users u WHERE u.id = r.created_by_user_id), r.comment, r.created_at
				FROM json_populate_recordset(NULL::policy_document_versions, $1::json) r`, []any{versions}},
			{`UPDATE policy_documents d SET active_version_id = r.active_version_id
				FROM json_populate_recordset(NULL::policy_documents, $1::json) r WHERE d.id = r.id`, []any{documents}},
		} {
			if _, err := tx.Exec(ctx, step.sql, step.args...); err != nil {
				return err
			}
		}
		for _, table := range []string{"policy_documents", "policy_document_versions"} {
			if _, err := tx.Exec(ctx, fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', 'id'), GREATEST((SELECT coalesce(max(id), 0) FROM %s), 1))`, table, table)); err != nil {
				return err
			}
		}
		return nil
	})
	return off, err
}

// carryPolicies runs CarryPolicies and says what it turned off.
func carryPolicies(ctx context.Context, loader DatabaseLoader, dbURL, liveURL string, out io.Writer) error {
	off, err := loader.CarryPolicies(ctx, dbURL, liveURL)
	if err != nil {
		return fmt.Errorf("keeping the live server's access policies: %w", err)
	}
	if len(off) > 0 {
		_, _ = fmt.Fprintf(out, "Turned off %d custom access policies this server does not run; review and enable them after Silo starts: %s.\n", len(off), strings.Join(off, ", "))
	}
	return nil
}
