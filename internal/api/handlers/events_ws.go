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
	"github.com/Silo-Server/silo-server/internal/presence"
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

// notificationUnreadCounter is the subset of notifications.Service used by EventsHandler.
type notificationUnreadCounter interface {
	UnreadCount(ctx context.Context, userID int, profileID string, childSafe bool) (int, error)
}

type EventsHandler struct {
	hub            *evt.Hub
	jobs           *AdminJobsHandler
	admin          *AdminHandler
	tasks          taskInfoLister
	scans          *evt.ScanRegistry
	persistedScans activeScanLister
	historyImports historyImportActiveLister
	notifications  notificationUnreadCounter
	presence       presence.Registry
}

func NewEventsHandler(
	hub *evt.Hub,
	jobs *AdminJobsHandler,
	admin *AdminHandler,
	tasks taskInfoLister,
	scans *evt.ScanRegistry,
	persistedScans *scanqueue.Service,
	historyImports historyImportActiveLister,
	notificationsSvc *notifications.Service,
	presenceReg presence.Registry,
) *EventsHandler {
	return &EventsHandler{
		hub:            hub,
		jobs:           jobs,
		admin:          admin,
		tasks:          tasks,
		scans:          scans,
		persistedScans: persistedScans,
		historyImports: historyImports,
		notifications:  notificationsSvc,
		presence:       presenceReg,
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

	if h.presence != nil {
		release := h.presence.Add(ctx, claims.UserID)
		defer release()
	}

	startWebSocketPingLoop(ctx, func() error {
		if h.presence != nil {
			h.presence.Refresh(ctx, claims.UserID)
		}
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
			nextSubs, handled, ok := h.handleEventsClientMessage(conn, r, claims, data, allowedChannels)
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
			if !allowsEventForClaims(claims, env) {
				continue
			}
			if err := h.writeEventFrame(conn, r, claims, env); err != nil {
				return
			}
		}
	}
}

func (h *EventsHandler) handleEventsClientMessage(
	conn *websocket.Conn,
	r *http.Request,
	claims *auth.Claims,
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
		if err := h.writeSnapshotFrame(conn, r, claims, channel); err != nil {
			return nil, false, false
		}
	}

	return nextSubs, true, true
}

func allowedChannelsForRole(role string) []evt.EventChannel {
	channels := []evt.EventChannel{
		evt.ChannelCatalog,
		evt.ChannelHistoryImport,
		evt.ChannelNotifications,
		evt.ChannelRequests,
		evt.ChannelUserState,
	}
	if role == "admin" {
		channels = append(channels,
			evt.ChannelJobs,
			evt.ChannelSessions,
			evt.ChannelTasks,
			evt.ChannelScans,
		)
	}
	return channels
}

func allowsEventForClaims(claims *auth.Claims, env evt.Envelope) bool {
	if claims == nil {
		return false
	}
	if env.AdminOnly && claims.Role != "admin" {
		return false
	}
	if env.UserID > 0 && env.UserID != claims.UserID {
		// The notifications and requests channels carry per-user private content
		// (inbox titles/bodies, requested media names). Nobody — including
		// admins — may receive another user's events on them. Other channels
		// keep the admin cross-user monitoring bypass.
		if claims.Role != "admin" || isPersonalChannel(env.Channel) {
			return false
		}
	}
	return true
}

// isPersonalChannel reports whether a channel carries per-user private content
// that must never be delivered across user boundaries, even to admins.
func isPersonalChannel(c evt.EventChannel) bool {
	return c == evt.ChannelNotifications || c == evt.ChannelRequests
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
	channel evt.EventChannel,
) (json.RawMessage, error) {
	switch channel {
	case evt.ChannelCatalog, evt.ChannelUserState:
		return json.RawMessage("null"), nil
	case evt.ChannelNotifications:
		if h == nil || h.notifications == nil {
			return json.RawMessage(`{"unread_count":0}`), nil
		}
		// Profile ID is read directly from the WS upgrade request header because
		// the events/ws route does not go through the RequireProfile middleware.
		// childSafe is intentionally false here: the EventsHandler has no access
		// to a profile store, so the WS snapshot is a conservative approximation.
		// The REST GET /notifications/unread-count endpoint is authoritative for
		// child-safe profile filtering.
		profileID := r.Header.Get("X-Profile-Id")
		count, err := h.notifications.UnreadCount(r.Context(), claims.UserID, profileID, false)
		if err != nil {
			return nil, err
		}
		return marshalJSON(struct {
			UnreadCount int `json:"unread_count"`
		}{UnreadCount: count}), nil
	case evt.ChannelRequests:
		// Request state is fetched via REST; the WS channel only carries
		// lifecycle events. Return an empty object as the initial snapshot.
		return json.RawMessage(`{}`), nil
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
	channel evt.EventChannel,
) error {
	data, err := h.snapshotForChannel(r, claims, channel)
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
	env evt.Envelope,
) error {
	data := env.Data
	if len(data) == 0 || (env.Channel == evt.ChannelSessions && env.Event == "sessions.replaced") {
		snapshot, err := h.snapshotForChannel(r, claims, env.Channel)
		if err != nil {
			slog.Error("events: failed to build event payload", "channel", env.Channel, "event", env.Event, "error", err)
			return err
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
