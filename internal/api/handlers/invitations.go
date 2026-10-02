package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/invitations"
	"github.com/Silo-Server/silo-server/internal/models"
)

// InvitationHandler handles the public (unauthenticated) claim endpoints.
type InvitationHandler struct {
	service      *invitations.Service
	accessGroups access.GroupPolicyProvider
}

// NewInvitationHandler creates a new InvitationHandler.
func NewInvitationHandler(service *invitations.Service) *InvitationHandler {
	return &InvitationHandler{service: service}
}

// SetAccessGroupProvider wires the access-group policy source used to resolve
// the effective policy reported on the accept-invitation login response.
func (h *InvitationHandler) SetAccessGroupProvider(provider access.GroupPolicyProvider) {
	h.accessGroups = provider
}

type invitationLookupResponse struct {
	Email       string    `json:"email"`
	InviterName string    `json:"inviter_name,omitempty"`
	ServerName  string    `json:"server_name"`
	ExpiresAt   time.Time `json:"expires_at"`
	ShowTour    bool      `json:"show_tour"`
}

type acceptInvitationRequest struct {
	Password string `json:"password"`
}

// HandleLookupInvitation handles GET /invitations/{token}. Unknown, expired,
// revoked, and accepted tokens return an identical 404 so a probe learns
// nothing about which.
func (h *InvitationHandler) HandleLookupInvitation(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Lookup(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "This invitation is invalid or has expired")
		return
	}
	writeJSON(w, http.StatusOK, invitationLookupResponse{
		Email:       result.Email,
		InviterName: result.InviterName,
		ServerName:  result.ServerName,
		ExpiresAt:   result.ExpiresAt,
		ShowTour:    result.ShowTour,
	})
}

// HandleAcceptInvitation handles POST /invitations/{token}/accept. On
// success it returns the same login response shape as signup, so clients
// reuse their existing session plumbing.
func (h *InvitationHandler) HandleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	var req acceptInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "weak_password", "Password must be at least 8 characters")
		return
	}

	pair, user, err := h.service.Accept(
		r.Context(),
		chi.URLParam(r, "token"),
		req.Password,
		r.UserAgent(),
		clientip.FromContext(r.Context()),
	)
	if err != nil {
		switch {
		case errors.Is(err, invitations.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "This invitation is invalid or has expired")
		case errors.Is(err, invitations.ErrNotClaimable):
			writeError(w, http.StatusConflict, "already_used", "This invitation has already been used")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		}
		return
	}

	writeJSON(w, http.StatusCreated, buildLoginResponse(pair, user, effectiveDownloadAllowed(r.Context(), user, h.accessGroups), nil))
}

// InvitationAcceptanceView distinguishes committed account creation from the
// separate login result. No token pair is fabricated after a login failure.
type InvitationAcceptanceView struct {
	Username string
	Tokens   *TokenPairView
}

func (h *InvitationHandler) AcceptInvitation(ctx context.Context, token, password, device, ip string) (InvitationAcceptanceView, error) {
	pair, user, err := h.service.Accept(ctx, token, password, device, ip)
	if user == nil {
		return InvitationAcceptanceView{}, err
	}
	view := InvitationAcceptanceView{Username: user.Username}
	if err == nil {
		view.Tokens = &TokenPairView{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken, ExpiresIn: pair.ExpiresIn, User: buildUserResponse(user, effectiveDownloadAllowed(ctx, user, h.accessGroups), nil, nil)}
	}
	return view, err
}
func (h *InvitationHandler) SupportsDefaultProfile() bool { return h.service.SupportsDefaultProfile() }
func (h *InvitationHandler) Lookup(ctx context.Context, token string) (*invitations.LookupResult, error) {
	return h.service.Lookup(ctx, token)
}
func (h *InvitationHandler) GetByID(ctx context.Context, id int64) (*models.Invitation, error) {
	return h.service.GetByID(ctx, id)
}
func (h *InvitationHandler) ListPage(ctx context.Context, after *invitations.PageKey, limit int) ([]*models.Invitation, bool, error) {
	return h.service.ListPage(ctx, after, limit)
}
func (h *InvitationHandler) Send(ctx context.Context, input invitations.SendInput) (*invitations.SendResult, error) {
	return h.service.Send(ctx, input)
}
func (h *InvitationHandler) Resend(ctx context.Context, id, by int64) (*invitations.SendResult, error) {
	return h.service.Resend(ctx, id, by)
}
func (h *InvitationHandler) Revoke(ctx context.Context, id int64) error {
	return h.service.Revoke(ctx, id)
}
