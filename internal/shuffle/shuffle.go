// Package shuffle plays random movies and episodes from one scope — a
// library, a series, a season, or a collection — until the profile stops.
//
// A shuffle is a Postgres row holding the item playing now and the item
// picked to play next, plus the items it handed out this cycle. Any API
// process can continue a shuffle another one started, and a client that lost
// its connection resumes from the row. Picks skip items already handed out,
// so nothing repeats until the whole scope has played; then a new cycle
// starts. Every pick re-applies the viewer's current access, so a shuffle
// never reaches an item the profile could not open.
package shuffle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ScopeKind names what a shuffle draws from.
type ScopeKind string

const (
	ScopeLibrary           ScopeKind = "library"
	ScopeSeries            ScopeKind = "series"
	ScopeSeason            ScopeKind = "season"
	ScopeLibraryCollection ScopeKind = "library_collection"
	ScopeUserCollection    ScopeKind = "user_collection"
)

// Scope is what a shuffle draws from: a library ID, a series or season
// content ID, or a collection ID.
type Scope struct {
	Kind ScopeKind
	ID   string
}

// Shuffle is one profile's running shuffle.
type Shuffle struct {
	ID    string
	Scope Scope
	// Title names the scope as it was when the shuffle started; ParentTitle
	// is a season's series title and empty otherwise.
	Title            string
	ParentTitle      string
	CurrentContentID string
	NextContentID    string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Owner is the profile a shuffle belongs to. Only its owner can read or
// advance it.
type Owner struct {
	UserID    int
	ProfileID string
}

var (
	// ErrNotFound: no shuffle with that ID belongs to the owner.
	ErrNotFound = errors.New("shuffle not found")
	// ErrScopeNotFound: the scope does not exist or the viewer cannot see it.
	ErrScopeNotFound = errors.New("shuffle scope not found")
	// ErrUnsupportedScope: the scope holds no kind of media a shuffle plays,
	// such as an audiobook library.
	ErrUnsupportedScope = errors.New("shuffle scope cannot be shuffled")
	// ErrEmpty: nothing in the scope can be played by this viewer.
	ErrEmpty = errors.New("nothing in the shuffle scope can be played")
)

// retentionInterval is how long an untouched shuffle is kept, as a Postgres
// interval. A shuffle advances once per item it plays, so a week covers any
// realistic pause.
const retentionInterval = "7 days"

// CollectionResolver lists a collection's members the viewer may see
// (*catalog.CatalogResolver).
type CollectionResolver interface {
	CollectionMembers(ctx context.Context, source catalog.CatalogSource, collectionID string, access catalog.AccessFilter) (string, []*models.MediaItem, error)
}

// Service creates and advances shuffles.
type Service struct {
	pool        *pgxpool.Pool
	collections CollectionResolver
}

// NewService returns a Service backed by pool. collections may be nil, in
// which case collection scopes are not found.
func NewService(pool *pgxpool.Pool, collections CollectionResolver) *Service {
	return &Service{pool: pool, collections: collections}
}

// Create starts a shuffle over scope and picks the first two items.
func (s *Service) Create(ctx context.Context, owner Owner, access catalog.AccessFilter, scope Scope) (*Shuffle, error) {
	info, err := s.resolveScope(ctx, s.pool, scope, access)
	if err != nil {
		return nil, err
	}
	current, err := pick(ctx, s.pool, info.pool, access, "", nil)
	if err != nil {
		return nil, err
	}
	if current == "" {
		return nil, ErrEmpty
	}
	next, err := pick(ctx, s.pool, info.pool, access, "", []string{current})
	if err != nil {
		return nil, err
	}
	if next == "" {
		// A scope with one playable item plays it again.
		next = current
	}

	shuffle := &Shuffle{
		ID:               uuid.NewString(),
		Scope:            Scope{Kind: scope.Kind, ID: strings.TrimSpace(scope.ID)},
		Title:            info.title,
		ParentTitle:      info.parentTitle,
		CurrentContentID: current,
		NextContentID:    next,
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("starting shuffle transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Abandoned shuffles are cleared as new ones start; the index on
	// updated_at keeps this cheap.
	if _, err := tx.Exec(ctx, `DELETE FROM playback_shuffles WHERE updated_at < now() - $1::interval`, retentionInterval); err != nil {
		return nil, fmt.Errorf("deleting abandoned shuffles: %w", err)
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO playback_shuffles
			(id, user_id, profile_id, scope_kind, scope_id, scope_title, scope_parent_title, current_content_id, next_content_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING created_at, updated_at`,
		shuffle.ID, owner.UserID, owner.ProfileID, string(shuffle.Scope.Kind), shuffle.Scope.ID,
		shuffle.Title, shuffle.ParentTitle, current, next,
	).Scan(&shuffle.CreatedAt, &shuffle.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating shuffle: %w", err)
	}
	if err := markPlayed(ctx, tx, shuffle.ID, current, next); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing shuffle: %w", err)
	}
	return shuffle, nil
}

// Get returns the owner's shuffle. When its announced next item can no
// longer play for this viewer (its file went missing, or the viewer lost
// access), another pick replaces it first, so a client never announces an
// item the shuffle would not play. When nothing in the scope can play any
// more, Get answers ErrEmpty.
func (s *Service) Get(ctx context.Context, owner Owner, access catalog.AccessFilter, id string) (*Shuffle, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	shuffle, err := loadShuffle(ctx, s.pool, owner, id, false)
	if err != nil {
		return nil, err
	}
	info, err := s.resolveScope(ctx, s.pool, shuffle.Scope, access)
	if errors.Is(err, ErrScopeNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ok, err := playable(ctx, s.pool, info.pool, access, shuffle.NextContentID)
	if err != nil || ok {
		return shuffle, err
	}
	return s.Skip(ctx, owner, access, id, shuffle.NextContentID)
}

// Advance moves the shuffle on when its current item is fromContentID: the
// next item becomes current and a new next item is picked. A repeated call
// for an item the shuffle already moved past changes nothing, so a client can
// retry after a lost response.
func (s *Service) Advance(ctx context.Context, owner Owner, access catalog.AccessFilter, id, fromContentID string) (*Shuffle, error) {
	return s.update(ctx, owner, access, id, "", func(shuffle *Shuffle) bool {
		if shuffle.CurrentContentID != fromContentID {
			return false
		}
		shuffle.CurrentContentID = shuffle.NextContentID
		return true
	})
}

// Skip replaces the next item with another pick when the next item is
// skipContentID. The skipped item never played, so it returns to the pool and
// can come up later in the cycle; only the replacement pick avoids it. A
// repeated call for an item already replaced changes nothing.
func (s *Service) Skip(ctx context.Context, owner Owner, access catalog.AccessFilter, id, skipContentID string) (*Shuffle, error) {
	return s.update(ctx, owner, access, id, skipContentID, func(shuffle *Shuffle) bool {
		return shuffle.NextContentID == skipContentID
	})
}

// Delete stops the owner's shuffle. Deleting one that is already gone
// succeeds.
func (s *Service) Delete(ctx context.Context, owner Owner, id string) error {
	if !validID(id) {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM playback_shuffles WHERE id = $1 AND user_id = $2 AND profile_id = $3`,
		id, owner.UserID, owner.ProfileID); err != nil {
		return fmt.Errorf("deleting shuffle: %w", err)
	}
	return nil
}

// update locks the shuffle, lets change decide whether it moves on, and when
// it does picks a new next item. skipped, when set, is the next item being
// replaced: it is handed back to the pool and the new pick avoids it.
func (s *Service) update(ctx context.Context, owner Owner, access catalog.AccessFilter, id, skipped string, change func(*Shuffle) bool) (*Shuffle, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("starting shuffle transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	shuffle, err := loadShuffle(ctx, tx, owner, id, true)
	if err != nil {
		return nil, err
	}
	// Access is checked before anything is answered, a repeated request
	// included: a viewer who lost the scope gets no shuffle back.
	info, err := s.resolveScope(ctx, tx, shuffle.Scope, access)
	if errors.Is(err, ErrScopeNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	previous := shuffle.CurrentContentID
	if !change(shuffle) {
		return shuffle, nil
	}
	if skipped == "" {
		// The item just promoted was picked a whole item ago. If it has since
		// gone, or the viewer lost access to it, play a fresh pick instead so
		// the shuffle never stops on an item nobody can open.
		ok, err := playable(ctx, tx, info.pool, access, shuffle.CurrentContentID)
		if err != nil {
			return nil, err
		}
		if !ok {
			// Avoid the item that just finished too, so a new cycle does not
			// open by replaying it.
			replacement, err := pickNext(ctx, tx, info.pool, access, shuffle.ID, shuffle.CurrentContentID, previous)
			if err != nil {
				return nil, err
			}
			shuffle.CurrentContentID = replacement
		}
	}
	if skipped != "" && skipped != shuffle.CurrentContentID {
		if _, err := tx.Exec(ctx, `DELETE FROM playback_shuffle_items WHERE shuffle_id = $1 AND content_id = $2`, shuffle.ID, skipped); err != nil {
			return nil, fmt.Errorf("returning a skipped shuffle item: %w", err)
		}
	}
	next, err := pickNext(ctx, tx, info.pool, access, shuffle.ID, shuffle.CurrentContentID, skipped)
	if err != nil {
		return nil, err
	}
	shuffle.NextContentID = next
	err = tx.QueryRow(ctx, `
		UPDATE playback_shuffles SET current_content_id = $2, next_content_id = $3, updated_at = now()
		WHERE id = $1
		RETURNING updated_at`,
		shuffle.ID, shuffle.CurrentContentID, shuffle.NextContentID,
	).Scan(&shuffle.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("advancing shuffle: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing shuffle: %w", err)
	}
	return shuffle, nil
}

// pickNext picks the item to play after current and records it as handed
// out. It also avoids alsoAvoid when that is set: the item being skipped, or
// the item that just finished. When nothing unplayed is left it starts a new
// cycle in which every item is eligible again, except that the cycle does not
// open with an avoided item. A scope down to one playable item plays it again.
func pickNext(ctx context.Context, tx pgx.Tx, p pool, access catalog.AccessFilter, shuffleID, current, alsoAvoid string) (string, error) {
	avoid := []string{current}
	if alsoAvoid != "" {
		avoid = append(avoid, alsoAvoid)
	}
	next, err := pick(ctx, tx, p, access, shuffleID, avoid)
	if err != nil {
		return "", err
	}
	if next == "" && alsoAvoid != "" {
		// When a skipped item is the only one this cycle has not played, it
		// stays next: nothing repeats until it has played. (An item that just
		// finished has played, so this never brings it back.)
		if next, err = pick(ctx, tx, p, access, shuffleID, []string{current}); err != nil {
			return "", err
		}
	}
	if next == "" {
		if _, err := tx.Exec(ctx, `DELETE FROM playback_shuffle_items WHERE shuffle_id = $1`, shuffleID); err != nil {
			return "", fmt.Errorf("starting a new shuffle cycle: %w", err)
		}
		if next, err = pick(ctx, tx, p, access, shuffleID, avoid); err != nil {
			return "", err
		}
	}
	// A scope of two plays the avoided item rather than repeating current;
	// a scope of one repeats its only item.
	for _, fallback := range [][]string{{current}, nil} {
		if next != "" {
			break
		}
		if next, err = pick(ctx, tx, p, access, "", fallback); err != nil {
			return "", err
		}
	}
	if next == "" {
		return "", ErrEmpty
	}
	if err := markPlayed(ctx, tx, shuffleID, next); err != nil {
		return "", err
	}
	return next, nil
}

// validID reports whether id can name a shuffle. Any other value names none,
// and is answered as a missing shuffle rather than a database error.
func validID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}

func markPlayed(ctx context.Context, tx pgx.Tx, shuffleID string, contentIDs ...string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO playback_shuffle_items (shuffle_id, content_id)
		SELECT $1, content_id FROM unnest($2::text[]) AS content_id
		ON CONFLICT DO NOTHING`, shuffleID, contentIDs); err != nil {
		return fmt.Errorf("recording shuffle items: %w", err)
	}
	return nil
}

func loadShuffle(ctx context.Context, q querier, owner Owner, id string, lock bool) (*Shuffle, error) {
	sql := `
		SELECT id::text, scope_kind, scope_id, scope_title, scope_parent_title, current_content_id, next_content_id, created_at, updated_at
		FROM playback_shuffles
		WHERE id = $1 AND user_id = $2 AND profile_id = $3`
	if lock {
		sql += ` FOR UPDATE`
	}
	var shuffle Shuffle
	var kind string
	err := q.QueryRow(ctx, sql, id, owner.UserID, owner.ProfileID).Scan(
		&shuffle.ID, &kind, &shuffle.Scope.ID, &shuffle.Title, &shuffle.ParentTitle,
		&shuffle.CurrentContentID, &shuffle.NextContentID, &shuffle.CreatedAt, &shuffle.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("loading shuffle: %w", err)
	}
	shuffle.Scope.Kind = ScopeKind(kind)
	return &shuffle, nil
}
