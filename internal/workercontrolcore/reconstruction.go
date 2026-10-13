package workercontrolcore

import (
	"context"
	"encoding/json"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type recoveryKey struct{}
type recipeSourceKindKey struct{}

func recipeAdmissionContext(ctx context.Context, card playback.RecipeCard) context.Context {
	if card.PlayMethod == streamtoken.PlayMethodThemeAAC || card.PlayMethod == streamtoken.PlayMethodThemeDirect {
		return context.WithValue(ctx, recipeSourceKindKey{}, sourceTheme)
	}
	return ctx
}

func (s *MemoryStore) BeginGuarded(ctx context.Context, a workerwire.Assignment, expected uint64) (workerwire.Assignment, error) {
	if err := ctx.Err(); err != nil {
		return a, err
	}
	if !validAssignment(a) {
		return a, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{a.NodeID, a.WorkID}
	if _, ok := s.candidates[k]; ok {
		return a, ErrFenced
	}
	live, found := s.live[k]
	if expected == 0 {
		if found || s.versions[k] != 0 {
			return a, ErrFenced
		}
	} else if !found || live.assignment.Generation != expected {
		return a, ErrFenced
	}
	return s.beginLocked(a)
}
func (s *RedisStore) BeginGuarded(ctx context.Context, a workerwire.Assignment, expected uint64) (workerwire.Assignment, error) {
	return s.Begin(context.WithValue(ctx, recoveryKey{}, expected), a)
}
func (s *Service) begin(ctx context.Context, a workerwire.Assignment) (workerwire.Assignment, error) {
	if expected, ok := ctx.Value(recoveryKey{}).(uint64); ok {
		guarded, ok := s.options.Store.(interface {
			BeginGuarded(context.Context, workerwire.Assignment, uint64) (workerwire.Assignment, error)
		})
		if !ok {
			return a, ErrUnavailable
		}
		return guarded.BeginGuarded(ctx, a, expected)
	}
	return s.options.Store.Begin(ctx, a)
}
func (s *Service) restoreSources(ctx context.Context, node Node, old workerwire.Assignment) (workerwire.Assignment, error) {
	if !s.executionActive(ctx, node, old) {
		return old, ErrDenied
	}
	authority, ok := s.options.Sources.(interface {
		Renew(context.Context, int, string, uint64, string) (workerwire.Source, error)
	})
	if !ok {
		return old, nil
	}
	expired := false
	for _, source := range old.Sources {
		renewed, err := authority.Renew(ctx, node.ID, old.WorkID, old.Generation, source.Grant)
		if err != nil {
			expired = true
			break
		}
		if renewed.Ref != source.Ref || renewed.NodeID != node.ID || renewed.WorkID != old.WorkID || renewed.Generation != old.Generation {
			return old, ErrDenied
		}
	}
	if !expired {
		return old, nil
	}
	if s.options.RecipeStore == nil {
		return old, ErrDenied
	}
	card, ok := s.options.RecipeStore.Get(ctx, old.WorkID)
	if !ok || card == nil || !recipeNode(*card, node) {
		return old, ErrDenied
	}
	alias, ok := primarySource(old)
	if !ok {
		return old, ErrDenied
	}
	binder, ok := s.options.Store.(InputBinder)
	if !ok {
		return old, ErrUnavailable
	}
	bindings, err := binder.InputBindings(ctx, node.ID, old.WorkID, old.Generation)
	if err != nil || bindings[alias] != inputDigest(card.InputPath) {
		return old, ErrDenied
	}
	encoded, err := json.Marshal(card)
	if err != nil {
		return old, err
	}
	next, err := s.admit(recipeAdmissionContext(context.WithValue(ctx, recoveryKey{}, old.Generation), *card), node, old.WorkID, transcodeStartPath, encoded)
	if err != nil {
		return old, err
	}
	nextAlias, valid := primarySource(next)
	if !valid || next.Sources[nextAlias].Ref.Revision != old.Sources[alias].Ref.Revision {
		s.cancelAdmission(context.WithoutCancel(ctx), next)
		return old, ErrDenied
	}
	if err = s.options.Store.Activate(ctx, node.ID, old.WorkID, next.Generation); err != nil {
		s.cancelAdmission(context.WithoutCancel(ctx), next)
		return old, err
	}
	s.finishAdmission(next)
	if err = s.options.Sources.Release(ctx, node.ID, old.WorkID, old.Generation); err != nil {
		return next, err
	}
	return next, nil
}
