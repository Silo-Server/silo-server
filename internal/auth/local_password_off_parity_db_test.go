package auth

import (
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// mainLocalPasswordOffSQL is listAdminLocalPasswordOffAccounts' query as main
// shipped it, before app keys took part: the reference a server with no key
// rows must still match. Do not change it to follow localPasswordOffSQL.
func mainLocalPasswordOffSQL() string {
	return `WITH affected AS (
			SELECT u.id, COALESCE(u.username::text, '') AS username, COALESCE(u.email, '')::text AS email,
				COALESCE(u.role, '') AS role, COALESCE(u.is_owner, false) AS is_owner,
				CASE
					WHEN ` + emailMatchCandidateSQL("u") + ` AND btrim(COALESCE(u.email, '')) <> '' THEN
						CASE WHEN $1 THEN 'email_match' ELSE 'email_match_off' END
					WHEN ` + emailMatchCandidateSQL("u") + ` THEN 'no_email'
					ELSE 'administrator'
				END AS reason
			FROM users u
			WHERE u.enabled AND ` + passwordSignsInSQL("u") + ` AND NOT u.break_glass
				AND NOT EXISTS (SELECT 1 FROM plugin_auth_identities i WHERE i.user_id = u.id AND i.plugin_installation_id = ANY($6))),
		totals AS (
			SELECT COUNT(*) FILTER (WHERE reason = 'email_match') AS matched,
				COUNT(*) FILTER (WHERE reason <> 'email_match') AS unmatched
			FROM affected)
		SELECT t.matched, t.unmatched, a.id, a.username, a.email, a.role, a.is_owner, a.reason
		FROM totals t
		LEFT JOIN LATERAL (
			SELECT * FROM affected
			WHERE (reason = 'email_match') = $2 AND (username, id) > ($3::text, $4::int)
			ORDER BY username, id
			LIMIT $5) a ON true
		ORDER BY a.username, a.id`
}

// On a database with no key rows, listAdminLocalPasswordOffAccounts answers
// exactly what main's query answered: the same accounts in each group, in
// the same order, with the same reasons and totals, and nobody blocking.
// Both queries run in one transaction that deletes every key row first and
// rolls back. Accounts of every kind main's list distinguishes are seeded,
// with and without email matching.
func TestLocalPasswordOffMatchesMainWithoutKeysDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	t.Cleanup(saveSettings(t, env.pool, config.ServerPublicURLSettingKey))
	directory := env.riskProvider(t, "parity-directory", `["password"]`)
	network := env.riskProvider(t, "parity-network", `["network"]`)
	for i, seed := range []struct {
		role         string
		pw, bg       bool
		noEmail      bool
		installation int
	}{
		{role: models.RoleUser, pw: true},
		{role: models.RoleUser, pw: true, noEmail: true},
		{role: models.RoleAdmin, pw: true},
		{role: models.RoleAdmin, pw: true, bg: true},
		{role: models.RoleUser},
		{role: models.RoleUser, pw: true, installation: directory},
		{role: models.RoleUser, pw: true, installation: network},
	} {
		id := env.riskAccount(t, fmt.Sprintf("parity-%d", i), seed.role, seed.pw, seed.bg)
		if seed.noEmail {
			if _, err := env.pool.Exec(ctx, `UPDATE users SET email = NULL WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
		}
		if seed.installation != 0 {
			env.riskIdentity(t, id, seed.installation, fmt.Sprintf("parity-identity-%d", i))
		}
	}
	providers, err := signInProviders(ctx, env.pool, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, matching := range []bool{true, false} {
		t.Run(fmt.Sprintf("email matching %v", matching), func(t *testing.T) {
			tx, err := env.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { rollbackWithin(tx) })
			if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '10s'`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM auth_device_keys; DELETE FROM auth_person_keys`); err != nil {
				t.Fatal(err)
			}
			for _, group := range []string{LocalPasswordOffEmailMatch, LocalPasswordOffAdminLink} {
				before := readLocalPasswordOff(t, tx, mainLocalPasswordOffSQL(), group == LocalPasswordOffEmailMatch, matching, false, providers)
				after := readLocalPasswordOff(t, tx, localPasswordOffSQL(), group, matching, true, providers)
				for i := range after {
					if after[i].BlockingIfAnswered != 0 {
						t.Fatalf("%s: %d accounts block with no key rows", group, after[i].BlockingIfAnswered)
					}
					after[i].BlockingIfAnswered = 0
				}
				if !slices.Equal(before, after) {
					t.Fatalf("%s: main answered %+v, now %+v", group, before, after)
				}
			}
		})
	}
}

// localPasswordOffRow is one row of a local-password-off query: the totals
// it carries and the account, if any.
type localPasswordOffRow struct {
	ID                    int
	Username, Email, Role string
	IsOwner               bool
	Reason                string
	EmailMatch, AdminLink int
	BlockingIfAnswered    int
}

// readLocalPasswordOff runs query (main's, or localPasswordOffSQL when
// withBlocking says it answers the blocking total too) for group, in tx.
func readLocalPasswordOff(t *testing.T, tx pgx.Tx, query string, group any, matching, withBlocking bool, providers []int) []localPasswordOffRow {
	t.Helper()
	rows, err := tx.Query(t.Context(), query, matching, group, "", 0, 100000, providers)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []localPasswordOffRow
	for rows.Next() {
		var (
			r                             localPasswordOffRow
			id                            *int
			username, email, role, reason *string
			owner                         *bool
		)
		dest := []any{&r.EmailMatch, &r.AdminLink}
		if withBlocking {
			dest = append(dest, &r.BlockingIfAnswered)
		}
		dest = append(dest, &id, &username, &email, &role, &owner, &reason)
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		if id != nil {
			r.ID, r.Username, r.Email, r.Role, r.IsOwner, r.Reason = *id, *username, *email, *role, *owner, *reason
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
