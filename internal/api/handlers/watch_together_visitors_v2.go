package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

// WatchPartyVisitorDeps are what visitor acceptance needs beyond the room
// service: the auth service that opens the visitor's session, the profile
// token service for a PIN-protected host profile, and the host's profile.
type WatchPartyVisitorDeps struct {
	Auth          *auth.Service
	ProfileTokens *access.ProfileTokenService
	Profiles      userstore.UserStoreProvider
}

// VisitorInviteCreated is a new invite link with its token.
type VisitorInviteCreated struct {
	Invite watchtogether.VisitorInvite
	Token  string
}

// VisitorAcceptance is what a visitor receives on joining: its session's
// tokens, the profile it acts as (with a profile token when that profile has
// a PIN), and the room proof for its member identity.
type VisitorAcceptance struct {
	Visitor       watchtogether.AcceptedVisitor
	AccessToken   string
	RefreshToken  string
	ExpiresIn     int
	ProfileToken  string
	RoomToken     string
	SessionEndsAt time.Time
}

// VisitorInvitePreviewView is what an invite link shows before joining.
type VisitorInvitePreviewView struct {
	HostName  string
	ExpiresAt time.Time
}

// ErrVisitorsUnavailable reports that visitor invites are not configured.
var ErrVisitorsUnavailable = errors.New("watch party visitors are unavailable")

// SetVisitorDeps enables visitor acceptance.
func (h *WatchTogetherHandler) SetVisitorDeps(deps WatchPartyVisitorDeps) {
	if h == nil {
		return
	}
	h.visitors = &deps
}

// WatchPartyVisitorsAvailable reports whether visitor invites can be created
// and accepted.
func (h *WatchTogetherHandler) WatchPartyVisitorsAvailable() bool {
	return h != nil && h.Service != nil && h.Service.VisitorsAvailable() && h.TokenService != nil &&
		h.visitors != nil && h.visitors.Auth != nil && h.visitors.ProfileTokens != nil && h.visitors.Profiles != nil
}

func (h *WatchTogetherHandler) CreateWatchPartyVisitorInvite(ctx context.Context, roomID string, user int, profile string) (VisitorInviteCreated, error) {
	if !h.WatchPartyVisitorsAvailable() {
		return VisitorInviteCreated{}, ErrVisitorsUnavailable
	}
	invite, token, err := h.Service.CreateVisitorInvite(ctx, roomID, user, profile)
	return VisitorInviteCreated{Invite: invite, Token: token}, err
}

func (h *WatchTogetherHandler) ListWatchPartyVisitorInvites(ctx context.Context, roomID string, user int, profile string) ([]watchtogether.VisitorInvite, error) {
	if !h.WatchPartyVisitorsAvailable() {
		return nil, ErrVisitorsUnavailable
	}
	return h.Service.ListVisitorInvites(ctx, roomID, user, profile)
}

func (h *WatchTogetherHandler) RevokeWatchPartyVisitorInvite(ctx context.Context, roomID, inviteID string, user int, profile string) error {
	if !h.WatchPartyVisitorsAvailable() {
		return ErrVisitorsUnavailable
	}
	return h.Service.RevokeVisitorInvite(ctx, roomID, inviteID, user, profile)
}

func (h *WatchTogetherHandler) RemoveWatchPartyVisitor(ctx context.Context, roomID, visitorID string, user int, profile string) error {
	if !h.WatchPartyVisitorsAvailable() {
		return ErrVisitorsUnavailable
	}
	return h.Service.RemoveVisitor(ctx, roomID, visitorID, user, profile)
}

func (h *WatchTogetherHandler) PreviewWatchPartyVisitorInvite(ctx context.Context, token string) (VisitorInvitePreviewView, error) {
	if !h.WatchPartyVisitorsAvailable() {
		return VisitorInvitePreviewView{}, ErrVisitorsUnavailable
	}
	preview, err := h.Service.PreviewVisitorInvite(ctx, token)
	if err != nil {
		return VisitorInvitePreviewView{}, err
	}
	return VisitorInvitePreviewView{HostName: h.Service.MemberDisplayName(ctx, preview.HostUserID, preview.HostProfileID), ExpiresAt: preview.ExpiresAt}, nil
}

