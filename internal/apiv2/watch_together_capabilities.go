package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

// WatchTogetherCapabilityService reports whether rooms are served at all.
type WatchTogetherCapabilityService interface {
	WatchTogetherAvailable() bool
}

type WatchTogetherCapabilities struct {
	Capability
	StagedSelection     bool   `json:"staged_selection" doc:"Lobbies stage an item and the host starts it explicitly"`
	LobbyReady          bool   `json:"lobby_ready" doc:"Members mark themselves ready in the lobby over the room socket"`
	ConnectionReplaced  bool   `json:"connection_replaced" doc:"Displaced v2 room sockets receive a terminal connection_replaced message before closing"`
	SelectionModeSwitch bool   `json:"selection_mode_switch" doc:"The host may switch a lobby between host picks and voting"`
	MemberState         bool   `json:"member_state" doc:"Member watch state and picker rows are computed server-side"`
	Picker              bool   `json:"picker"`
	VoteHostOverride    bool   `json:"vote_host_override" doc:"The host may promote any suggestion in a vote room, not only the leader"`
	StopPlayback        bool   `json:"stop_playback" doc:"The host may stop playback and return the room to the lobby without ending it"`
	VisitorInvites      bool   `json:"visitor_invites" doc:"The host may create invite links that let people without an account join the room as visitors"`
	MaxMemberStateIDs   int    `json:"max_member_state_ids"`
	SocketProtocol      string `json:"socket_protocol"`
}
type WatchTogetherCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         WatchTogetherCapabilities
}

func registerWatchTogetherCapabilities(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/watch-together/capabilities", "getWatchTogetherCapabilities", "realtime", "Read which watch-together room behaviors this server supports."), Class: ClassAuthenticated, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, _ *CapabilityInput) (*WatchTogetherCapabilitiesOutput, error) {
		svc := reg.deps.WatchTogetherCapability
		if svc == nil || !svc.WatchTogetherAvailable() {
			return &WatchTogetherCapabilitiesOutput{Body: WatchTogetherCapabilities{Capability: Capability{State: StateNotConfigured}}}, nil
		}
		socketProtocol := ""
		if reg.deps.WatchTogetherSocket != nil {
			socketProtocol = watchtogether.RoomSocketProtocol
		}
		memberState := reg.deps.WatchTogetherMemberState != nil && reg.deps.CatalogAccess != nil
		maxIDs := 0
		if memberState {
			maxIDs = watchtogether.MaxMemberStateIDs
		}
		return &WatchTogetherCapabilitiesOutput{Body: WatchTogetherCapabilities{
			Capability:          Capability{Allowed: new(capabilityLoginAllowed(ctx))},
			StagedSelection:     reg.deps.WatchTogetherStage != nil && reg.deps.WatchTogetherStart != nil,
			LobbyReady:          reg.deps.WatchTogetherSocket != nil,
			ConnectionReplaced:  reg.deps.WatchTogetherSocket != nil,
			SelectionModeSwitch: reg.deps.WatchTogetherSelectionMode != nil,
			MemberState:         memberState,
			Picker:              reg.deps.WatchTogetherPicker != nil && reg.deps.CatalogAccess != nil,
			VoteHostOverride:    reg.deps.WatchTogetherSuggestionPromote != nil,
			StopPlayback:        reg.deps.WatchTogetherStop != nil,
			VisitorInvites:      reg.deps.WatchTogetherSocket != nil && reg.deps.WatchPartyVisitors != nil && reg.deps.WatchPartyVisitors.WatchPartyVisitorsAvailable(),
			MaxMemberStateIDs:   maxIDs,
			SocketProtocol:      socketProtocol,
		}}, nil
	})
}

func (c WatchTogetherCapabilities) capabilityState() string {
	if c.State != "" {
		return c.State
	}
	return StateAvailable
}

// watchPartyMember is the caller as a room member: the profile-scoped viewer,
// or for a Watch Party visitor session the host's account with the
// visitor's member profile id. The auth gate already confined a visitor to
// its own room's routes.
func watchPartyMember(ctx context.Context) (int, string, *Problem) {
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return 0, "", p
	}
	if claims := claimsFrom(ctx); claims.IsWatchPartyVisitor() {
		if claims.WatchPartyVisitorID == "" {
			return 0, "", NewProblem(TypePermissionDenied, "This visitor session names no visitor.")
		}
		return user, watchtogether.VisitorMemberProfileID(claims.WatchPartyVisitorID), nil
	}
	return user, profile, nil
}
