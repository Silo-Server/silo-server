package progresssync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

var (
	ErrUnsupported     = errors.New("progress bootstrap unsupported")
	ErrNotConfigured   = errors.New("progress bootstrap not configured")
	ErrUnauthenticated = errors.New("progress bootstrap authentication expired")
)

// Actor carries the exact authenticated request identity. Recheck must revalidate
// the credential, account, PIN and current PDP outside the storage transaction.
// It is mandatory even for capability reads and must never log credential values.
type Actor struct {
	Input   access.ResolveInput
	Recheck func(context.Context) (access.Scope, error)
}

type Service struct {
	pool     *pgxpool.Pool
	provider userstore.UserStoreProvider
	settings diagnostics.SettingsStore
	resolver *policy.ViewerResolver
}

func NewService(pool *pgxpool.Pool, provider userstore.UserStoreProvider, settings diagnostics.SettingsStore, resolver *policy.ViewerResolver) *Service {
	return &Service{pool: pool, provider: provider, settings: settings, resolver: resolver}
}

func scopeDigest(scope access.Scope) string {
	scope.AllowedLibraryIDs = slices.Clone(scope.AllowedLibraryIDs)
	slices.Sort(scope.AllowedLibraryIDs)
	scope.DisabledLibraryIDs = slices.Clone(scope.DisabledLibraryIDs)
	slices.Sort(scope.DisabledLibraryIDs)
	// Includes PDP outputs as well as policy revision; custom policy changes need
	// not increment the account revision to invalidate a changed decision.
	raw, _ := json.Marshal(scope)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func checkActor(ctx context.Context, actor Actor) (access.Scope, error) {
	if actor.Recheck == nil || actor.Input.UserID <= 0 || actor.Input.ProfileID == "" {
		return access.Scope{}, ErrUnauthenticated
	}
	scope, err := actor.Recheck(ctx)
	if err != nil {
		return access.Scope{}, err
	}
	if scope.UserID != actor.Input.UserID || scope.ProfileID != actor.Input.ProfileID || !scope.ProfileVerified {
		return access.Scope{}, ErrUnauthenticated
	}
	return scope, nil
}
func (s *Service) repository(ctx context.Context, actor Actor) (*Repository, string, error) {
	before, err := checkActor(ctx, actor)
	if err != nil {
		return nil, "", err
	}
	if s == nil || s.provider == nil {
		return nil, "", ErrNotConfigured
	}
	store, err := s.provider.ForUser(ctx, actor.Input.UserID)
	if err != nil {
		return nil, "", err
	}
	source, ok := store.(userstore.ProgressSnapshotSource)
	if !ok {
		return nil, "", ErrUnsupported
	}
	pool, userID := source.ProgressSnapshotDatabase()
	if pool == nil {
		return nil, "", ErrUnsupported
	}
	if pool != s.pool || userID != actor.Input.UserID || s.resolver == nil || s.settings == nil {
		return nil, "", ErrNotConfigured
	}
	installation, err := diagnostics.ServerInstanceID(ctx, s.settings)
	if err != nil {
		return nil, "", err
	}
	digest := scopeDigest(before)
	r, err := New(pool, installation, func(ctx context.Context, tx pgx.Tx, id Identity, ids []string) (string, map[string]bool, error) {
		scope, err := s.resolveSnapshot(ctx, tx, actor.Input)
		if err != nil {
			return "", nil, err
		}
		current := scopeDigest(scope)
		if current != digest {
			return "", nil, ErrResetRequired
		}
		visible, err := catalog.FilterAccessibleContentIDsInTransaction(ctx, tx, ids, scope.AllowedLibraryIDs, scope.DisabledLibraryIDs, scope.MaturityLimits)
		return current, visible, err
	})
	return r, digest, err
}
func (s *Service) resolveSnapshot(ctx context.Context, tx pgx.Tx, input access.ResolveInput) (access.Scope, error) {
	user, err := auth.UserInTransaction(ctx, tx, input.UserID)
	if err != nil {
		return access.Scope{}, err
	}
	if !user.Enabled {
		return access.Scope{}, ErrUnauthenticated
	}
	profile, err := pgstore.ProfileInTransaction(ctx, tx, input.UserID, input.ProfileID)
	if err != nil {
		return access.Scope{}, err
	}
	var group *access.GroupPolicy
	if access.GroupApplies(user) {
		group, err = access.GroupPolicyInTransaction(ctx, tx, input.UserID)
		if err != nil {
			return access.Scope{}, err
		}
	}
	preferences, err := access.ResolveViewerPreferencesStrict(ctx, pgstore.NewViewerSnapshotReader(tx, input.UserID), input.ProfileID)
	if err != nil {
		return access.Scope{}, err
	}
	return s.resolver.ResolveFacts(ctx, input, user, profile, access.ApplyGroupPolicy(user, group), preferences)
}
func (s *Service) finishActor(ctx context.Context, actor Actor, digest, generation string, items []Entry) error {
	scope, err := checkActor(ctx, actor)
	if err != nil {
		return err
	}
	if scopeDigest(scope) != digest {
		return ErrResetRequired
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var currentGeneration string
	if err = tx.QueryRow(ctx, `SELECT generation::text FROM user_progress_sync_state WHERE user_id=$1 FOR SHARE`, actor.Input.UserID).Scan(&currentGeneration); err != nil {
		return err
	}
	if currentGeneration != generation {
		return ErrResetRequired
	}
	current, err := s.resolveSnapshot(ctx, tx, actor.Input)
	if err != nil {
		return err
	}
	if scopeDigest(current) != digest {
		return ErrResetRequired
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.MediaItemID)
	}
	visible, err := catalog.FilterAccessibleContentIDsInTransaction(ctx, tx, ids, current.AllowedLibraryIDs, current.DisabledLibraryIDs, current.MaturityLimits)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !visible[id] {
			return ErrResetRequired
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	current, err = checkActor(ctx, actor)
	if err != nil {
		return err
	}
	if scopeDigest(current) != digest {
		return ErrResetRequired
	}
	return nil
}
func (s *Service) CreateSnapshot(ctx context.Context, actor Actor, requestID string, limit int) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, AdmissionTimeout)
	defer cancel()
	r, digest, err := s.repository(ctx, actor)
	if err != nil {
		return Page{}, err
	}
	page, err := r.Create(ctx, Identity{actor.Input.UserID, actor.Input.ProfileID}, requestID, limit)
	if err != nil {
		return Page{}, err
	}
	if err = s.finishActor(ctx, actor, digest, page.Snapshot.Generation, page.Items); err != nil {
		return Page{}, err
	}
	return page, nil
}
func (s *Service) ReadSnapshot(ctx context.Context, actor Actor, pos Position) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, AdmissionTimeout)
	defer cancel()
	r, digest, err := s.repository(ctx, actor)
	if err != nil {
		return Page{}, err
	}
	page, err := r.Read(ctx, Identity{actor.Input.UserID, actor.Input.ProfileID}, pos)
	if err != nil {
		return Page{}, err
	}
	if err = s.finishActor(ctx, actor, digest, page.Snapshot.Generation, page.Items); err != nil {
		return Page{}, err
	}
	return page, nil
}

