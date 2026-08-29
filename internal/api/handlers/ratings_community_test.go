package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type communityRatingsRepoStub struct {
	community      []catalog.CommunityRating
	setReaction    int
	setTargetUser  int
	setTargetID    string
	setReactionOK  bool
	deleteReaction bool
}

func (*communityRatingsRepoStub) Set(context.Context, int, string, string, int) error { return nil }
func (*communityRatingsRepoStub) Get(context.Context, int, string, string) (*catalog.UserRating, error) {
	return nil, nil
}
func (*communityRatingsRepoStub) Delete(context.Context, int, string, string) error { return nil }
func (*communityRatingsRepoStub) List(context.Context, int, string, int, int) ([]catalog.UserRating, error) {
	return nil, nil
}
func (s *communityRatingsRepoStub) ListCommunity(context.Context, int, string, string, int, int) ([]catalog.CommunityRating, error) {
	return s.community, nil
}
func (s *communityRatingsRepoStub) SetCommunityReaction(_ context.Context, _ int, _ string, _ string, targetUserID int, targetProfileID string, reaction int) (bool, error) {
	s.setTargetUser = targetUserID
	s.setTargetID = targetProfileID
	s.setReaction = reaction
	return s.setReactionOK, nil
}
func (s *communityRatingsRepoStub) DeleteCommunityReaction(context.Context, int, string, string, int, string) error {
	s.deleteReaction = true
	return nil
}

type communityRatingsItemRepoStub struct{}

func (communityRatingsItemRepoStub) GetByID(context.Context, string) (*models.MediaItem, error) {
	return nil, nil
}
func (communityRatingsItemRepoStub) GetByIDs(context.Context, []string) ([]*models.MediaItem, error) {
	return nil, nil
}
func (communityRatingsItemRepoStub) EnsureAccessible(context.Context, string, catalog.AccessFilter) error {
	return nil
}

type communityAvatarStoreStub struct {
	data   []byte
	getKey string
}

