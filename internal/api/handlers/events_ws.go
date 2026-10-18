package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/adminjob"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/models"
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

const maxRealtimeScanSnapshotRuns = 500

// subscribeGracePeriod bounds how long a connection that is not subscribed to
// anything may stay silent before it is closed. It exists to distinguish a
// client mid-handshake from one that connected and stalled; a connection that
// arrived with a usable ?channels= selection is never put on this clock.
const subscribeGracePeriod = 5 * time.Second

// maxRequestedChannels bounds how many distinct channels one selection may
// name. Every refusal is echoed back with its channel name, so without a cap a
// selection of many unknown names is an amplifier: the request is bounded by
// the header/frame limit, the answer was not. Any real client names a subset of
// the ten client channels; the slack is only so a caller learns it overshot
// rather than silently losing channels.
const maxRequestedChannels = 32

// maxChannelNameLength bounds the channel name echoed in a rejection. A name
// longer than any real channel is garbage by definition, and quoting it back in
// full is the second half of the same amplification.
const maxChannelNameLength = 64

// maxEventsFrameBytes bounds an inbound frame on this socket. The only message
// it accepts is a subscribe frame naming at most maxRequestedChannels channels,
// which is orders of magnitude smaller; the limit exists so a client cannot
// make the server buffer an arbitrarily large frame before it is rejected.
const maxEventsFrameBytes = 64 * 1024

// required_action values in the hello frame: whether the client still owes a
// subscribe frame, or already holds a subscription it declared on the URL.
const (
	requiredActionSubscribe = "subscribe"
	requiredActionNone      = "none"
)

// frameTypeSubscribed is the acknowledgement both selection paths answer with.
const frameTypeSubscribed = "subscribed"

type activeScanLister interface {
	ListActiveSnapshot(ctx context.Context, limit int) ([]evt.ScanRun, error)
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
	// sessionRoles and sessionCheckInterval drive the v1 connection's
	// periodic login-session check; see SetSessionRoles.
	sessionRoles         sessionRoleChecker
	sessionCheckInterval time.Duration
	// profiles re-verifies the PIN proof of the profile a connection holding
	// the admin channels acts as; see SetProfileVerifier.
	profiles apimw.ViewerResolver
}

// sessionRoleChecker reports whether a login session is still active and its
// authority, including the current role of its account;
// auth.SessionRepository implements it.
type sessionRoleChecker interface {
	ActiveSession(ctx context.Context, sessionID string) (authority auth.SessionAuthority, active bool, err error)
}

// SetSessionRoles makes the v1 events socket recheck its login session. The
// connection picks its channels from the access token's role once, at the
// handshake, so it closes when the session ends or the account's role
// changes; the client reconnects with a refreshed token.
func (h *EventsHandler) SetSessionRoles(sessions sessionRoleChecker) {
	if h != nil {
		h.sessionRoles = sessions
	}
}

// SetProfileVerifier makes the v1 events socket re-verify, at the handshake
// and on every session check, the PIN proof of the profile a connection on an
// admin account acts as, so changing that profile's PIN takes the admin
// channels away as it does on the v2 socket.
func (h *EventsHandler) SetProfileVerifier(profiles apimw.ViewerResolver) {
	if h != nil {
		h.profiles = profiles
	}
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
	var bound notifications.Ticket
	if ticket := r.URL.Query().Get("ticket"); ticket != "" && h.notifications != nil {
		binding, ok := h.notifications.Tickets.Consume(r.Context(), ticket)
		if ok && binding.UserID == claims.UserID {
			bound = binding
		} else {
			// Expired, reused, consumed on a different node, or minted for
			// another user: degrade to an unbound connection instead of
			// failing the handshake, which would take down every realtime
			// channel. An unbound connection cannot subscribe to
			// notifications, and on an admin account it is judged as acting
			// with no profile below, so a household that requires a profile
			// gets the user channels. Both fail closed: the client gets the
			// binding back by reconnecting with a fresh ticket, which it does
			// when its notifications subscription is rejected.
			slog.WarnContext(r.Context(), "events: websocket ticket rejected; connection unbound", "component", "api",
				"user_id", claims.UserID)
		}
	}

	// Admin channels and other accounts' events follow the acting profile, as
	// on the v2 socket: another profile on an admin's account connects with
	// the user role. A handshake that declares no profile acts as the profile
	// its ticket was minted for, which the viewer gate verified then. Either
	// way the profile's PIN proof is re-verified here and on every session
	// check. The session check below still compares the account's real role.
	boundProfileID := bound.ProfileID
	effective := claims
	var stillActingAdmin func(context.Context) (bool, error)
	if claims.Role == models.RoleAdmin {
		stillActingAdmin = apimw.ActingAdmin
		actingProfileID, profileToken := apimw.GetProfileID(r.Context()), r.Header.Get("X-Profile-Token")
		if actingProfileID == "" && boundProfileID != "" {
			actingProfileID, profileToken = boundProfileID, bound.ProfileToken
			stillActingAdmin = func(ctx context.Context) (bool, error) { return apimw.ActingAdminAs(ctx, boundProfileID) }
		}
		stillActingAdmin = h.withProfileProof(stillActingAdmin, claims, actingProfileID, profileToken)
		actingAdmin, err := stillActingAdmin(r.Context())
		if err != nil {
			http.Error(w, "failed to verify active profile", http.StatusInternalServerError)
			return
		}
		if !actingAdmin {
			demoted := *claims
			demoted.Role = models.RoleUser
			effective = &demoted
			stillActingAdmin = nil
		}
	}

	if h.sessionRoles != nil && claims.SessionID != "" {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go h.closeOnSessionChange(ctx, cancel, claims, stillActingAdmin)
		r = r.WithContext(ctx)
	}
	h.serveWebSocket(w, r, effective, boundProfileID, wsUpgrader, nil)
}

