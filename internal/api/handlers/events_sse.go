package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

// ssePingInterval keeps idle connections alive through proxies that would
// otherwise time them out.
const ssePingInterval = 25 * time.Second

// sseFrameWriteDeadline bounds how long a single frame write is allowed to
// block. HandleSSE clears the connection's write deadline entirely so a
// healthy long-lived stream survives past the server's 120s WriteTimeout, but
// that alone would let a stalled-but-not-closed client hold a hub
// subscription (and its buffered channel) open forever. writeSSEFrame renews
// a bounded deadline before every write instead. ssePingInterval is used as
// the bound: a client that cannot absorb a single frame within one ping
// interval is not keeping up, so this catches a stalled peer about as
// promptly as the ping cadence itself would notice one, without cutting off
// a merely slow-but-healthy connection mid-write.
const sseFrameWriteDeadline = ssePingInterval

// sseRequiredActionNone tells clients no handshake is expected, unlike the
// WebSocket hello which requires a subscribe frame.
const sseRequiredActionNone = "none"

// ssePingMessage is the keepalive frame. Typed rather than an inline map so
// the ping payload has one definition, matching the other wire types here.
type ssePingMessage struct {
	Type string `json:"type"`
}

// HandleSSE streams hub events as Server-Sent Events.
//
// This is a read-only sibling of HandleWebSocket for consumers that only need
// to observe. Channel selection is a query parameter rather than a handshake:
// SSE is one-way, so there is nothing to negotiate, and no subscribe deadline
// is needed to tell a live client from a stalled one.
//
// Profile-scoped channels are not offered here — they depend on the ws-ticket
// binding, which SSE does not use.
func (h *EventsHandler) HandleSSE(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.hub == nil {
		http.Error(w, "events unavailable", http.StatusServiceUnavailable)
		return
	}

	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	allowed := allowedChannelsForRole(claims.Role)
	subscribed := resolveSSEChannels(r.URL.Query().Get("channels"), allowed)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// openresty fronts several deployments and buffers the stream without this.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// The shared server (cmd/silo/main.go) sets WriteTimeout: 120s on this
	// listener. net/http applies that as a hard per-connection deadline set
	// once when the request is read; handler writes and flushes never renew
	// it, so without this the stream would be force-closed at ~120s no
	// matter how healthy it is. Clear it for this connection only, the same
	// way startStandaloneServer (cmd/silo/main.go) sets WriteTimeout: 0 for
	// its own long-lived streams — HandleWebSocket never needs this because
	// wsUpgrader.Upgrade hijacks the raw connection and escapes the server's
	// deadline management entirely, but SSE stays on the standard
	// ResponseWriter path.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		// Not fatal on its own: some wrapping middleware may not support
		// per-connection deadlines (returns http.ErrNotSupported). Log and
		// keep streaming; the connection simply remains subject to the
		// server's WriteTimeout in that case.
		slog.WarnContext(r.Context(), "events: sse write deadline not cleared; stream may be closed by the server's WriteTimeout", "component", "api", "error", err)
	}

	eventsCh, unsubscribe := h.hub.Subscribe()
	defer unsubscribe()

	ctx := r.Context()

	if err := writeSSEFrame(ctx, w, flusher, "hello", evt.EventsHelloMessage{
		Type:              "hello",
		SchemaVersion:     1,
		ConnectionID:      ulid.Make().String(),
		AvailableChannels: allowed,
		RequiredAction:    sseRequiredActionNone,
	}); err != nil {
		return
	}

	ping := time.NewTicker(ssePingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if err := writeSSEFrame(ctx, w, flusher, "ping", ssePingMessage{Type: "ping"}); err != nil {
				return
			}
		case env, ok := <-eventsCh:
			if !ok {
				return
			}
			if _, want := subscribed[env.Channel]; !want {
				continue
			}
			if !allowsEventForClaims(claims, "", env) {
				continue
			}
			if err := writeSSEFrame(ctx, w, flusher, string(env.Channel), sseEventMessage(env)); err != nil {
				return
			}
		}
	}
}

// sseEventMessage projects a hub envelope into the same wire-safe shape
// HandleWebSocket sends (evt.EventsEventMessage), rather than marshaling the
// raw evt.Envelope. The envelope carries internal plumbing and other users'
// identifiers — source_id, admin_only, target_plugin_id, user_id, profile_id
// — that must never reach the wire; the two transports agree on one public
// event schema.
func sseEventMessage(env evt.Envelope) evt.EventsEventMessage {
	data := env.Data
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	return evt.EventsEventMessage{
		Type:      "event",
		Channel:   env.Channel,
		Event:     env.Event,
		EventID:   env.EventID,
		Timestamp: env.Timestamp.UTC().Format(time.RFC3339Nano),
		Data:      data,
	}
}

// resolveSSEChannels intersects the caller's requested channels with what its
// role allows. An empty request means "everything allowed". A requested
// channel the role forbids, or that isn't recognized at all (a malformed or
// bogus value), is dropped silently rather than failing the whole stream or
// widening access — deny-by-default, so a garbage `channels` value yields an
// empty subscription (hello and pings only, no events) instead of an error.
func resolveSSEChannels(requested string, allowed []evt.EventChannel) map[evt.EventChannel]struct{} {
	allowedSet := make(map[evt.EventChannel]struct{}, len(allowed))
	for _, ch := range allowed {
		allowedSet[ch] = struct{}{}
	}

	requested = strings.TrimSpace(requested)
	if requested == "" {
		return allowedSet
	}

	selected := make(map[evt.EventChannel]struct{})
	for _, raw := range strings.Split(requested, ",") {
		ch := evt.EventChannel(strings.TrimSpace(raw))
		if ch == "" {
			continue
		}
		if _, ok := allowedSet[ch]; ok {
			selected[ch] = struct{}{}
		}
	}
	return selected
}

// writeSSEFrame writes one named SSE event and flushes it. data is emitted as
// a single compact JSON line, as the SSE framing requires. It returns an
// error if the payload could not be marshaled or the frame could not be
// written, so the caller can drop a dead connection promptly instead of
// waiting on context cancellation — mirroring HandleWebSocket's frame-write
// discipline (events_ws.go), which returns immediately on any write failure.
//
// Before writing, it renews a bounded per-write deadline (sseFrameWriteDeadline).
// HandleSSE clears the connection's write deadline once so the server's
// WriteTimeout doesn't kill a healthy stream at 120s, but that must not mean
// no deadline ever applies again — otherwise a stalled-but-not-closed peer
// would block this Write forever and hold its hub subscription open
// indefinitely. Renewing a short deadline before every write bounds each
// individual frame instead of the whole connection.
func writeSSEFrame(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(sseFrameWriteDeadline)); err != nil {
		// Not fatal on its own, same as the initial clear in HandleSSE: some
		// wrapping middleware doesn't support per-connection deadlines, and
		// httptest.ResponseRecorder (used throughout events_sse_test.go)
		// returns http.ErrNotSupported. Log and write anyway; the frame just
		// isn't individually bounded in that case.
		slog.WarnContext(ctx, "events: sse frame write deadline not set", "component", "api", "error", err)
	}
	if _, err := w.Write([]byte("event: " + event + "\ndata: " + string(data) + "\n\n")); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
