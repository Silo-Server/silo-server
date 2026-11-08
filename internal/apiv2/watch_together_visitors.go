package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

// WatchPartyVisitorService manages Watch Party visitor invites: links a host
// shares with people who have no account, and the visitors who join through
// them. See docs/architecture/watch-party-visitors.md.
type WatchPartyVisitorService interface {
	WatchPartyVisitorsAvailable() bool
	CreateWatchPartyVisitorInvite(ctx context.Context, roomID string, user int, profile string) (handlers.VisitorInviteCreated, error)
	ListWatchPartyVisitorInvites(ctx context.Context, roomID string, user int, profile string) ([]watchtogether.VisitorInvite, error)
	RevokeWatchPartyVisitorInvite(ctx context.Context, roomID, inviteID string, user int, profile string) error
	RemoveWatchPartyVisitor(ctx context.Context, roomID, visitorID string, user int, profile string) error
	PreviewWatchPartyVisitorInvite(ctx context.Context, token string) (handlers.VisitorInvitePreviewView, error)
	AcceptWatchPartyVisitorInvite(ctx context.Context, token, displayName, deviceName, ipAddress string) (handlers.VisitorAcceptance, error)
}

// WatchPartyVisitorInvite is a usable invite link's metadata. The token is
// only disclosed when the link is created.
type WatchPartyVisitorInvite struct {
	ID        string  `json:"id" doc:"Invite link id"`
	CreatedAt Instant `json:"created_at"`
	ExpiresAt Instant `json:"expires_at" doc:"When the link stops admitting visitors"`
}

// WatchPartyVisitorInviteCreated is a new invite link.
type WatchPartyVisitorInviteCreated struct {
	Invite WatchPartyVisitorInvite `json:"invite"`
	Token  string                  `json:"token" doc:"The link's secret token. Shown only here; keep it out of durable caches. Clients build the link to their own visitor join screen from it."`
}

type WatchPartyVisitorInviteList struct {
	Items []WatchPartyVisitorInvite `json:"items" doc:"Usable invite links of the room, newest first"`
}

// WatchPartyVisitorInvitePreview is what an invite link shows before
// joining.
type WatchPartyVisitorInvitePreview struct {
	HostName  string  `json:"host_name" doc:"Display name of the host's profile"`
	ExpiresAt Instant `json:"expires_at" doc:"When the link stops admitting visitors"`
}

// WatchPartyVisitorAcceptance is a visitor's credential set.
type WatchPartyVisitorAcceptance struct {
	RoomID        string  `json:"room_id"`
	VisitorID     string  `json:"visitor_id"`
	DisplayName   string  `json:"display_name"`
	MemberID      string  `json:"member_profile_id" doc:"The visitor's profile_id in the room's member roster"`
	AccessToken   string  `json:"access_token" doc:"Bearer access token of the visitor's session"`
	RefreshToken  string  `json:"refresh_token" doc:"Refresh token for POST /auth/refresh"`
	ExpiresIn     int     `json:"expires_in" doc:"Access token lifetime in seconds"`
	ProfileID     string  `json:"profile_id" doc:"Value for X-Profile-Id on every visitor request"`
	ProfileToken  string  `json:"profile_token,omitempty" doc:"Value for X-Profile-Token when the bound profile has a PIN"`
	RoomToken     string  `json:"room_token" doc:"Room proof for X-Room-Token, as joinWatchTogetherRoom returns"`
	SessionEndsAt Instant `json:"session_ends_at" doc:"Latest end of the visitor's session; closing the room or removing the visitor ends it sooner"`
}

type WatchPartyVisitorRoomInput struct {
	RoomID string `path:"room_id" maxLength:"128"`
}

type WatchPartyVisitorInviteIDInput struct {
	RoomID   string `path:"room_id" maxLength:"128"`
	InviteID string `path:"invite_id" maxLength:"128"`
}

type WatchPartyVisitorIDInput struct {
	RoomID    string `path:"room_id" maxLength:"128"`
	VisitorID string `path:"visitor_id" maxLength:"128"`
}

type WatchPartyVisitorTokenInput struct {
	Token string `path:"token" maxLength:"128"`
}

type WatchPartyVisitorAcceptInput struct {
	Token     string `path:"token" maxLength:"128"`
	UserAgent string `header:"User-Agent"`
	Body      struct {
		DisplayName string `json:"display_name" minLength:"1" maxLength:"160" doc:"Name the room shows for the visitor, up to 40 characters"`
	}
}

type WatchPartyVisitorInviteCreatedOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         WatchPartyVisitorInviteCreated
}

type WatchPartyVisitorInviteListOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         WatchPartyVisitorInviteList
}

type WatchPartyVisitorInvitePreviewOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         WatchPartyVisitorInvitePreview
}

type WatchPartyVisitorAcceptanceOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         WatchPartyVisitorAcceptance
}