type Support struct{ InstallationID, Generation string }

func (s *Service) Capabilities(ctx context.Context, actor Actor) (Support, error) {
	ctx, cancel := context.WithTimeout(ctx, AdmissionTimeout)
	defer cancel()
	for {
		result, err := s.capabilities(ctx, actor)
		if pgerr, ok := errors.AsType[*pgconn.PgError](err); ok && pgerr.Code == "42P01" {
			return Support{}, ErrNotConfigured
		}
		if pgerr, ok := errors.AsType[*pgconn.PgError](err); ok && pgerr.Code == "40001" && ctx.Err() == nil {
			continue
		}
		return result, err
	}
}
func (s *Service) capabilities(ctx context.Context, actor Actor) (Support, error) {
	r, digest, err := s.repository(ctx, actor)
	if err != nil {
		return Support{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, AdmissionTimeout)
	defer cancel()
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Support{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, _, err = r.visibility(ctx, tx, Identity{actor.Input.UserID, actor.Input.ProfileID}, nil); err != nil {
		return Support{}, err
	}
	// Capability discovery lazily creates identity only; it admits no snapshot.
	var generation string
	err = tx.QueryRow(ctx, `INSERT INTO user_progress_sync_state(user_id,generation) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET user_id=excluded.user_id RETURNING generation::text`, actor.Input.UserID, uuid.NewString()).Scan(&generation)
	if err != nil {
		return Support{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Support{}, err
	}
	if err = s.finishActor(ctx, actor, digest, generation, nil); err != nil {
		return Support{}, err
	}
	return Support{r.installation, generation}, nil
}

// RunCleanup is owned by the application's cancellable lifecycle. Each pass is
// bounded independently and errors are reported without source or account data.
func (s *Service) RunCleanup(ctx context.Context, onError func(error)) {
	if ctx == nil || s == nil || s.pool == nil {
		return
	}
	cleanup := func() {
		pass, cancel := context.WithTimeout(ctx, AdmissionTimeout)
		defer cancel()
		err := (&Repository{pool: s.pool}).Cleanup(pass)
		if err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
	}
	cleanup()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

// CheckSnapshotVisibility enforces resource hiding before transport cursor parsing.
// Expired owned metadata remains visible for its reset-required response.
func (s *Service) CheckSnapshotVisibility(ctx context.Context, actor Actor, snapshotID string) error {
	ctx, cancel := context.WithTimeout(ctx, AdmissionTimeout)
	defer cancel()
	r, _, err := s.repository(ctx, actor)
	if err != nil {
		return err
	}
	var exists bool
	err = r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM progress_bootstrap_snapshots WHERE id=$1 AND user_id=$2 AND profile_id=$3)`, snapshotID, actor.Input.UserID, actor.Input.ProfileID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	_, err = checkActor(ctx, actor)
	return err
}
