package pgstore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TitleAllowances keeps every account's title and collection allowances in
// the shared tables. The access scope reads the result through
// userstore.Profile.AllowedTitleIDs. Every write that changes a row also
// advances the account's access_policy_revision in the same transaction.
//
// It is its own store rather than optional methods on PostgresUserStore so a
// decorated user store provider cannot hide it.
type TitleAllowances struct {
	pool *pgxpool.Pool
}

// NewTitleAllowances returns the allowance store over pool, which must hold
// the accounts' profiles (PostgresProvider.StoresAccountsIn).
func NewTitleAllowances(pool *pgxpool.Pool) *TitleAllowances {
	return &TitleAllowances{pool: pool}
}

func (s *TitleAllowances) ListTitleAllowances(ctx context.Context, userID int, profileID string) ([]userstore.TitleAllowance, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT content_id, created_at FROM profile_title_allowances
		WHERE user_id = $1 AND profile_id = $2
		ORDER BY created_at DESC, content_id DESC`, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("listing title allowances: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (userstore.TitleAllowance, error) {
		var a userstore.TitleAllowance
		var createdAt time.Time
		if err := row.Scan(&a.ContentID, &createdAt); err != nil {
			return a, err
		}
		a.CreatedAt = timeToString(createdAt)
		return a, nil
	})
}

func (s *TitleAllowances) SetTitleAllowance(ctx context.Context, m userstore.AllowanceManager, profileID, contentID string, allowed bool) (bool, error) {
	if allowed {
		return s.changeAllowances(ctx, m, toggleAudit(profileID, auditFieldAllowedTitle, contentID, true), `
			INSERT INTO profile_title_allowances (user_id, profile_id, content_id)
			VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, m.UserID, profileID, contentID)
	}
	return s.changeAllowances(ctx, m, toggleAudit(profileID, auditFieldAllowedTitle, contentID, false), `
		DELETE FROM profile_title_allowances
		WHERE user_id = $1 AND profile_id = $2 AND content_id = $3`, m.UserID, profileID, contentID)
}

func (s *TitleAllowances) ListCollectionAllowances(ctx context.Context, userID int, profileID string) ([]userstore.CollectionAllowance, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.name,
		       (SELECT count(*) FROM user_personal_collection_items ci
		        WHERE ci.user_id = c.user_id AND ci.collection_id = c.id),
		       c.collection_type = 'manual' AND COALESCE(creator.is_primary, false),
		       ca.created_at
		FROM profile_collection_allowances ca
		JOIN user_personal_collections c ON c.user_id = ca.user_id AND c.id = ca.collection_id
		LEFT JOIN user_profiles creator ON creator.user_id = c.user_id AND creator.id = c.creator_profile_id
		WHERE ca.user_id = $1 AND ca.profile_id = $2
		ORDER BY ca.created_at DESC, c.id DESC`, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("listing collection allowances: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (userstore.CollectionAllowance, error) {
		var a userstore.CollectionAllowance
		var createdAt time.Time
		if err := row.Scan(&a.CollectionID, &a.Name, &a.ItemCount, &a.Live, &createdAt); err != nil {
			return a, err
		}
		a.CreatedAt = timeToString(createdAt)
		return a, nil
	})
}

func (s *TitleAllowances) SetCollectionAllowance(ctx context.Context, m userstore.AllowanceManager, profileID, collectionID string, allowed bool) (bool, error) {
	if allowed {
		return s.changeAllowances(ctx, m, toggleAudit(profileID, auditFieldAllowedCollection, collectionID, true), `
			INSERT INTO profile_collection_allowances (user_id, profile_id, collection_id)
			VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, m.UserID, profileID, collectionID)
	}
	return s.changeAllowances(ctx, m, toggleAudit(profileID, auditFieldAllowedCollection, collectionID, false), `
		DELETE FROM profile_collection_allowances
		WHERE user_id = $1 AND profile_id = $2 AND collection_id = $3`, m.UserID, profileID, collectionID)
}