const (
	noStore = "no-store"
	// visitorInviteRateBucket is the rate-limit budget of the public invite
	// routes, shared with emailed invitations.
	visitorInviteRateBucket = "invitation"
)

func visitorInviteOf(invite watchtogether.VisitorInvite) WatchPartyVisitorInvite {
	return WatchPartyVisitorInvite{ID: invite.ID, CreatedAt: NewInstant(invite.CreatedAt), ExpiresAt: NewInstant(invite.ExpiresAt)}
}

func visitorProblem(err error) error {
	switch {
	case errors.Is(err, handlers.ErrVisitorsUnavailable):
		return unavailable("watch party visitors")
	case errors.Is(err, watchtogether.ErrVisitorInviteNotFound):
		return NewProblem(TypeNotFound, "The invite link is not valid.")
	case errors.Is(err, watchtogether.ErrVisitorNotFound):
		return NewProblem(TypeNotFound, "The visitor is not in this room.")
	case errors.Is(err, watchtogether.ErrVisitorLimitReached):
		return NewProblem(TypeConflict, "The room has reached its visitor or invite link limit.")
	case errors.Is(err, watchtogether.ErrInvalidVisitorName):
		return NewProblem(TypeValidationFailed, "Enter a name of 1 to 40 characters.")
	case errors.Is(err, watchtogether.ErrRoomForbidden):
		return NewProblem(TypePermissionDenied, "Only the host account and profile may manage visitors.")
	default:
		return suggestionProblem(err)
	}
}

func (reg *Registry) watchPartyVisitors() (WatchPartyVisitorService, *Problem) {
	svc := reg.deps.WatchPartyVisitors
	if svc == nil || !svc.WatchPartyVisitorsAvailable() {
		return nil, unavailable("watch party visitors")
	}
	return svc, nil
}

func registerWatchPartyVisitors(reg *Registry) {
	const room = Prefix + "/watch-together/rooms/{room_id}"
	hostOp := func(method, path, id, summary string) Operation {
		op := Operation{Operation: humaOp(method, room+path, id, "realtime", summary), Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: isMutatingMethod(method)}
		op.Errors = []int{http.StatusConflict}
		switch method {
		case http.MethodPost:
			op.RetrySafety = RetrySafetyNonRetryable
		case http.MethodDelete:
			op.DefaultStatus = http.StatusNoContent
			op.RetrySafety = RetrySafetyNaturalIdempotent
		}
		return op
	}
	create := hostOp(http.MethodPost, "/visitor-invites", "createWatchPartyVisitorInvite",
		"Create an invite link as the room's host. People without an account join this room alone through it, as visitors whose streams count against the host's limits. The token is shown only in this response.")
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, func(ctx context.Context, in *WatchPartyVisitorRoomInput) (*WatchPartyVisitorInviteCreatedOutput, error) {
		svc, p := reg.watchPartyVisitors()
		if p != nil {
			return nil, p
		}
		user, profile, p := watchPartyMember(ctx)
		if p != nil {
			return nil, p
		}
		// Visitors act on the host's account, so only the host's own login
		// session may invite them: not an API key, an impersonating
		// administrator or a bound session.
		if !claimsFrom(ctx).IsOwnLoginSession() {
			return nil, NewProblem(TypePermissionDenied, "Only the host's own sign-in may invite visitors.")
		}
		created, err := svc.CreateWatchPartyVisitorInvite(ctx, in.RoomID, user, profile)
		if err != nil {
			return nil, visitorProblem(err)
		}
		return &WatchPartyVisitorInviteCreatedOutput{CacheControl: noStore, Body: WatchPartyVisitorInviteCreated{
			Invite: visitorInviteOf(created.Invite), Token: created.Token,
		}}, nil
	})
	list := hostOp(http.MethodGet, "/visitor-invites", "listWatchPartyVisitorInvites", "List the room's usable invite links as its host, without their tokens.")
	Register(reg, list, func(ctx context.Context, in *WatchPartyVisitorRoomInput) (*WatchPartyVisitorInviteListOutput, error) {
		svc, p := reg.watchPartyVisitors()
		if p != nil {
			return nil, p
		}
		user, profile, p := watchPartyMember(ctx)
		if p != nil {
			return nil, p
		}
		invites, err := svc.ListWatchPartyVisitorInvites(ctx, in.RoomID, user, profile)
		if err != nil {
			return nil, visitorProblem(err)
		}
		out := WatchPartyVisitorInviteList{Items: make([]WatchPartyVisitorInvite, 0, len(invites))}
		for _, invite := range invites {
			out.Items = append(out.Items, visitorInviteOf(invite))
		}
		return &WatchPartyVisitorInviteListOutput{CacheControl: noStore, Body: out}, nil
	})
	registerWatchPartyVisitorRemovals(reg, hostOp)
	registerWatchPartyVisitorPublic(reg)
}

