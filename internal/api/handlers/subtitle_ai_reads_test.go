package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type subtitleQuotaStore struct {
	userstore.UserStore
	primary bool
	err     error
}

func (s subtitleQuotaStore) GetProfile(context.Context, string) (*userstore.Profile, error) {
	return &userstore.Profile{IsPrimary: s.primary}, s.err
}

type subtitleQuotaProvider struct {
	store  userstore.UserStore
	userID int
}

func (p *subtitleQuotaProvider) ForUser(_ context.Context, userID int) (userstore.UserStore, error) {
	p.userID = userID
	return p.store, nil
}
func (*subtitleQuotaProvider) Close() error { return nil }
func TestSubtitleAIQuotaUsesHouseholdProfile(t *testing.T) {
	for _, primary := range []bool{true, false} {
		provider := &subtitleQuotaProvider{store: subtitleQuotaStore{primary: primary}}
		h := &SubtitleAIHandler{StoreProvider: provider}
		if got := h.subtitleQuotaExempt(t.Context(), 7, "profile", true); got != primary || provider.userID != 7 {
			t.Fatalf("wrong household exemption: %v %d", got, provider.userID)
		}
		if h.subtitleQuotaExempt(t.Context(), 7, "profile", false) {
			t.Fatal("ordinary account exempt")
		}
	}
	h := &SubtitleAIHandler{StoreProvider: &subtitleQuotaProvider{store: subtitleQuotaStore{primary: true, err: errors.New("unavailable")}}}
	if h.subtitleQuotaExempt(t.Context(), 7, "profile", true) {
		t.Fatal("failed profile lookup granted exemption")
	}
}
func TestSubtitleAIReadsAuthorizeBeforeJobs(t *testing.T) {
	// An empty repository would panic if a denied list reached persistence.
	h := NewSubtitleAIHandler(ai.NewService(t.Context(), ai.Config{}, nil, nil, nil, nil, nil, nil, nil, "", nil, nil))
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{err: catalog.ErrItemNotFound}}
	_, err := h.ListSubtitleAIJobs(t.Context(), catalog.AccessFilter{UserID: 7}, 42)
	if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 404 {
		t.Fatalf("access error: %v", err)
	}
}