// UnallowedCollectionTitles lists one page, in the collection's order and
// after the given position, of the movies and series a personal collection
// stores that the profile is not yet allowed, so a repeated copy continues
// where the last one stopped. A zero position starts at the beginning. Smart
// collections store none; their items come from their query.
func (s *TitleAllowances) UnallowedCollectionTitles(ctx context.Context, userID int, profileID, collectionID string, after userstore.CollectionPosition, limit int) ([]userstore.CollectionPosition, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(ci.position, 0), ci.media_item_id FROM user_personal_collection_items ci
		JOIN media_items mi ON mi.content_id = ci.media_item_id AND mi.type = ANY($4)
		WHERE ci.user_id = $1 AND ci.collection_id = $2 AND ci.sub_item_id = ''
		  AND (COALESCE(ci.position, 0), ci.media_item_id) > ($5, $6)
		  AND NOT EXISTS (SELECT 1 FROM profile_title_allowances ta
		      WHERE ta.user_id = ci.user_id AND ta.profile_id = $3 AND ta.content_id = ci.media_item_id)
		ORDER BY COALESCE(ci.position, 0), ci.media_item_id
		LIMIT $7`, userID, collectionID, profileID, userstore.AllowanceTitleTypes, after.Position, after.MediaItemID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing collection titles: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (userstore.CollectionPosition, error) {
		var p userstore.CollectionPosition
		err := row.Scan(&p.Position, &p.MediaItemID)
		return p, err
	})
}

// AllowTitles adds a title allowance for every movie and series among
// contentIDs and returns how many were new.
func (s *TitleAllowances) AllowTitles(ctx context.Context, m userstore.AllowanceManager, profileID string, contentIDs []string) (int, error) {
	if len(contentIDs) == 0 {
		return 0, nil
	}
	changed, err := s.changeAllowancesCount(ctx, m, copyAudit(profileID), `
		INSERT INTO profile_title_allowances (user_id, profile_id, content_id)
		SELECT $1, $2, mi.content_id
		FROM media_items mi
		WHERE mi.content_id = ANY($3) AND mi.type = ANY($4)
		ON CONFLICT DO NOTHING`, m.UserID, profileID, contentIDs, userstore.AllowanceTitleTypes)
	return int(changed), err
}

// Audit fields of an allowance change, recorded as a profile.updated action on
// the profile the allowance is for: an allowance changes what it may see, like
// its rating limit does.
const (
	auditFieldAllowedTitle      = "allowed_title"
	auditFieldAllowedCollection = "allowed_collection"
	auditFieldAllowedTitlesAdd  = "allowed_titles_added"
)

// allowanceAudit describes the audit row of one allowance write: the profile
// it changes, the response status, and the changes given the rows it wrote.
type allowanceAudit struct {
	profileID string
	status    int
	changes   func(rows int64) []auditmutation.Change
}

func toggleAudit(profileID, field, id string, allowed bool) allowanceAudit {
	return allowanceAudit{profileID: profileID, status: http.StatusNoContent, changes: func(int64) []auditmutation.Change {
		if allowed {
			return auditmutation.Created(field, id)
		}
		return auditmutation.Removed(field, id)
	}}
}

func copyAudit(profileID string) allowanceAudit {
	return allowanceAudit{profileID: profileID, status: http.StatusOK, changes: func(rows int64) []auditmutation.Change {
		return auditmutation.Created(auditFieldAllowedTitlesAdd, rows)
	}}
}

func (s *TitleAllowances) changeAllowances(ctx context.Context, m userstore.AllowanceManager, audit allowanceAudit, query string, args ...any) (bool, error) {
	n, err := s.changeAllowancesCount(ctx, m, audit, query, args...)
	return n > 0, err
}

// changeAllowancesCount runs one allowance write and, when it changed a row,
// advances the account's access_policy_revision and records its audit row in
// the same transaction, the way every other change to what a profile may see
// does. The audit row is written only on a request that records domain
// actions (an Audited v2 operation). It first locks the account, then the
// acting manager's profile, and refuses when that PIN changed since
// authorization.
func (s *TitleAllowances) changeAllowancesCount(ctx context.Context, m userstore.AllowanceManager, audit allowanceAudit, query string, args ...any) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning allowance change: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Account rows before the profile row, as account deletion and audited
	// profile writes take them; the reverse order could deadlock with them.
	if err := auditmutation.LockActors(ctx, tx); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, "SELECT 1 FROM users WHERE id = $1 FOR NO KEY UPDATE", m.UserID); err != nil {
		return 0, fmt.Errorf("locking the account: %w", err)
	}
	if err := checkAllowanceManager(ctx, tx, m); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("changing allowances: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET access_policy_revision = access_policy_revision + 1 WHERE id = $1", m.UserID); err != nil {
		return 0, fmt.Errorf("advancing access policy revision: %w", err)
	}
	entry, err := auditmutation.RecordMutation(ctx, tx, auditmutation.ActionProfileUpdated, auditmutation.TargetProfile,
		audit.profileID, audit.status, audit.changes(tag.RowsAffected()))
	if err != nil {
		return 0, fmt.Errorf("recording allowance change: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing allowance change: %w", err)
	}
	auditmutation.CommitMutation(ctx, entry)
	return tag.RowsAffected(), nil
}

// checkAllowanceManager locks the acting manager's profile and refuses with
// userstore.ErrAllowanceManagerChanged when it is gone or its PIN changed
// since authorization. An account-level manager (no profile) passes.
func checkAllowanceManager(ctx context.Context, tx pgx.Tx, m userstore.AllowanceManager) error {
	if m.ProfileID == "" {
		return nil
	}
	var current bool
	err := tx.QueryRow(ctx, "SELECT pin_revision = $3 FROM user_profiles WHERE user_id = $1 AND id = $2 FOR UPDATE",
		m.UserID, m.ProfileID, m.PINRevision).Scan(&current)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return userstore.ErrAllowanceManagerChanged
	case err != nil:
		return fmt.Errorf("checking the allowance manager: %w", err)
	case !current:
		return userstore.ErrAllowanceManagerChanged
	}
	return nil
}