// AcceptWatchPartyVisitorInvite admits a visitor named displayName through
// token. The visitor row and its login session commit together; tokens,
// profile token and room proof are minted after the commit.
func (h *WatchTogetherHandler) AcceptWatchPartyVisitorInvite(ctx context.Context, token, displayName, deviceName, ipAddress string) (VisitorAcceptance, error) {
	if !h.WatchPartyVisitorsAvailable() {
		return VisitorAcceptance{}, ErrVisitorsUnavailable
	}
	sessionInput := func(v watchtogether.AcceptedVisitor) auth.WatchPartyVisitorSessionInput {
		return auth.WatchPartyVisitorSessionInput{
			SessionID: v.SessionID, HostUserID: v.HostUserID, HostProfileID: v.HostProfileID,
			RoomID: v.RoomID, VisitorID: v.VisitorID, EndsAt: v.SessionEndsAt,
			DeviceName: deviceName, IPAddress: ipAddress,
		}
	}
	// The host account and profile are read before the acceptance
	// transaction, so it never waits on a second pooled connection. A room's
	// host never changes, and the transaction checks that the link still
	// leads to this host's room.
	preview, err := h.Service.PreviewVisitorInvite(ctx, token)
	if err != nil {
		return VisitorAcceptance{}, err
	}
	if err := h.visitors.Auth.CheckWatchPartyHost(ctx, preview.HostUserID); err != nil {
		if errors.Is(err, auth.ErrVisitorHostUnavailable) {
			return VisitorAcceptance{}, watchtogether.ErrVisitorInviteNotFound
		}
		return VisitorAcceptance{}, err
	}
	hostProfile, err := h.hostProfile(ctx, preview.HostUserID, preview.HostProfileID)
	if err != nil {
		return VisitorAcceptance{}, err
	}
	accepted, err := h.Service.AcceptVisitorInvite(ctx, token, displayName, func(ctx context.Context, tx pgx.Tx, v watchtogether.AcceptedVisitor) error {
		if v.RoomID != preview.RoomID || v.HostUserID != preview.HostUserID || v.HostProfileID != preview.HostProfileID {
			return watchtogether.ErrVisitorInviteNotFound
		}
		return h.visitors.Auth.CreateWatchPartyVisitorSession(ctx, tx, sessionInput(v))
	})
	if err != nil {
		return VisitorAcceptance{}, err
	}
	in := sessionInput(accepted)
	tokens, err := h.visitors.Auth.WatchPartyVisitorTokens(in)
	if err != nil {
		return VisitorAcceptance{}, err
	}
	out := VisitorAcceptance{Visitor: accepted, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, ExpiresIn: tokens.ExpiresIn, SessionEndsAt: accepted.SessionEndsAt}
	if hostProfile.PINHash != "" {
		// The host vouched for the visitor by inviting it; the proof is
		// bound to the visitor's own session and ends with it or with a
		// change of the profile's PIN.
		out.ProfileToken, _, err = h.visitors.ProfileTokens.Mint(access.ProfileTokenClaims{
			UserID: accepted.HostUserID, SessionID: accepted.SessionID, ProfileID: accepted.HostProfileID, PINRevision: hostProfile.PINRevision,
		})
		if err != nil {
			return VisitorAcceptance{}, fmt.Errorf("mint visitor profile token: %w", err)
		}
	}
	out.RoomToken, _, err = h.TokenService.Mint(watchtogether.RoomTokenClaims{
		RoomID: accepted.RoomID, UserID: accepted.HostUserID, ProfileID: watchtogether.VisitorMemberProfileID(accepted.VisitorID),
	})
	if err != nil {
		return VisitorAcceptance{}, err
	}
	return out, nil
}

func (h *WatchTogetherHandler) hostProfile(ctx context.Context, userID int, profileID string) (*userstore.Profile, error) {
	store, err := h.visitors.Profiles.ForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, watchtogether.ErrVisitorInviteNotFound
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, watchtogether.ErrVisitorInviteNotFound
	}
	return profile, nil
}
