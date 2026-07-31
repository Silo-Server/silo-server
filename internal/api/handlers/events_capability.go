package handlers

import "net/http"

// eventsCapabilityResponse reports which realtime transports the events
// subsystem offers, for feature detection rather than version sniffing (see
// the v1 API rules in CLAUDE.md). It deliberately does not repeat the
// caller's available channels — the hello frame both transports send already
// carries those (evt.EventsHelloMessage.AvailableChannels), and duplicating
// them here would invite the two to drift.
type eventsCapabilityResponse struct {
	// SchemaVersion matches the schema_version every hello frame sends
	// (evt.EventsHelloMessage), not this response's own shape. A client that
	// has already parsed one transport's hello frame can skip re-deriving
	// compatibility from this field.
	SchemaVersion int                       `json:"schema_version"`
	SSE           eventsTransportCapability `json:"sse"`
	WebSocket     eventsTransportCapability `json:"websocket"`
}

// eventsTransportCapability describes one transport over the events hub.
type eventsTransportCapability struct {
	Supported bool   `json:"supported"`
	Path      string `json:"path"`
}

// HandleCapability handles GET /events/capability. Clients use this to
// detect the SSE transport (added as a read-only sibling of the websocket
// handshake) without probing the URL or comparing release versions.
func (h *EventsHandler) HandleCapability(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, eventsCapabilityResponse{
		SchemaVersion: 1,
		SSE: eventsTransportCapability{
			Supported: true,
			Path:      "/api/v1/events/sse",
		},
		WebSocket: eventsTransportCapability{
			Supported: true,
			Path:      "/api/v1/events/ws",
		},
	})
}
