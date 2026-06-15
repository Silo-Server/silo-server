package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/scanqueue"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/gorilla/websocket"
	"github.com/oklog/ulid/v2"
)

type historyImportActiveLister interface {
	ListActiveRuns(ctx context.Context, userID int) ([]historyimport.Run, error)
	ListAdminActiveRuns(ctx context.Context, sourceID *int) ([]historyimport.Run, error)
}

type taskInfoLister interface {
	ListTasks(includeHidden bool) []taskmanager.TaskInfo
}

type activeScanLister interface {
	ListActive(ctx context.Context) ([]evt.ScanRun, error)
}

type EventsHandler struct {
	hub            *evt.Hub
	jobs           *AdminJobsHandler
	admin          *AdminHandler
	tasks          taskInfoLister
	scans          *evt.ScanRegistry
	persistedScans activeScanLister
	historyImports historyImportActiveLister
	notifications  *notifications.System
}

// SetNotificationsSystem wires the user-notification system: websocket
// handshake tickets and the notifications channel snapshot.
func (h *EventsHandler) SetNotificationsSystem(system *notifications.System) {
	if h != nil {
		h.notifications = system
	}
}

func NewEventsHandler(
	hub *evt.Hub,
	jobs *AdminJobsHandler,
	admin *AdminHandler,
	tasks taskInfoLister,
	scans *evt.ScanRegistry,
	persistedScans *scanqueue.Service,
	historyImports historyImportActiveLister,
) *EventsHandler {
	return &EventsHandler{
		hub:            hub,
		jobs:           jobs,
		admin:          admin,
		tasks:          tasks,
		scans:          scans,
		persistedScans: persistedScans,
		historyImports: historyImports,
	}
}

func (h *EventsHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.hub == nil {
		http.Error(w, "events unavailable", http.StatusServiceUnavailable)
		return
	}

	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Browsers cannot set custom headers on websocket handshakes, so profile
	// identity arrives as a short-lived single-use ticket minted via
	// POST /events/ws-ticket. A connection without a ticket stays unbound and
	// simply cannot subscribe to the profile-scoped notifications channel.
	boundProfileID := ""
	if ticket := r.URL.Query().Get("ticket"); ticket != "" && h.notifications != nil {
		ticketUserID, ticketProfileID, ok := h.notifications.Tickets.Consume(r.Context(), ticket)
		if ok && ticketUserID == claims.UserID {
			boundProfileID = ticketProfileID
		} else {
			// Expired, reused, consumed on a different node, or minted for
			// another user: degrade to an unbound connection instead of
			// failing the handshake. The binding grants nothing on its own —
			// the client retries it when its notifications subscription is
			// rejected — whereas a hard 403 would take down every realtime
			// channel over a notifications-only concern.
			slog.Warn("events: websocket ticket rejected; connection unbound",
				"user_id", claims.UserID)
		}
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	configureWebSocket(conn)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	eventsCh, unsubscribe := h.hub.Subscribe()
	defer unsubscribe()
	startWebSocketPingLoop(ctx, func() error {
		return writeWebSocketControl(conn, websocket.PingMessage, nil)
	})

	allowedChannels := allowedChannelsForRole(claims.Role)
	connectionID := ulid.Make().String()
	if err := writeWebSocketJSON(conn, evt.EventsHelloMessage{
		Type:              "hello",
		SchemaVersion:     1,
		ConnectionID:      connectionID,
		AvailableChannels: allowedChannels,
		RequiredAction:    "subscribe",
	}); err != nil {
		return
	}

	readMessages := make(chan []byte, 8)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, data, readErr := conn.ReadMessage()
			if readErr != nil {
				return
			}
			select {
			case readMessages <- data:
			case <-ctx.Done():
				return
			}
		}
	}()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()

	subscriptions := make(map[evt.EventChannel]struct{})
	subscribedOnce := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-readDone:
			return
		case <-deadline.C:
			if subscribedOnce {
				continue
			}
			writeWebSocketError(conn, "bad_request", "subscribe is required within 5 seconds")
			_ = writeWebSocketControl(
				conn,
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "subscribe required"),
			)
			return
		case data := <-readMessages:
			nextSubs, handled, ok := h.handleEventsClientMessage(conn, r, claims, boundProfileID, data, allowedChannels)
			if !ok {
				return
			}
			if !handled {
				continue
			}
			subscribedOnce = true
			if !deadline.Stop() {
				select {
				case <-deadline.C:
				default:
				}
			}
			subscriptions = nextSubs
		case env, ok := <-eventsCh:
			if !ok {
				return
			}
			if _, subscribed := subscriptions[env.Channel]; !subscribed {
				continue
			}
			if !allowsEventForClaims(claims, boundProfileID, env) {
				continue
			}
			if err := h.writeEventFrame(conn, r, claims, boundProfileID, env); err != nil {
				return
			}
		}
	}
}

