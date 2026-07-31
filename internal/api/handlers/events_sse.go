package handlers

import (
	"encoding/json"
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

	eventsCh, unsubscribe := h.hub.Subscribe()
	defer unsubscribe()

	writeSSEFrame(w, flusher, "hello", evt.EventsHelloMessage{
		Type:              "hello",
		SchemaVersion:     1,
		ConnectionID:      ulid.Make().String(),
		AvailableChannels: allowed,
		RequiredAction:    "none",
	})

	ping := time.NewTicker(ssePingInterval)
	defer ping.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			writeSSEFrame(w, flusher, "ping", map[string]string{
				"type": "ping",
			})
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
			writeSSEFrame(w, flusher, string(env.Channel), env)
		}
	}
}

// resolveSSEChannels intersects the caller's requested channels with what its
// role allows. An empty request means "everything allowed". A requested channel
// the role forbids is dropped silently rather than failing the whole stream, so
// one forbidden channel does not deny a caller the rest.
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

// writeSSEFrame writes one named SSE event and flushes it. data is emitted as a
// single compact JSON line, as the SSE framing requires.
func writeSSEFrame(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if _, err := w.Write([]byte("event: " + event + "\ndata: " + string(data) + "\n\n")); err != nil {
		return
	}
	flusher.Flush()
}