// registerWatchPartyVisitorRemovals registers revoking a link and removing a
// visitor.
func registerWatchPartyVisitorRemovals(reg *Registry, hostOp func(method, path, id, summary string) Operation) {
	revoke := hostOp(http.MethodDelete, "/visitor-invites/{invite_id}", "revokeWatchPartyVisitorInvite", "Stop an invite link from admitting anyone else, as the room's host. Visitors who already joined stay; remove them separately.")
	Register(reg, revoke, func(ctx context.Context, in *WatchPartyVisitorInviteIDInput) (*struct{}, error) {
		svc, p := reg.watchPartyVisitors()
		if p != nil {
			return nil, p
		}
		user, profile, p := watchPartyMember(ctx)
		if p != nil {
			return nil, p
		}
		if err := svc.RevokeWatchPartyVisitorInvite(ctx, in.RoomID, in.InviteID, user, profile); err != nil {
			return nil, visitorProblem(err)
		}
		return &struct{}{}, nil
	})
	remove := hostOp(http.MethodDelete, "/visitors/{visitor_id}", "removeWatchPartyVisitor", "Remove a visitor from the room as its host and end the visitor's session. Its room socket closes at the next authority check.")
	remove.Errors = append(remove.Errors, http.StatusNotFound)
	Register(reg, remove, func(ctx context.Context, in *WatchPartyVisitorIDInput) (*struct{}, error) {
		svc, p := reg.watchPartyVisitors()
		if p != nil {
			return nil, p
		}
		user, profile, p := watchPartyMember(ctx)
		if p != nil {
			return nil, p
		}
		if err := svc.RemoveWatchPartyVisitor(ctx, in.RoomID, in.VisitorID, user, profile); err != nil {
			return nil, visitorProblem(err)
		}
		return &struct{}{}, nil
	})
}

// registerWatchPartyVisitorPublic registers the credential-free invite
// lookup and acceptance.
func registerWatchPartyVisitorPublic(reg *Registry) {
	publicOp := func(method, path, id, summary string) Operation {
		op := Operation{Operation: humaOp(method, Prefix+"/watch-together/visitor-invites/{token}"+path, id, "realtime", summary), Class: ClassPublic, ServiceBacked: true, RateLimitBucket: visitorInviteRateBucket}
		op.Errors = []int{http.StatusNotFound, http.StatusTooManyRequests}
		return op
	}
	preview := publicOp(http.MethodGet, "", "getWatchPartyVisitorInvite", "Show where a usable invite link leads. Unknown, revoked and expired links, and links to a closed room, are all 404.")
	Register(reg, preview, func(ctx context.Context, in *WatchPartyVisitorTokenInput) (*WatchPartyVisitorInvitePreviewOutput, error) {
		svc, p := reg.watchPartyVisitors()
		if p != nil {
			return nil, p
		}
		view, err := svc.PreviewWatchPartyVisitorInvite(ctx, in.Token)
		if err != nil {
			return nil, visitorProblem(err)
		}
		return &WatchPartyVisitorInvitePreviewOutput{CacheControl: noStore, Body: WatchPartyVisitorInvitePreview{HostName: view.HostName, ExpiresAt: NewInstant(view.ExpiresAt)}}, nil
	})
	accept := publicOp(http.MethodPost, "/accept", "acceptWatchPartyVisitorInvite",
		"Join the link's room as a visitor without an account. Returns a session that reaches only that room and its selected file. Not idempotent: each call admits another visitor.")
	accept.DefaultStatus = http.StatusCreated
	accept.MaxBodyBytes = 4096
	accept.RetrySafety = RetrySafetyNonRetryable
	accept.Errors = append(accept.Errors, http.StatusConflict)
	Register(reg, accept, func(ctx context.Context, in *WatchPartyVisitorAcceptInput) (*WatchPartyVisitorAcceptanceOutput, error) {
		svc, p := reg.watchPartyVisitors()
		if p != nil {
			return nil, p
		}
		deviceName := in.UserAgent
		if runes := []rune(strings.ToValidUTF8(deviceName, "")); len(runes) > 200 {
			deviceName = string(runes[:200])
		} else {
			deviceName = string(runes)
		}
		got, err := svc.AcceptWatchPartyVisitorInvite(withClientDevice(ctx), in.Token, in.Body.DisplayName, deviceName, clientip.FromContext(ctx))
		if err != nil {
			return nil, visitorProblem(err)
		}
		v := got.Visitor
		return &WatchPartyVisitorAcceptanceOutput{CacheControl: noStore, Body: WatchPartyVisitorAcceptance{
			RoomID: v.RoomID, VisitorID: v.VisitorID, DisplayName: v.DisplayName, MemberID: watchtogether.VisitorMemberProfileID(v.VisitorID),
			AccessToken: got.AccessToken, RefreshToken: got.RefreshToken, ExpiresIn: got.ExpiresIn,
			ProfileID: v.HostProfileID, ProfileToken: got.ProfileToken, RoomToken: got.RoomToken, SessionEndsAt: NewInstant(got.SessionEndsAt),
		}}, nil
	})
}
