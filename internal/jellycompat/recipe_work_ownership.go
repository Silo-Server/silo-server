package jellycompat

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/noderecipe"
)

type recipeWorkGate struct {
	held chan struct{}
	refs int
}
type recipeOwnerKey struct{}
type recipeWorkOwner struct {
	store  any
	work   string
	active atomic.Bool
	locked *recipeLockedConnection
}

func recipeOwner(ctx context.Context, store any, work string) *recipeWorkOwner {
	owner, _ := ctx.Value(recipeOwnerKey{}).(*recipeWorkOwner)
	if owner != nil && owner.store == store && owner.work == work && owner.active.Load() {
		return owner
	}
	return nil
}
func rejectedRecipeOwner(ctx context.Context, store any, work string) bool {
	owner, _ := ctx.Value(recipeOwnerKey{}).(*recipeWorkOwner)
	return owner != nil && (owner.store != store || owner.work != work || !owner.active.Load())
}
func (s *PlaybackSessionStore) lockRecipeWork(ctx context.Context, work string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.recipeWorkMu.Lock()
	if s.recipeWorks == nil {
		s.recipeWorks = make(map[string]*recipeWorkGate)
	}
	gate := s.recipeWorks[work]
	if gate == nil {
		gate = &recipeWorkGate{held: make(chan struct{}, 1)}
		s.recipeWorks[work] = gate
	}
	gate.refs++
	s.recipeWorkMu.Unlock()
	unref := func() {
		s.recipeWorkMu.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(s.recipeWorks, work)
		}
		s.recipeWorkMu.Unlock()
	}
	select {
	case gate.held <- struct{}{}:
		release := sync.OnceFunc(func() { <-gate.held; unref() })
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		unref()
		return nil, ctx.Err()
	}
}
func (s *PlaybackSessionStore) admitRecipeBackend(ctx context.Context, limit int) (func(), error) {
	s.recipeAdmissionOnce.Do(func() { s.recipeAdmission = make(chan struct{}, max(limit, 1)) })
	select {
	case s.recipeAdmission <- struct{}{}:
		release := sync.OnceFunc(func() { <-s.recipeAdmission })
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// WithRemoteStart serializes the external start and its publication/cleanup for
// one canonical upstream work. The callback is synchronous; its owned context
// must not escape. Network calls never hold the session-map mutex or a row lock.
func (s *PlaybackSessionStore) WithRemoteStart(ctx context.Context, work string, run func(context.Context) error) error {
	if rejectedRecipeOwner(ctx, s, work) {
		return noderecipe.ErrPublicationUncertain
	}
	if recipeOwner(ctx, s, work) != nil {
		return run(ctx)
	}
	acquireCtx, cancel := context.WithTimeout(ctx, recipeMutationTimeout)
	defer cancel()
	release, err := s.lockRecipeWork(acquireCtx, work)
	if err != nil {
		return errors.Join(err, noderecipe.ErrPublicationUncertain)
	}
	defer release()
	owner := &recipeWorkOwner{store: s, work: work}
	owner.active.Store(true)
	defer owner.active.Store(false)
	return run(context.WithValue(ctx, recipeOwnerKey{}, owner))
}
func (d *DurableCompatPlaybackStore) WithRemoteStart(ctx context.Context, work string, run func(context.Context) error) error {
	if d.pool == nil {
		return d.mem.WithRemoteStart(ctx, work, run)
	}
	if rejectedRecipeOwner(ctx, d, work) {
		return noderecipe.ErrPublicationUncertain
	}
	if recipeOwner(ctx, d, work) != nil {
		return run(ctx)
	}
	acquireCtx, cancel := context.WithTimeout(ctx, recipeMutationTimeout)
	defer cancel()
	release, err := d.mem.lockRecipeWork(acquireCtx, work)
	if err != nil {
		return errors.Join(err, noderecipe.ErrPublicationUncertain)
	}
	defer release()
	releaseBackend, err := d.mem.admitRecipeBackend(acquireCtx, int(d.pool.Config().MaxConns))
	if err != nil {
		return errors.Join(err, noderecipe.ErrPublicationUncertain)
	}
	defer releaseBackend()
	locked, err := acquireRecipeConnection(acquireCtx, d.pool, work)
	if err != nil {
		return errors.Join(err, noderecipe.ErrPublicationUncertain)
	}
	defer locked.release()
	owner := &recipeWorkOwner{store: d, work: work, locked: locked}
	owner.active.Store(true)
	defer owner.active.Store(false)
	return run(context.WithValue(ctx, recipeOwnerKey{}, owner))
}

//nolint:nestif // The ownership branches distinguish a fresh lease, a reentrant owner and an expired owner without renewing authority.
func checkRecipeWorkOwner(ctx context.Context, work string) error {
	owner, _ := ctx.Value(recipeOwnerKey{}).(*recipeWorkOwner)
	if owner != nil {
		if !owner.active.Load() || owner.work != work {
			return noderecipe.ErrPublicationUncertain
		}
		if owner.locked != nil {
			if owner.locked.conn == nil {
				return noderecipe.ErrPublicationUncertain
			}
			checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_, err := owner.locked.conn.Exec(checkCtx, "SELECT 1")
			cancel()
			if err != nil {
				owner.locked.discard()
				return errors.Join(err, noderecipe.ErrPublicationUncertain)
			}
		}
	}
	return nil
}

func (h *PlaybackHandler) stopOwnedRemoteStart(ctx context.Context, work, nodeURL string) error {
	if err := checkRecipeWorkOwner(ctx, work); err != nil {
		return err
	}
	// This scoped cleanup finishes before releasing work ownership. In particular
	// a connect failure cannot launch a delayed DELETE against a later owner.
	if err := h.tm.CancelRemoteTranscode(ctx, work, nodeURL); err != nil {
		return errors.Join(err, noderecipe.ErrPublicationUncertain)
	}
	return nil
}
