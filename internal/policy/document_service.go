package policy

import (
	"context"
	"errors"
)

// DocumentService combines an atomic persisted mutation with its subsequent
// local reload and cross-node notification. Application failures are outcomes,
// never mutation errors that invite replay of an already committed write.
type DocumentService struct {
	store  *PolicyStore
	system *System
}

func NewDocumentService(store *PolicyStore, system *System) *DocumentService {
	return &DocumentService{store: store, system: system}
}

type DocumentApplyResult struct {
	Persisted bool
	DocumentMutation
	Application ApplyStatus
}

func (s *DocumentService) Activate(ctx context.Context, id, versionID, expected int64) (DocumentApplyResult, error) {
	mutation, err := s.store.ActivateIfRevision(ctx, id, versionID, expected, s.system.EvalTimeout())
	if err != nil {
		return DocumentApplyResult{}, err
	}
	return s.apply(ctx, mutation), nil
}
func (s *DocumentService) SetEnabled(ctx context.Context, id int64, enabled bool, expected int64) (DocumentApplyResult, error) {
	mutation, err := s.store.SetEnabledIfRevision(ctx, id, enabled, expected, s.system.EvalTimeout())
	if err != nil {
		return DocumentApplyResult{}, err
	}
	return s.apply(ctx, mutation), nil
}
func (s *DocumentService) apply(ctx context.Context, mutation DocumentMutation) DocumentApplyResult {
	result := DocumentApplyResult{Persisted: true, DocumentMutation: mutation}
	if s.system == nil {
		result.Application.LocalReloadErr = errors.New("policy system unavailable")
	} else {
		result.Application = s.system.ApplyChanged(ctx)
	}
	return result
}