func (h *EventsHandler) handleEventsClientMessage(
	conn *websocket.Conn,
	r *http.Request,
	claims *auth.Claims,
	boundProfileID string,
	data []byte,
	allowed []evt.EventChannel,
) (map[evt.EventChannel]struct{}, bool, bool) {
	var base struct {
		Type string `json:"type"`
	}
	if err := readWebSocketJSON(data, &base); err != nil {
		writeWebSocketError(conn, "bad_request", "Malformed JSON")
		_ = writeWebSocketControl(
			conn,
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "malformed json"),
		)
		return nil, false, false
	}
	if base.Type != "subscribe" {
		writeWebSocketError(conn, "bad_request", "Unknown message type")
		_ = writeWebSocketControl(
			conn,
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "unknown message type"),
		)
		return nil, false, false
	}

	var message evt.EventsSubscribeMessage
	if err := readWebSocketJSON(data, &message); err != nil {
		writeWebSocketError(conn, "bad_request", "Invalid subscribe payload")
		_ = writeWebSocketControl(
			conn,
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid subscribe payload"),
		)
		return nil, false, false
	}

	allowedSet := make(map[evt.EventChannel]struct{}, len(allowed))
	for _, channel := range allowed {
		allowedSet[channel] = struct{}{}
	}
	validSet := make(map[evt.EventChannel]struct{}, len(evt.AllChannels))
	for _, channel := range evt.AllChannels {
		validSet[channel] = struct{}{}
	}

	nextSubs := make(map[evt.EventChannel]struct{}, len(message.Channels))
	accepted := make([]evt.EventChannel, 0, len(message.Channels))
	rejected := make([]evt.EventsRejectedChannel, 0)

	for _, channel := range message.Channels {
		if _, ok := validSet[channel]; !ok {
			writeWebSocketError(conn, "bad_request", "Invalid channel")
			_ = writeWebSocketControl(
				conn,
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid channel"),
			)
			return nil, false, false
		}
		if _, ok := allowedSet[channel]; !ok {
			rejected = append(rejected, evt.EventsRejectedChannel{
				Channel: channel,
				Code:    "forbidden",
				Message: "Admin access required",
			})
			continue
		}
		// The notifications channel is profile-scoped: it requires a
		// connection bound to a profile via a websocket ticket.
		if channel == evt.ChannelNotifications && boundProfileID == "" {
			rejected = append(rejected, evt.EventsRejectedChannel{
				Channel: channel,
				Code:    "profile_required",
				Message: "A profile-bound websocket ticket is required",
			})
			continue
		}
		if _, seen := nextSubs[channel]; seen {
			continue
		}
		nextSubs[channel] = struct{}{}
		accepted = append(accepted, channel)
	}

	if err := writeWebSocketJSON(conn, evt.EventsSubscribedMessage{
		Type:      "subscribed",
		RequestID: message.RequestID,
		Channels:  accepted,
		Rejected:  rejected,
	}); err != nil {
		return nil, false, false
	}

	for _, channel := range accepted {
		if err := h.writeSnapshotFrame(conn, r, claims, boundProfileID, channel); err != nil {
			return nil, false, false
		}
	}

	return nextSubs, true, true
}

func allowedChannelsForRole(role string) []evt.EventChannel {
	channels := []evt.EventChannel{
		evt.ChannelCatalog,
		evt.ChannelHistoryImport,
		evt.ChannelUserState,
		evt.ChannelNotifications,
	}
	if role == "admin" {
		channels = append(channels,
			evt.ChannelJobs,
			evt.ChannelSessions,
			evt.ChannelTasks,
			evt.ChannelScans,
			evt.ChannelSettings,
		)
	}
	return channels
}

func allowsEventForClaims(claims *auth.Claims, boundProfileID string, env evt.Envelope) bool {
	if claims == nil {
		return false
	}
	if env.AdminOnly && claims.Role != "admin" {
		return false
	}
	if env.Channel == evt.ChannelNotifications {
		// Notifications are personal: even admins only receive their own
		// profile's deliveries, and only on a profile-bound connection.
		return boundProfileID != "" &&
			env.UserID == claims.UserID &&
			env.ProfileID == boundProfileID
	}
	if env.UserID > 0 && claims.Role != "admin" && env.UserID != claims.UserID {
		return false
	}
	return true
}