// withProfileProof narrows decide to while profileID's PIN proof still
// verifies for the connection's login session. The proof is bound to the
// profile's PIN revision, so a PIN change, or a PIN newly set on the profile,
// ends it. With no verifier wired, or no profile, decide is returned as is.
func (h *EventsHandler) withProfileProof(decide func(context.Context) (bool, error), claims *auth.Claims, profileID, profileToken string) func(context.Context) (bool, error) {
	if h.profiles == nil || profileID == "" {
		return decide
	}
	input := access.ResolveInput{
		UserID:       claims.UserID,
		SessionID:    claims.SessionID,
		ProfileID:    profileID,
		ProfileToken: profileToken,
		// API keys carry no PIN proof by design, as at the viewer gate.
		SkipPINVerification: claims.TokenType == auth.TokenTypeAPIKey,
	}
	return func(ctx context.Context) (bool, error) {
		scope, err := h.profiles.Resolve(ctx, input)
		if errors.Is(err, access.ErrProfileUnverified) || errors.Is(err, access.ErrProfileNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !scope.ProfileVerified {
			return false, nil
		}
		return decide(ctx)
	}
}

// closeOnSessionChange cancels a v1 connection once its login session is no
// longer active or the account's role differs from the access token's, and,
// for a connection holding the admin channels, once stillActingAdmin stops
// holding (say the household gained a PIN, or the acting profile's PIN
// changed). A failed check is skipped; the next one decides.
func (h *EventsHandler) closeOnSessionChange(ctx context.Context, cancel context.CancelFunc, claims *auth.Claims, stillActingAdmin func(context.Context) (bool, error)) {
	interval := h.sessionCheckInterval
	if interval <= 0 {
		interval = eventsSessionCheckInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkCtx, stop := context.WithTimeout(ctx, 2*time.Second)
			changed := h.sessionChanged(checkCtx, claims, stillActingAdmin)
			stop()
			if changed {
				cancel()
				return
			}
		}
	}
}

// sessionChanged reports whether one recheck found the connection's access
// gone: the session ended, the account's role changed, or stillActingAdmin
// (when set) no longer holds. A failed session lookup reports no change; a
// failed acting-admin decision counts as lost, so a connection holding the
// administrator channels fails closed like the HTTP routes and reconnects.
func (h *EventsHandler) sessionChanged(ctx context.Context, claims *auth.Claims, stillActingAdmin func(context.Context) (bool, error)) bool {
	authority, active, err := h.sessionRoles.ActiveSession(ctx, claims.SessionID)
	// A remote-playback handoff never holds a v1 socket.
	if err == nil && (!active || authority.Role != claims.Role || authority.IsRemotePlayback()) {
		return true
	}
	if stillActingAdmin == nil {
		return false
	}
	acting, err := stillActingAdmin(ctx)
	return err != nil || !acting
}

