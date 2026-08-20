package auth

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func ptrOf[T any](value T) *T { return &value }

// TestUserRepositoryPolicyOverridesDB pins the inherit/override storage
// contract: nil policy fields are stored as NULL (inherit), explicit values
// round-trip as overrides, and an Optional with Set=true/Value=nil clears a
// column back to inherit.
func TestUserRepositoryPolicyOverridesDB(t *testing.T) {
	ctx, pool, suffix := newAccessGroupUserRepoDBTest(t)
	users := NewUserRepository(pool)

	created, err := users.Create(ctx, createAuthAccessGroupUserInput(suffix, "policy-inherit", nil))
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if created.LibraryIDs != nil || created.MaxPlaybackQuality != nil || created.MaxStreams != nil ||
		created.MaxTranscodes != nil || created.TranscodeAllowed != nil || created.AudioTranscodeAllowed != nil ||
		created.DownloadAllowed != nil || created.DownloadTranscodeAllowed != nil || created.RequestsAllowed != nil {
		t.Fatalf("new user should inherit every policy field, got %+v", created)
	}
	if created.MaxProfiles < 1 {
		t.Fatalf("MaxProfiles = %d, want the DB default", created.MaxProfiles)
	}

	input := createAuthAccessGroupUserInput(suffix, "policy-override", nil)
	input.LibraryIDs = []int{}
	input.MaxPlaybackQuality = ptrOf("4K")
	input.MaxStreams = ptrOf(0)
	input.MaxTranscodes = ptrOf(3)
	input.TranscodeAllowed = ptrOf(false)
	input.AudioTranscodeAllowed = ptrOf(true)
	input.DownloadAllowed = ptrOf(true)
	input.DownloadTranscodeAllowed = ptrOf(false)
	input.RequestsAllowed = ptrOf(false)
	overridden, err := users.Create(ctx, input)
	if err != nil {
		t.Fatalf("Create(overrides) error: %v", err)
	}
	if overridden.LibraryIDs == nil || len(overridden.LibraryIDs) != 0 {
		t.Fatalf("LibraryIDs = %#v, want explicit empty override", overridden.LibraryIDs)
	}
	if overridden.MaxPlaybackQuality == nil || *overridden.MaxPlaybackQuality != "2160p" {
		t.Fatalf("MaxPlaybackQuality = %v, want normalized 2160p", overridden.MaxPlaybackQuality)
	}
	if overridden.MaxStreams == nil || *overridden.MaxStreams != 0 || overridden.MaxTranscodes == nil || *overridden.MaxTranscodes != 3 {
		t.Fatalf("stream overrides = %v/%v, want 0/3", overridden.MaxStreams, overridden.MaxTranscodes)
	}
	if overridden.TranscodeAllowed == nil || *overridden.TranscodeAllowed || overridden.AudioTranscodeAllowed == nil || !*overridden.AudioTranscodeAllowed {
		t.Fatalf("transcode overrides = %v/%v, want false/true", overridden.TranscodeAllowed, overridden.AudioTranscodeAllowed)
	}
	if overridden.DownloadAllowed == nil || !*overridden.DownloadAllowed || overridden.DownloadTranscodeAllowed == nil || *overridden.DownloadTranscodeAllowed {
		t.Fatalf("download overrides = %v/%v, want true/false", overridden.DownloadAllowed, overridden.DownloadTranscodeAllowed)
	}
	if overridden.RequestsAllowed == nil || *overridden.RequestsAllowed {
		t.Fatalf("RequestsAllowed = %v, want explicit false", overridden.RequestsAllowed)
	}

	// Update: set overrides on the inheriting user, bumping the policy
	// revision only for the quality ceiling.
	before := created.AccessPolicyRevision
	if err := users.Update(ctx, created.ID, models.UpdateUserInput{
		LibraryIDs:         models.SetValue([]int{3, 1}),
		MaxPlaybackQuality: models.SetValue("1080p"),
		MaxStreams:         models.SetValue(5),
		DownloadAllowed:    models.SetValue(false),
	}); err != nil {
		t.Fatalf("Update(set overrides) error: %v", err)
	}
	updated, err := users.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID() error: %v", err)
	}
	if !reflect.DeepEqual(updated.LibraryIDs, []int{3, 1}) {
		t.Fatalf("LibraryIDs = %#v, want [3 1]", updated.LibraryIDs)
	}
	if updated.MaxPlaybackQuality == nil || *updated.MaxPlaybackQuality != "1080p" {
		t.Fatalf("MaxPlaybackQuality = %v, want 1080p", updated.MaxPlaybackQuality)
	}
	if updated.MaxStreams == nil || *updated.MaxStreams != 5 {
		t.Fatalf("MaxStreams = %v, want 5", updated.MaxStreams)
	}
	if updated.DownloadAllowed == nil || *updated.DownloadAllowed {
		t.Fatalf("DownloadAllowed = %v, want explicit false", updated.DownloadAllowed)
	}
	if updated.MaxTranscodes != nil || updated.TranscodeAllowed != nil {
		t.Fatalf("untouched fields should still inherit, got transcodes=%v transcode_allowed=%v", updated.MaxTranscodes, updated.TranscodeAllowed)
	}
	if updated.AccessPolicyRevision != before+1 {
		t.Fatalf("AccessPolicyRevision = %d, want %d after quality override", updated.AccessPolicyRevision, before+1)
	}

	// Update: clear back to inherit (explicit null).
	if err := users.Update(ctx, created.ID, models.UpdateUserInput{
		LibraryIDs:         models.ClearValue[[]int](),
		MaxPlaybackQuality: models.ClearValue[string](),
		MaxStreams:         models.ClearValue[int](),
		DownloadAllowed:    models.ClearValue[bool](),
	}); err != nil {
		t.Fatalf("Update(clear overrides) error: %v", err)
	}
	cleared, err := users.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID() after clear error: %v", err)
	}
	if cleared.LibraryIDs != nil || cleared.MaxPlaybackQuality != nil || cleared.MaxStreams != nil || cleared.DownloadAllowed != nil {
		t.Fatalf("cleared fields should inherit, got %+v", cleared)
	}
	if cleared.AccessPolicyRevision != updated.AccessPolicyRevision+1 {
		t.Fatalf("AccessPolicyRevision = %d, want %d after clearing the quality override", cleared.AccessPolicyRevision, updated.AccessPolicyRevision+1)
	}

	// Re-clearing an inheriting quality is a no-op for the revision.
	if err := users.Update(ctx, created.ID, models.UpdateUserInput{MaxPlaybackQuality: models.ClearValue[string]()}); err != nil {
		t.Fatalf("Update(clear again) error: %v", err)
	}
	same, err := users.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID() after no-op clear error: %v", err)
	}
	if same.AccessPolicyRevision != cleared.AccessPolicyRevision {
		t.Fatalf("AccessPolicyRevision = %d after no-op clear, want unchanged %d", same.AccessPolicyRevision, cleared.AccessPolicyRevision)
	}
}
