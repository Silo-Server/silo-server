package handlers

import (
	"encoding/json"
	"net/http"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// eventsCapabilityResponse describes how a client may subscribe to the events
// websocket.
//
// Clients are expected to run a current build rather than negotiate down to an
// old server, so this is not a branch-on-capability contract. It is how a
// client tells the difference between "this server does not do that" and "the
// connection failed", which is what lets it say the deployment is out of date
// instead of failing opaquely: a server predating declared channels ignores
// ?channels=, answers required_action:"subscribe", and closes the connection
// after the grace period, which is indistinguishable from a broken socket.
type eventsCapabilityResponse struct {
	SchemaVersion int `json:"schema_version"`
	// SubscribeFrame reports the handshake: connect, then send a subscribe
	// frame. Always true; named so a future removal is detectable rather than
	// silent.
	SubscribeFrame bool `json:"subscribe_frame"`
	// DeclaredChannels reports that ?channels= is honored on connect, that such
	// a connection is exempt from the subscribe grace period, and that its
	// hello frame carries required_action:"none".
	DeclaredChannels bool `json:"declared_channels"`
	// SubscribeGracePeriodSeconds is how long a connection that declared no
	// channels may stay silent before it is closed. 0 would mean no deadline.
	SubscribeGracePeriodSeconds int `json:"subscribe_grace_period_seconds"`
	// Channels is every channel this server knows, independent of role. What
	// the caller may actually subscribe to arrives as available_channels in the
	// hello frame, which is role-filtered.
	Channels []evt.EventChannel `json:"channels"`
}

// HandleCapability reports the events websocket's subscription capabilities.
//
// Per the v1 rules, new functionality is feature-detected rather than inferred
// from a version. This follows the existing per-subsystem convention
// (/notifications/capability, /playback/capability, /downloads/capability).
//
// A client that finds declared_channels false is talking to a server older than
// its own expectations; the useful response is to tell the user to update the
// server, not to silently fall back.
func (h *EventsHandler) HandleCapability(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(eventsCapabilityResponse{
		SchemaVersion:               1,
		SubscribeFrame:              true,
		DeclaredChannels:            true,
		SubscribeGracePeriodSeconds: int(subscribeGracePeriod.Seconds()),
		Channels:                    evt.AllChannels,
	})
}
