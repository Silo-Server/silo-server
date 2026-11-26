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
	Register(reg, withOrigins(op, grantedCapability), func(ctx context.Context, _ *CapabilityInput) (*WatchTogetherCapabilitiesOutput, error) {
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
		// A host flag describes the session asking: false for one to which
		// every host operation it governs is refused (capabilityMembers).
		host := func(member string) bool { return reg.memberPermitted(ctx, "getWatchTogetherCapabilities "+member) }
		return &WatchTogetherCapabilitiesOutput{Body: WatchTogetherCapabilities{
			Capability:          Capability{Allowed: new(capabilityLoginAllowed(ctx))},
			StagedSelection:     reg.deps.WatchTogetherStage != nil && reg.deps.WatchTogetherStart != nil && host("staged_selection"),
			LobbyReady:          reg.deps.WatchTogetherSocket != nil,
			ConnectionReplaced:  reg.deps.WatchTogetherSocket != nil,
			SelectionModeSwitch: reg.deps.WatchTogetherSelectionMode != nil && host("selection_mode_switch"),
			MemberState:         memberState,
			Picker:              reg.deps.WatchTogetherPicker != nil && reg.deps.CatalogAccess != nil,
			VoteHostOverride:    reg.deps.WatchTogetherSuggestionPromote != nil && host("vote_host_override"),
			StopPlayback:        reg.deps.WatchTogetherStop != nil && host("stop_playback"),
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