// serveWebSocket runs one events connection. When accessChanged closes, the
// loop tells the client its access changed and closes the connection with
// EventsCloseAccessChanged; a nil channel never fires.
func (h *EventsHandler) serveWebSocket(w http.ResponseWriter, r *http.Request, claims *auth.Claims, boundProfileID string, upgrader websocket.Upgrader, accessChanged <-chan struct{}) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// The Android/KMP client has a long history of silent handshake
		// failures here (gorilla writes the 4xx itself); log the exact
		// upgrade-relevant request shape so a failing client is diagnosable
		// from the server alone.
		slog.WarnContext(r.Context(), "events websocket upgrade failed", "component", "api",
			"error", err,
			"user_id", claims.UserID,
			"proto", r.Proto,
			"method", r.Method,
			"connection_header", r.Header.Get("Connection"),
			"upgrade_header", r.Header.Get("Upgrade"),
			"ws_version", r.Header.Get("Sec-Websocket-Version"),
			"ws_key_present", r.Header.Get("Sec-Websocket-Key") != "",
			"user_agent", r.UserAgent(),
		)
		return
	}
	defer conn.Close()
	stopClose := context.AfterFunc(r.Context(), func() { _ = conn.Close() })
	defer stopClose()
	configureWebSocket(conn)
	conn.SetReadLimit(maxEventsFrameBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	eventsCh, unsubscribe := h.hub.Subscribe()
	defer unsubscribe()
	startWebSocketPingLoop(ctx, func() error {
		return writeWebSocketControl(conn, websocket.PingMessage, nil)
	})

	allowedChannels := allowedChannelsForClaims(claims)

	// A connection may declare its channels on the URL instead of sending a
	// subscribe frame. That is all an observer needs — subscribe is the only
	// inbound message this endpoint accepts — so declaring up front lets a
	// read-only client skip the handshake, the grace period, and the write
	// half of the socket entirely.
	declaredChannels, declared := parseDeclaredChannels(r.URL.Query())

	// Resolved before the hello frame so required_action can report what the
	// connection actually owes. Resolution is pure — no query, no I/O — so
	// nothing here can stall ahead of the reader that starts below; only the
	// snapshots the accepted channels produce can, and those still run after
	// it.
	declaredSubs := make(map[evt.EventChannel]struct{})
	var declaredAccepted []evt.EventChannel
	var declaredRejected []evt.EventsRejectedChannel
	if declared {
		declaredSubs, declaredAccepted, declaredRejected =
			resolveChannelSelection(declaredChannels, allowedChannels, boundProfileID)
	}

	// A declaration that yielded no subscription discharges nothing: the client
	// still owes a subscribe frame, and saying "none" would send it to wait
	// silently for events on a connection due to be closed in five seconds.
	requiredAction := requiredActionSubscribe
	if len(declaredSubs) > 0 {
		requiredAction = requiredActionNone
	}
	connectionID := ulid.Make().String()
	if err := writeWebSocketJSON(conn, evt.EventsHelloMessage{
		Type:              "hello",
		SchemaVersion:     1,
		ConnectionID:      connectionID,
		AvailableChannels: allowedChannels,
		RequiredAction:    requiredAction,
	}); err != nil {
		return
	}

	// The reader starts before any snapshot query runs. configureWebSocket
	// installs an absolute read deadline that only pongs extend, and gorilla
	// processes pongs solely inside ReadMessage — so a snapshot slow enough to
	// outlast the deadline (a loaded jobs/sessions/scans query) would kill an
	// otherwise healthy connection the moment reading began. The handshake path
	// is safe for free, building its snapshots downstream of an active reader;
	// the declared path has to arrange that ordering deliberately.
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

	subscriptions := declaredSubs
	if declared {
		// The same subscribed frame the handshake produces, so a client sees
		// one acknowledgement shape however it selected its channels — and
		// still learns which of its requests were refused, and why.
		if err := writeWebSocketJSON(conn, evt.EventsSubscribedMessage{
			Type:     frameTypeSubscribed,
			Channels: declaredAccepted,
			Rejected: declaredRejected,
		}); err != nil {
			return
		}
		for _, channel := range declaredAccepted {
			if err := h.writeSnapshotFrame(conn, r, claims, boundProfileID, channel); err != nil {
				return
			}
		}
	}

	// The clock is disarmed by holding a subscription, not by having declared
	// one. A declaration that resolved to nothing — ?channels= with no names,
	// or a selection every entry of which was refused — leaves the connection
	// in exactly the state the grace period exists to reap: subscribed to
	// nothing, with no reason to expect it to ever receive a frame, while still
	// holding a hub subscriber, two goroutines, and an envelope channel that
	// every published event fans into. Such a client still owes a subscribe
	// frame, and its rejected list told it why. A nil channel blocks forever,
	// which is exactly the disarmed case.
	var deadlineC <-chan time.Time
	if len(subscriptions) == 0 {
		deadline := time.NewTimer(subscribeGracePeriod)
		defer deadline.Stop()
		deadlineC = deadline.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-readDone:
			return
		case <-accessChanged:
			closeEventsAccessChanged(conn, readDone)
			return
		case <-deadlineC:
			writeWebSocketError(conn, "bad_request", "subscribe is required within "+subscribeGracePeriod.String())
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
			// Obligation discharged: detach the timer so a later tick cannot
			// close a connection that has since subscribed. The timer itself
			// is stopped by the deferred Stop above.
			deadlineC = nil
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

// closeEventsAccessChanged sends the access_changed frame and close code, then
// waits briefly for the client's close reply so both frames arrive before the
// connection is torn down.
func closeEventsAccessChanged(conn *websocket.Conn, readDone <-chan struct{}) {
	if err := writeWebSocketJSON(conn, evt.EventsAccessChangedMessage{Type: eventsAccessChanged}); err != nil {
		return
	}
	if err := writeWebSocketControl(
		conn,
		websocket.CloseMessage,
		websocket.FormatCloseMessage(EventsCloseAccessChanged, eventsAccessChanged),
	); err != nil {
		return
	}
	reply := time.NewTimer(time.Second)
	defer reply.Stop()
	select {
	case <-readDone:
	case <-reply.C:
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

	nextSubs, accepted, rejected := resolveChannelSelection(message.Channels, allowed, boundProfileID)

	if err := writeWebSocketJSON(conn, evt.EventsSubscribedMessage{
		Type:      frameTypeSubscribed,
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

// resolveChannelSelection decides which of the requested channels a connection
// may subscribe to. It is the single place that answers that question, shared
// by the URL-declared path and the subscribe frame so the two cannot drift.
//
// Every rejection is reported rather than fatal: an unknown, forbidden, or
// profile-requiring channel costs the caller that channel and nothing else.
// Closing the connection instead would take down every other channel it holds
// over one bad name, and a client cannot always know which channels its role
// allows before it asks.
//
// Because refusals are answered instead of closing the connection, the answer
// has to be bounded independently of the request: at most maxRequestedChannels
// distinct names are considered, and an overrun is reported once rather than
// per excess name.
func resolveChannelSelection(
	requested []evt.EventChannel,
	allowed []evt.EventChannel,
	boundProfileID string,
) (map[evt.EventChannel]struct{}, []evt.EventChannel, []evt.EventsRejectedChannel) {
	allowedSet := make(map[evt.EventChannel]struct{}, len(allowed))
	for _, channel := range allowed {
		allowedSet[channel] = struct{}{}
	}
	validSet := make(map[evt.EventChannel]struct{}, len(evt.ClientChannels))
	for _, channel := range evt.ClientChannels {
		validSet[channel] = struct{}{}
	}

	capacity := min(len(requested), maxRequestedChannels)
	subs := make(map[evt.EventChannel]struct{}, capacity)
	accepted := make([]evt.EventChannel, 0, capacity)
	rejected := make([]evt.EventsRejectedChannel, 0, capacity)
	// A repeated channel is answered once whether it was accepted or refused.
	// Only the accepted side deduplicated before this change, so asking twice
	// for one forbidden channel produced two identical rejections.
	seen := make(map[evt.EventChannel]struct{}, capacity)

	reject := func(channel evt.EventChannel, code, message string) {
		// A garbage name is echoed back only far enough to be recognizable.
		if len(channel) > maxChannelNameLength {
			channel = channel[:maxChannelNameLength]
		}
		rejected = append(rejected, evt.EventsRejectedChannel{
			Channel: channel,
			Code:    code,
			Message: message,
		})
	}

	for _, channel := range requested {
		if _, dup := seen[channel]; dup {
			continue
		}
		if len(seen) >= maxRequestedChannels {
			// One entry for the whole overrun, not one per name: the point of
			// the cap is that the response cannot grow with the request.
			reject("", "too_many_channels",
				"At most "+strconv.Itoa(maxRequestedChannels)+" channels may be requested at once")
			break
		}
		seen[channel] = struct{}{}

		switch {
		case !contains(validSet, channel):
			reject(channel, "unknown_channel", "Unknown channel")
		case !contains(allowedSet, channel):
			// Deliberately not "admin access required": the caller's role is
			// the usual reason a channel is refused but not the only one, and a
			// remedy that does not exist reads as a bug in the client's own
			// permissions rather than a channel it was never going to get.
			reject(channel, "forbidden", "This channel is not available to this connection")
		// The notifications channel is profile-scoped: it requires a
		// connection bound to a profile via a websocket ticket.
		case channel == evt.ChannelNotifications && boundProfileID == "":
			reject(channel, "profile_required", "A profile-bound websocket ticket is required")
		default:
			subs[channel] = struct{}{}
			accepted = append(accepted, channel)
		}
	}

	return subs, accepted, rejected
}

func contains(set map[evt.EventChannel]struct{}, channel evt.EventChannel) bool {
	_, ok := set[channel]
	return ok
}

// parseDeclaredChannels reads the optional ?channels= selection a connection
// may declare on the URL, letting a read-only observer skip the subscribe
// handshake entirely. An absent parameter returns ok=false, which keeps the
// handshake (and its grace period) in force; an empty or all-blank value is a
// deliberate declaration of nothing, so it returns ok=true and no channels.
//
// Every occurrence of the parameter is read, not just the first. Repeating a
// query parameter is as natural a spelling as a comma-separated list, and
// honoring one and silently discarding the rest loses channels with no
// diagnostic — the connection would come up subscribed to less than it asked
// for and report nothing rejected.
func parseDeclaredChannels(query url.Values) ([]evt.EventChannel, bool) {
	values, ok := query["channels"]
	if !ok {
		return nil, false
	}

	channels := make([]evt.EventChannel, 0, 4)
	for _, value := range values {
		for _, raw := range strings.Split(value, ",") {
			name := strings.TrimSpace(raw)
			if name == "" {
				continue
			}
			channels = append(channels, evt.EventChannel(name))
		}
	}
	return channels, true
}

// allowedChannelsForClaims is allowedChannelsForRole, less the household
// channels a remote-playback handoff may not see: history imports cover every
// profile on the account.
func allowedChannelsForClaims(claims *auth.Claims) []evt.EventChannel {
	if claims.IsRemotePlayback() {
		return []evt.EventChannel{evt.ChannelCatalog, evt.ChannelUserState, evt.ChannelUserSettings, evt.ChannelNotifications}
	}
	return allowedChannelsForRole(claims.Role)
}

func allowedChannelsForRole(role string) []evt.EventChannel {
	channels := []evt.EventChannel{
		evt.ChannelCatalog,
		evt.ChannelHistoryImport,
		evt.ChannelUserState,
		evt.ChannelUserSettings,
		evt.ChannelNotifications,
	}
	if role == "admin" {
		channels = append(channels,
			evt.ChannelJobs,
			evt.ChannelSessions,
			evt.ChannelTasks,
			evt.ChannelScans,
			evt.ChannelSettings,
			evt.ChannelDownloadPreparations,
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
	// A remote-playback handoff sees its bound profile's events, not those
	// of the account's other profiles.
	if claims.IsRemotePlayback() && env.ProfileID != "" && env.ProfileID != claims.ProfileID {
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
	case evt.ChannelDownloadPreparations:
		// Events only say what changed; clients re-read the admin list, and a
		// null snapshot tells a reconnecting client to do so.
		return json.RawMessage("null"), nil
	case evt.ChannelNotifications:
		// Recent unread deliveries for the bound profile so reconnecting
		// clients hydrate without a separate REST call. Same row shape as the
		// inbox list API.
		if h == nil || h.notifications == nil || boundProfileID == "" {
			return json.RawMessage("[]"), nil
		}
		rows, err := h.notifications.Deliveries.RecentUnread(r.Context(), claims.UserID, boundProfileID, 25)
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
		runs := make([]evt.ScanRun, 0, maxRealtimeScanSnapshotRuns)
		remaining := maxRealtimeScanSnapshotRuns
		if h.persistedScans != nil && remaining > 0 {
			persisted, err := h.persistedScans.ListActiveSnapshot(r.Context(), remaining)
			if err != nil {
				return nil, err
			}
			if len(persisted) > remaining {
				persisted = persisted[:remaining]
			}
			runs = append(runs, persisted...)
			remaining -= len(persisted)
		}
		if h.scans != nil && remaining > 0 {
			runs = append(runs, h.scans.ListActiveLimit(remaining)...)
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
		for i := range runs {
			runs[i] = historyimport.PublicRun(runs[i])
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
		slog.ErrorContext(r.Context(),
			"events: failed to build initial snapshot", "component", "api",
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
			slog.ErrorContext(r.Context(), "events: failed to build event payload", "component", "api", "channel", env.Channel, "event", env.Event, "error", err)
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