func (s *communityAvatarStoreStub) GetObject(_ context.Context, _, key string) ([]byte, error) {
	s.getKey = key
	return s.data, nil
}
func (*communityAvatarStoreStub) PutObject(context.Context, string, string, []byte) error {
	return nil
}
func (*communityAvatarStoreStub) DeleteObject(context.Context, string, string) error { return nil }
func (*communityAvatarStoreStub) ListObjects(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (*communityAvatarStoreStub) PresignGetURL(context.Context, string, string, time.Duration) (string, error) {
	return "https://storage.invalid/private-key", nil
}
func (*communityAvatarStoreStub) Bucket() string { return "private" }

func TestCommunityRatingsResponseMasksIdentityAndUsesCurrentAvatar(t *testing.T) {
	ratedAt := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	repo := &communityRatingsRepoStub{community: []catalog.CommunityRating{
		{
			UserID:         9,
			ProfileID:      "profile-secret",
			ProfileName:    "Samantha",
			Avatar:         "preset:avatar-1",
			Rating:         5,
			RatedAt:        ratedAt,
			UpCount:        7,
			DownCount:      2,
			ViewerReaction: 1,
			AverageRating:  4.5,
			TotalVoteCount: 2,
		},
	}}
	handler := NewRatingsHandler(repo, communityRatingsItemRepoStub{})
	req := communityRatingsRequest(http.MethodGet, "/ratings/movie-1/community", "movie-1", "", nil)
	recorder := httptest.NewRecorder()

	handler.HandleListCommunityRatings(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "profile-secret") || strings.Contains(body, "Samantha") {
		t.Fatalf("response leaked profile identity: %s", body)
	}
	for _, want := range []string{
		`"display_name":"S*******"`,
		`"avatar_url":"/profile-avatars/avatar-1.svg"`,
		`"rated_at":"2026-08-29T08:00:00Z"`,
		`"viewer_reaction":"up"`,
		`"average_rating":4.5`,
		`"vote_count":2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("response %q does not contain %q", body, want)
		}
	}
}

func TestSetCommunityRatingReactionResolvesOpaqueCardKey(t *testing.T) {
	target := catalog.CommunityRating{
		UserID:      9,
		ProfileID:   "profile-secret",
		ProfileName: "Samantha",
		Rating:      5,
		RatedAt:     time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC),
	}
	repo := &communityRatingsRepoStub{
		community:     []catalog.CommunityRating{target},
		setReactionOK: true,
	}
	handler := NewRatingsHandler(repo, communityRatingsItemRepoStub{})
	req := communityRatingsRequest(
		http.MethodPut,
		"/ratings/movie-1/community/key/reaction",
		"movie-1",
		communityRatingKey("movie-1", target),
		strings.NewReader(`{"reaction":"down"}`),
	)
	recorder := httptest.NewRecorder()

	handler.HandleSetCommunityRatingReaction(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.setTargetUser != target.UserID || repo.setTargetID != target.ProfileID || repo.setReaction != -1 {
		t.Fatalf("set target = (%d, %q, %d)", repo.setTargetUser, repo.setTargetID, repo.setReaction)
	}
}

func TestSetCommunityRatingReactionAcceptsLegacyKeyDuringRollingDeploy(t *testing.T) {
	target := catalog.CommunityRating{
		UserID:      9,
		ProfileID:   "profile-secret",
		ProfileName: "Samantha",
		Rating:      5,
		RatedAt:     time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC),
	}
	repo := &communityRatingsRepoStub{
		community:     []catalog.CommunityRating{target},
		setReactionOK: true,
	}
	handler := NewRatingsHandler(repo, communityRatingsItemRepoStub{})
	req := communityRatingsRequest(
		http.MethodPut,
		"/ratings/movie-1/community/key/reaction",
		"movie-1",
		legacyCommunityRatingKey(target),
		strings.NewReader(`{"reaction":"up"}`),
	)
	recorder := httptest.NewRecorder()

	handler.HandleSetCommunityRatingReaction(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCommunityUploadedAvatarUsesOpaqueProxyCapability(t *testing.T) {
	rating := catalog.CommunityRating{
		UserID:           9,
		ProfileID:        "profile-secret",
		ProfileName:      "Samantha",
		Avatar:           "upload:profile-avatars/9/profile-secret/original.webp",
		ProfileUpdatedAt: time.Date(2026, 8, 29, 8, 5, 0, 0, time.UTC),
		Rating:           5,
		RatedAt:          time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC),
	}
	repo := &communityRatingsRepoStub{community: []catalog.CommunityRating{rating}}
	store := &communityAvatarStoreStub{data: []byte("webp-data")}
	handler := NewRatingsHandler(repo, communityRatingsItemRepoStub{})
	handler.AvatarStore = store
	handler.AvatarTokenSecret = []byte("test-avatar-signing-secret")

	avatarURL := handler.communityAvatarURL(context.Background(), rating)
	if strings.Contains(avatarURL, "profile-secret") || strings.Contains(avatarURL, "profile-avatars/9") {
		t.Fatalf("avatar URL leaked storage identity: %s", avatarURL)
	}
	const prefix = "/api/v1/ratings/community-avatar/"
	if !strings.HasPrefix(avatarURL, prefix) {
		t.Fatalf("avatar URL = %q", avatarURL)
	}

	req := communityRatingsRequest(
		http.MethodGet,
		avatarURL,
		"",
		"",
		nil,
	)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("token", strings.TrimPrefix(avatarURL, prefix))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	recorder := httptest.NewRecorder()
	handler.HandleCommunityRatingAvatar(recorder, req)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "webp-data" {
		t.Fatalf("avatar response = %d %q", recorder.Code, recorder.Body.String())
	}
	if store.getKey != "profile-avatars/9/profile-secret/w256.webp" {
		t.Fatalf("avatar object key = %q", store.getKey)
	}
}

func TestCommunityAvatarTokenRejectsOversizedInput(t *testing.T) {
	handler := NewRatingsHandler(&communityRatingsRepoStub{}, communityRatingsItemRepoStub{})
	handler.AvatarTokenSecret = []byte("test-avatar-signing-secret")

	if _, ok := handler.parseCommunityAvatarToken(strings.Repeat("a", 2049)); ok {
		t.Fatal("oversized avatar token was accepted")
	}
}

func TestSetCommunityRatingReactionAllowsOwnCard(t *testing.T) {
	target := catalog.CommunityRating{
		UserID:      7,
		ProfileID:   "viewer-profile",
		ProfileName: "Viewer",
		Rating:      4,
		RatedAt:     time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC),
	}
	repo := &communityRatingsRepoStub{
		community:     []catalog.CommunityRating{target},
		setReactionOK: true,
	}
	handler := NewRatingsHandler(repo, communityRatingsItemRepoStub{})
	req := communityRatingsRequest(
		http.MethodPut,
		"/ratings/movie-1/community/key/reaction",
		"movie-1",
		communityRatingKey("movie-1", target),
		strings.NewReader(`{"reaction":"up"}`),
	)
	recorder := httptest.NewRecorder()

	handler.HandleSetCommunityRatingReaction(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.setTargetUser != target.UserID || repo.setTargetID != target.ProfileID || repo.setReaction != 1 {
		t.Fatalf("set target = (%d, %q, %d)", repo.setTargetUser, repo.setTargetID, repo.setReaction)
	}
}

func TestDeleteCommunityRatingReactionAllowsOwnCard(t *testing.T) {
	target := catalog.CommunityRating{
		UserID:      7,
		ProfileID:   "viewer-profile",
		ProfileName: "Viewer",
		Rating:      4,
		RatedAt:     time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC),
	}
	repo := &communityRatingsRepoStub{community: []catalog.CommunityRating{target}}
	handler := NewRatingsHandler(repo, communityRatingsItemRepoStub{})
	req := communityRatingsRequest(
		http.MethodDelete,
		"/ratings/movie-1/community/key/reaction",
		"movie-1",
		communityRatingKey("movie-1", target),
		nil,
	)
	recorder := httptest.NewRecorder()

	handler.HandleDeleteCommunityRatingReaction(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !repo.deleteReaction {
		t.Fatal("own-card reaction delete did not reach repository")
	}
}

func TestAbbreviateProfileNameUsesRunes(t *testing.T) {
	if got := abbreviateProfileName("Åsa-Marie"); got != "Å********" {
		t.Fatalf("abbreviated name = %q", got)
	}
	if got := abbreviateProfileName("Mohommad"); got != "M*******" {
		t.Fatalf("abbreviated name = %q", got)
	}
}

func TestCommunityRatingKeyIsStableAcrossEditsAndScopedToOneCard(t *testing.T) {
	rating := catalog.CommunityRating{
		UserID:    7,
		ProfileID: "viewer-profile",
		Rating:    2,
		RatedAt:   time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC),
	}
	original := communityRatingKey("movie-1", rating)

	rating.Rating = 5
	rating.RatedAt = time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	if edited := communityRatingKey("movie-1", rating); edited != original {
		t.Fatalf("edited rating key = %q, want stable key %q", edited, original)
	}

	otherAccount := rating
	otherAccount.UserID = 8
	if got := communityRatingKey("movie-1", otherAccount); got == original {
		t.Fatal("different account produced the same card key")
	}
	if got := communityRatingKey("movie-2", rating); got == original {
		t.Fatal("different item produced the same card key")
	}
}

func communityRatingsRequest(method, target, itemID, ratingKey string, body *strings.Reader) *http.Request {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, body)
	}
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("item_id", itemID)
	if ratingKey != "" {
		routeContext.URLParams.Add("rating_key", ratingKey)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 7})
	ctx = apimw.SetProfileID(ctx, "viewer-profile")
	return req.WithContext(ctx)
}
