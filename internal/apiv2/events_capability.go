package apiv2

import (
	"context"
	"net/http"
	"slices"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type EventsCapabilityService interface {
	EventsCapability() handlers.EventsCapability
}

type EventsCapabilities struct {
	Capability
	SchemaVersion               int      `json:"schema_version"`
	SubscribeFrame              bool     `json:"subscribe_frame"`
	DeclaredChannels            bool     `json:"declared_channels"`
	SubscribeGracePeriodSeconds int      `json:"subscribe_grace_period_seconds"`
	MaxRequestedChannels        int      `json:"max_requested_channels"`
	Channels                    []string `json:"channels"`
}
type EventsCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         EventsCapabilities
}

func registerEventsCapability(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/events/capabilities", "getEventsCapabilities", "realtime", "Read realtime subscription channels and limits."), Class: ClassAuthenticated, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, _ *CapabilityInput) (*EventsCapabilitiesOutput, error) {
		if reg.deps.EventsCapability == nil {
			return &EventsCapabilitiesOutput{Body: EventsCapabilities{Capability: Capability{State: StateNotConfigured}, Channels: []string{}}}, nil
		}
		value := reg.deps.EventsCapability.EventsCapability()
		out := EventsCapabilities{Capability: Capability{Allowed: new(eventsSocketAllowed(ctx))}, SchemaVersion: value.SchemaVersion, SubscribeFrame: value.SubscribeFrame, DeclaredChannels: value.DeclaredChannels, SubscribeGracePeriodSeconds: value.SubscribeGracePeriodSeconds, MaxRequestedChannels: value.MaxRequestedChannels, Channels: make([]string, 0, len(value.Channels))}
		for _, channel := range value.Channels {
			out.Channels = append(out.Channels, string(channel))
		}
		return &EventsCapabilitiesOutput{Body: out}, nil
	})
}

// eventsSocketAllowed reports whether the caller may mint an events socket
// ticket: a current login session, or an administrator's admin:monitor:read
// API key sent with no profile. This operation resolves no profile, so the
// header itself is read.
func eventsSocketAllowed(ctx context.Context) bool {
	if capabilityLoginAllowed(ctx) {
		return true
	}
	claims := claimsFrom(ctx)
	return claims != nil && claims.TokenType == auth.TokenTypeAPIKey && claims.Role == models.RoleAdmin && slices.Contains(claims.APIKeyScopes, auth.ScopeAdminMonitorRead) && !declaresProfile(ctx)
}

func (c EventsCapabilities) capabilityState() string { return StateAvailable }

func declaresProfile(ctx context.Context) bool {
	r := requestFrom(ctx)
	return r != nil && r.Header.Get(profileHeader) != ""
}
