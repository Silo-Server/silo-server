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

	// An admin approving replaces the picks in the same update that approves.
	second := RequesterChoices{
		IntegrationID: "seerr-1",
		Choices:       []RequesterChoice{{Key: "radarr_profile_id", Label: "Quality", Value: "7", ValueLabel: "Ultra-HD", ByAdmin: true}},
	}
	approved, err := repo.ApproveWithRequesterChoices(ctx, "picked", second, Viewer{UserID: 9, IsAdmin: true})
	if err != nil {
		t.Fatalf("approve with choices: %v", err)
	}
	if approved.Status != StatusApproved || !reflect.DeepEqual(approved.RequesterChoices, second) {
		t.Fatalf("approved = %s %+v, want approved with %+v", approved.Status, approved.RequesterChoices, second)
	}

	// A second approval loses: the request is no longer pending, and the
	// winner's picks stay.
	if _, err := repo.ApproveWithRequesterChoices(ctx, "picked", first, Viewer{UserID: 8, IsAdmin: true}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second approval: err = %v, want ErrInvalidState", err)
	}
	got, err := repo.GetRequest(ctx, "picked")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reflect.DeepEqual(got.RequesterChoices, second) {
		t.Fatalf("stored choices = %+v, want the winner's %+v", got.RequesterChoices, second)
	}

	// A plain status change leaves the picks alone.
	insertLifecycleRequest(t, repo, "plain", 1, 602, StatusPending)
	plain, err := repo.SetStatus(ctx, "plain", guardPending, StatusApproved, Viewer{UserID: 9, IsAdmin: true})
	if err != nil {
		t.Fatalf("set status: %v", err)
	}
	if len(plain.RequesterChoices.Choices) != 0 {
		t.Fatalf("plain approval choices = %+v, want none", plain.RequesterChoices)
	}
}
