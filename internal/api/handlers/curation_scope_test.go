package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

type curationFollowUp struct{ contexts chan error }

func (s *curationFollowUp) SearchAndNormalize(context.Context, metadata.SearchQuery, int) ([]metadata.MatchCandidate, error) {
	return nil, errors.New("provider transport disabled")
}

func (s *curationFollowUp) Process(ctx context.Context, _ metadata.ProcessRequest) (*metadata.ProcessResult, error) {
	err := access.AuthorizeMetadataCurationLibraries(ctx, []int{2})
	s.contexts <- err
	return nil, err
}

func TestCurationScopeDetachedSplitRetainsAuthority(t *testing.T) {
	origin, cancel := context.WithCancel(access.WithMetadataCurationAuthorizer(t.Context(), func(context.Context, []int) error { return access.ErrMetadataCurationScope }))
	cancel()
	stub := &curationFollowUp{contexts: make(chan error, 1)}
	h := &AdminSplitHandler{metadata: stub}
	h.runPostSplitFollowUps(origin, "synthetic-source", splitTarget{contentID: "synthetic-target", providerIDs: map[string]string{"tmdb": "4242"}}, nil)
	select {
	case err := <-stub.contexts:
		if !errors.Is(err, access.ErrMetadataCurationScope) {
			t.Fatalf("detached follow-up lost authority or inherited cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up did not execute")
	}
}

type curationRefusingMerger struct{}

func (curationRefusingMerger) MergeItems(context.Context, string, string) error {
	return fmt.Errorf("transaction refused: %w", access.ErrMetadataCurationScope)
}

func TestCurationScopeRefusalUsesForbidden(t *testing.T) {
	match := NewAdminMatchHandler(
		&fakeMatchItemLookup{items: map[string]*models.MediaItem{"synthetic": {ContentID: "synthetic", Type: "movie"}}},
		&fakeMatchFolderLookup{folders: map[string]int{"synthetic": 1}},
		&fakeMatchMetadataService{processErr: fmt.Errorf("sink refused: %w", access.ErrMetadataCurationScope)},
	)
	_, matchErr := match.ApplyAdminItemMatch(t.Context(), "synthetic", AdminMatchApplyRequest{ProviderIDs: map[string]string{"tmdb": "4242"}})
	merge := &AdminSplitHandler{merger: curationRefusingMerger{}}
	_, mergeErr := merge.MergeAdminItem(t.Context(), "synthetic", "synthetic-target")
	for _, err := range []error{matchErr, mergeErr} {
		api, ok := errors.AsType[*APIError](err)
		if !ok || api.Status != http.StatusForbidden || api.Code != "forbidden" {
			t.Fatalf("policy refusal = %v", err)
		}
	}
}
