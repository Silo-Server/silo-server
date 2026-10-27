package handlers

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
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

// InvitationAcceptanceView distinguishes committed account creation from the
// separate login result. No token pair is fabricated after a login failure.
type InvitationAcceptanceView struct {
	Username string
	Tokens   *TokenPairView
}

func (h *InvitationHandler) AcceptInvitation(ctx context.Context, token, email, password, device, ip string) (InvitationAcceptanceView, error) {
	pair, user, err := h.service.Accept(ctx, token, email, password, device, ip)
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
func (h *InvitationHandler) EmailDeliveryAvailable(ctx context.Context) bool {
	return h.service.EmailDeliveryAvailable(ctx)
}
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
func (h *InvitationHandler) Resend(ctx context.Context, id, by int64, delivery invitations.Delivery) (*invitations.SendResult, error) {
	return h.service.Resend(ctx, id, by, delivery)
}
func (h *InvitationHandler) Revoke(ctx context.Context, id int64) error {
	return h.service.Revoke(ctx, id)
}