func marshalJSON(value any) json.RawMessage {
	if value == nil {
		return json.RawMessage("null")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return data
}

func (h *EventsHandler) snapshotForChannel(
	r *http.Request,
	claims *auth.Claims,
	boundProfileID string,
	channel evt.EventChannel,
) (json.RawMessage, error) {
	switch channel {
	case evt.ChannelCatalog, evt.ChannelUserState:
		return json.RawMessage("null"), nil
	case evt.ChannelNotifications:
		// Recent unread deliveries for the bound profile so reconnecting
		// clients hydrate without a separate REST call. Same row shape as the
		// inbox list API.
		if h == nil || h.notifications == nil || boundProfileID == "" {
			return json.RawMessage("[]"), nil
		}
		rows, err := h.notifications.Deliveries.RecentUnread(r.Context(), boundProfileID, 25)
		if err != nil {
			return nil, err
		}
		return marshalJSON(h.notifications.PayloadsForRows(r.Context(), rows)), nil
	case evt.ChannelJobs:
		if h == nil || h.jobs == nil || h.jobs.repo == nil {
			return json.RawMessage("[]"), nil
		}
		jobs, err := h.jobs.repo.List(r.Context(), adminjob.ListJobsOptions{Limit: 50})
		if err != nil {
			return nil, err
		}
		response := make([]adminJobResponse, 0, len(jobs))
		for _, job := range jobs {
			response = append(response, adminJobToResponse(r, job, h.jobs.store))
		}
		return marshalJSON(response), nil
	case evt.ChannelSessions:
		if h == nil || h.admin == nil {
			return json.RawMessage("[]"), nil
		}
		sessions, err := h.admin.loadPlaybackSessions(r.Context(), r)
		if err != nil {
			return nil, err
		}
		return marshalJSON(sessions), nil
	case evt.ChannelTasks:
		if h == nil || h.tasks == nil {
			return json.RawMessage("[]"), nil
		}
		return marshalJSON(h.tasks.ListTasks(false)), nil
	case evt.ChannelScans:
		if h == nil {
			return json.RawMessage("[]"), nil
		}
		runs := make([]evt.ScanRun, 0)
		if h.persistedScans != nil {
			persisted, err := h.persistedScans.ListActive(r.Context())
			if err != nil {
				return nil, err
			}
			runs = append(runs, persisted...)
		}
		if h.scans != nil {
			runs = append(runs, h.scans.ListActive()...)
		}
		return marshalJSON(runs), nil
	case evt.ChannelHistoryImport:
		if h == nil || h.historyImports == nil {
			return json.RawMessage("[]"), nil
		}
		if claims != nil && claims.Role == "admin" {
			runs, err := h.historyImports.ListAdminActiveRuns(r.Context(), nil)
			if err != nil {
				return nil, err
			}
			return marshalJSON(runs), nil
		}
		runs, err := h.historyImports.ListActiveRuns(r.Context(), claims.UserID)
		if err != nil {
			return nil, err
		}
		return marshalJSON(runs), nil
	default:
		return json.RawMessage("null"), nil
	}
}

func (h *EventsHandler) writeSnapshotFrame(
	conn *websocket.Conn,
	r *http.Request,
	claims *auth.Claims,
	boundProfileID string,
	channel evt.EventChannel,
) error {
	data, err := h.snapshotForChannel(r, claims, boundProfileID, channel)
	if err != nil {
		slog.Error(
			"events: failed to build initial snapshot",
			"channel",
			channel,
			"user_id",
			claims.UserID,
			"error",
			err,
		)
		writeWebSocketError(conn, "internal_error", "Failed to load snapshot")
		return nil
	}
	return writeWebSocketJSON(conn, evt.EventsSnapshotMessage{
		Type:      "snapshot",
		Channel:   channel,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Data:      data,
	})
}

func (h *EventsHandler) writeEventFrame(
	conn *websocket.Conn,
	r *http.Request,
	claims *auth.Claims,
	boundProfileID string,
	env evt.Envelope,
) error {
	data := env.Data
	if len(data) == 0 || (env.Channel == evt.ChannelSessions && env.Event == "sessions.replaced") {
		snapshot, err := h.snapshotForChannel(r, claims, boundProfileID, env.Channel)
		if err != nil {
			// Drop the frame but keep the stream open (same contract as
			// writeSnapshotFrame): durable state covers the gap on the next
			// event or reconnect, while closing the socket tears down every
			// channel the client subscribed to.
			slog.Error("events: failed to build event payload", "channel", env.Channel, "event", env.Event, "error", err)
			return nil
		}
		data = snapshot
	}
	if len(data) == 0 {
		data = json.RawMessage("null")
	}

	return writeWebSocketJSON(conn, evt.EventsEventMessage{
		Type:      "event",
		Channel:   env.Channel,
		Event:     env.Event,
		EventID:   env.EventID,
		Timestamp: env.Timestamp.UTC().Format(time.RFC3339Nano),
		Data:      data,
	})
}
