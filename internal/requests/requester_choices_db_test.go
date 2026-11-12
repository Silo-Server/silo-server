package requests

import (
	"errors"
	"reflect"
	"testing"
)

func TestRequesterChoicesDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	ctx := t.Context()
	first := RequesterChoices{
		IntegrationID: "seerr-1",
		Choices:       []RequesterChoice{{Key: "radarr_profile_id", Label: "Quality", Value: "4", ValueLabel: "HD-1080p"}},
	}
	created, err := repo.CreateRequest(ctx, CreateRequestRecord{
		ID:        "picked",
		Input:     CreateRequestInput{MediaType: MediaTypeMovie, TMDBID: 601, Title: "Picked"},
		Choices:   first,
		Status:    StatusPending,
		Outcome:   OutcomeActive,
		Requester: Viewer{UserID: 1, ProfileID: "profile"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !reflect.DeepEqual(created.RequesterChoices, first) {
		t.Fatalf("created choices = %+v, want %+v", created.RequesterChoices, first)
	}

	// An admin approving replaces the picks while the request is pending.
	second := RequesterChoices{
		IntegrationID: "seerr-1",
		Choices:       []RequesterChoice{{Key: "radarr_profile_id", Label: "Quality", Value: "7", ValueLabel: "Ultra-HD", ByAdmin: true}},
	}
	if err := repo.SetPendingRequesterChoices(ctx, "picked", second); err != nil {
		t.Fatalf("set pending choices: %v", err)
	}
	got, err := repo.GetRequest(ctx, "picked")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reflect.DeepEqual(got.RequesterChoices, second) {
		t.Fatalf("stored choices = %+v, want %+v", got.RequesterChoices, second)
	}

	// A request that is no longer pending keeps its picks.
	insertLifecycleRequest(t, repo, "approved", 1, 602, StatusApproved)
	if err := repo.SetPendingRequesterChoices(ctx, "approved", first); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("approved request: err = %v, want ErrInvalidState", err)
	}
	if err := repo.SetPendingRequesterChoices(ctx, "missing", first); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("missing request: err = %v, want ErrInvalidState", err)
	}
}
