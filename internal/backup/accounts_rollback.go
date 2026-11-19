package backup

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// A full restore is a rollback of the accounts: every account, role, and
// password goes back to the backup's, an admin demoted or a password
// changed since included. That is deliberate (a restore that kept today's
// accounts beside yesterday's data would be neither), so the restore says
// so before the swap and names the accounts it rolls back.

// AccountRollback is how the restored accounts differ from the live ones,
// by account id, on the deployment the backup came from. Each list holds
// the live server's usernames (the backup's for Returned), sorted.
type AccountRollback struct {
	// Authority lists accounts whose role, owner flag, or break-glass flag
	// the restore changes.
	Authority []string
	// Password lists accounts whose password the restore changes.
	Password []string
	// Lost lists accounts the backup does not have: created since.
	Lost []string
	// Returned lists accounts only the backup has: deleted since.
	Returned []string
	// LiveChangedAt is when an account here last changed.
	LiveChangedAt time.Time
}

// changed reports whether the rollback changes any account.
func (r AccountRollback) changed() bool {
	return len(r.Authority)+len(r.Password)+len(r.Lost)+len(r.Returned) > 0
}

// accountRow is what AccountRollback compares of one account.
type accountRow struct {
	name, role, passwordHash string
	owner, breakGlass        bool
}

// AccountRollback compares dbURL's accounts with liveURL's.
func (l PGLoader) AccountRollback(ctx context.Context, dbURL, liveURL string) (AccountRollback, error) {
	var out AccountRollback
	live, err := readAccounts(ctx, liveURL, &out.LiveChangedAt)
	if err != nil {
		return out, fmt.Errorf("reading the live server's accounts: %w", err)
	}
	restored, err := readAccounts(ctx, dbURL, nil)
	if err != nil {
		return out, fmt.Errorf("reading the restored accounts: %w", err)
	}
	for id, now := range live {
		then, ok := restored[id]
		if !ok {
			out.Lost = append(out.Lost, now.name)
			continue
		}
		if then.role != now.role || then.owner != now.owner || then.breakGlass != now.breakGlass {
			out.Authority = append(out.Authority, now.name)
		}
		if then.passwordHash != now.passwordHash {
			out.Password = append(out.Password, now.name)
		}
	}
	for id, then := range restored {
		if _, ok := live[id]; !ok {
			out.Returned = append(out.Returned, then.name)
		}
	}
	for _, list := range []*[]string{&out.Authority, &out.Password, &out.Lost, &out.Returned} {
		slices.Sort(*list)
	}
	return out, nil
}

// readAccounts reads the accounts of the database at url by id, and when
// changedAt is set, when one last changed.
func readAccounts(ctx context.Context, url string, changedAt *time.Time) (map[int64]accountRow, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	rows, err := conn.Query(ctx, `SELECT id, COALESCE(username::text, email::text, id::text), role,
		COALESCE(password_hash, ''), is_owner, break_glass, updated_at FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]accountRow{}
	for rows.Next() {
		var (
			id      int64
			row     accountRow
			updated time.Time
		)
		if err := rows.Scan(&id, &row.name, &row.role, &row.passwordHash, &row.owner, &row.breakGlass, &updated); err != nil {
			return nil, err
		}
		if changedAt != nil && updated.After(*changedAt) {
			*changedAt = updated
		}
		out[id] = row
	}
	return out, rows.Err()
}

// maxListedAccounts bounds the names one line of the rollback notice lists.
const maxListedAccounts = 20

// reportAccountRollback tells the operator, before the swap, that the
// accounts go back to the backup's: always for another deployment's backup,
// whose accounts are other people, and on this deployment's when an
// account changed since the backup, naming the accounts that change.
func reportAccountRollback(ctx context.Context, loader DatabaseLoader, restoredURL, liveURL string, taken time.Time, sameDeployment bool, out io.Writer) error {
	notice := fmt.Sprintf("Accounts, roles, and passwords go back to %s, when the backup was taken; review the admin accounts after the restore, and reset the password of anyone whose password changed since.\n",
		taken.UTC().Format(time.RFC3339))
	if !sameDeployment {
		_, _ = io.WriteString(out, notice)
		return nil
	}
	r, err := loader.AccountRollback(ctx, restoredURL, liveURL)
	if err != nil {
		return err
	}
	if !r.changed() && !r.LiveChangedAt.After(taken) {
		return nil
	}
	_, _ = io.WriteString(out, notice)
	for _, line := range []struct {
		what  string
		names []string
	}{
		{"get the role or owner status the backup has", r.Authority},
		{"get the password the backup has", r.Password},
		{"were created since the backup and are removed", r.Lost},
		{"were deleted since the backup and come back", r.Returned},
	} {
		if len(line.names) > 0 {
			_, _ = fmt.Fprintf(out, "  %d accounts %s: %s\n", len(line.names), line.what, listNames(line.names))
		}
	}
	return nil
}

// listNames joins names, the first maxListedAccounts of them.
func listNames(names []string) string {
	if len(names) <= maxListedAccounts {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:maxListedAccounts], ", ") + fmt.Sprintf(", and %d more", len(names)-maxListedAccounts)
}
