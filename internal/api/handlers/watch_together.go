package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/watchtogether"

	"github.com/Silo-Server/silo-server/internal/wsconn"
)

type WatchTogetherScopeResolver interface {
	Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error)
}

type WatchTogetherHandler struct {
	Service       *watchtogether.Service
	ScopeResolver WatchTogetherScopeResolver
	TokenService  *watchtogether.RoomTokenService
	// MemberState and Details serve the v2 member-state and picker reads.
	// Both may be nil, in which case those operations fail closed.
	MemberState        *watchtogether.MemberStateReader
	Details            watchtogether.ItemDetailLookup
	MemberStateCatalog interface {
		GetSearchItemsByIDsWithAccess(context.Context, []string, catalog.AccessFilter) ([]*models.MediaItem, error)
	}
	// StreamLoginSession answers the login session that started a playback
	// session (its attempt row). A connection of a session another app's key
	// opened attaches only a stream its own login session started; nil
	// refuses every such attach.
	StreamLoginSession func(ctx context.Context, playbackSessionID string) (string, error)
}

type watchTogetherRoomResponse struct {
	Room            watchtogether.Snapshot `json:"room"`
	RoomAccessToken string                 `json:"room_access_token,omitempty"`
}

type watchTogetherClientMessage struct {
	Type string `json:"type"`
}

type watchTogetherAttachMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

type watchTogetherTransportRequestMessage struct {
	Type            string                        `json:"type"`
	Action          watchtogether.TransportAction `json:"action"`
	PositionSeconds *float64                      `json:"position_seconds,omitempty"`
	IsPaused        bool                          `json:"is_paused"`
}

type watchTogetherStateReportMessage struct {
	Type            string  `json:"type"`
	SessionID       string  `json:"session_id"`
	PositionSeconds float64 `json:"position_seconds"`
	IsPaused        bool    `json:"is_paused"`
	// Optional: the report doubles as a ready acknowledgement of command_id
	// while the room is waiting, so a lost ready frame heals on the next tick.
	CommandID string `json:"command_id,omitempty"`
	IsReady   bool   `json:"is_ready,omitempty"`
}

type watchTogetherReadyMessage struct {
	Type            string  `json:"type"`
	CommandID       string  `json:"command_id,omitempty"`
	SessionID       string  `json:"session_id"`
	PositionSeconds float64 `json:"position_seconds"`
	IsPaused        bool    `json:"is_paused"`
}

type watchTogetherBufferingMessage struct {
	Type            string  `json:"type"`
	SessionID       string  `json:"session_id"`
	PositionSeconds float64 `json:"position_seconds"`
	IsPaused        bool    `json:"is_paused"`
}

type watchTogetherLobbyReadyMessage struct {
	Type  string `json:"type"`
	Ready bool   `json:"ready"`
}

type watchTogetherPingMessage struct {
	Type         string `json:"type"`
	ClientSentAt string `json:"client_sent_at"`
}

// Each viewer has one writer and a bounded queue. A slow socket is closed
// without delaying commands to the rest of the room.
const watchTogetherQueueSize = 64
const watchTogetherReplacementTimeout = time.Second
const watchTogetherNotFound = "not_found"
const watchTogetherReasonKey = "reason"
const watchTogetherTypeKey = "type"
const watchTogetherRoomClosed = "room_closed"

type watchTogetherSocket interface {
	SetWriteDeadline(time.Time) error
	WriteMessage(int, []byte) error
	WriteControl(int, []byte, time.Time) error
	Close() error
}
type watchTogetherFrame struct {
	data []byte
	ping bool
}
type watchTogetherRoomConn struct {
	conn           watchTogetherSocket
	outgoing       chan watchTogetherFrame
	done           chan struct{}
	replaced       chan struct{}
	stopOnce       sync.Once
	closeOnce      sync.Once
	replaceOnce    sync.Once
	pingSentAtNano atomic.Int64
}

func newWatchTogetherRoomConn(conn watchTogetherSocket) *watchTogetherRoomConn {
	c := &watchTogetherRoomConn{conn: conn, outgoing: make(chan watchTogetherFrame, watchTogetherQueueSize), done: make(chan struct{}), replaced: make(chan struct{})}
	go c.writeLoop()
	return c
}
func (c *watchTogetherRoomConn) WriteJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.enqueue(watchTogetherFrame{data: data})
}
func (c *watchTogetherRoomConn) enqueue(frame watchTogetherFrame) error {
	select {
	case <-c.replaced:
		return websocket.ErrCloseSent
	case <-c.done:
		return websocket.ErrCloseSent
	default:
	}
	select {
	case <-c.done:
		return websocket.ErrCloseSent
	case c.outgoing <- frame:
		return nil
	default:
		_ = c.Close()
		return websocket.ErrCloseSent
	}
}
func (c *watchTogetherRoomConn) Close() error {
	c.stop()
	var err error
	c.closeOnce.Do(func() { err = c.conn.Close() })
	return err
}

// stop ends the writer and refuses further frames, leaving the transport open.
func (c *watchTogetherRoomConn) stop() { c.stopOnce.Do(func() { close(c.done) }) }

// CloseReplaced uses the socket's single writer and waits for the terminal
// frame to be written before closing. A blocked peer can delay takeover by at
// most one second.
func (c *watchTogetherRoomConn) CloseReplaced() error {
	c.replaceOnce.Do(func() { close(c.replaced) })
	timer := time.NewTimer(watchTogetherReplacementTimeout)
	defer timer.Stop()
	select {
	case <-c.done:
		return c.Close()
	case <-timer.C:
		return c.Close()
	}
}

func (c *watchTogetherRoomConn) WritePing() error { return c.enqueue(watchTogetherFrame{ping: true}) }
func (c *watchTogetherRoomConn) writeLoop() {
	var err error
	defer func() {
		// A close frame already went out, from the v2 closer or as the reply
		// to the peer's: whoever sent it owns the transport while it waits
		// for the other side, so the writer only stops.
		if errors.Is(err, websocket.ErrCloseSent) {
			c.stop()
			return
		}
		_ = c.Close()
	}()
	for {
		var frame watchTogetherFrame
		select {
		case <-c.done:
			return
		case <-c.replaced:
		case frame = <-c.outgoing:
		}
		// Give replacement priority over queued snapshots and pings.
		terminal := false
		writeTimeout := wsWriteTimeout
		select {
		case <-c.replaced:
			terminal = true
			writeTimeout = watchTogetherReplacementTimeout
			frame = watchTogetherFrame{data: []byte(`{"type":"connection_replaced","reason":"This profile joined the Watch Party on another device."}`)}
		default:
		}
		if frame.ping {
			c.pingSentAtNano.Store(time.Now().UnixNano())
			err = c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout))
		} else {
			err = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err == nil {
				err = c.conn.WriteMessage(websocket.TextMessage, frame.data)
			}
		}
		if err != nil || terminal {
			return
		}
	}
}

// TakePingSentAt returns and clears the send time of the last unanswered
// protocol-level ping.
func (c *watchTogetherRoomConn) TakePingSentAt() time.Time {
	nano := c.pingSentAtNano.Swap(0)
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

func (c *watchTogetherRoomConn) WriteError(code, message string) {
	_ = c.WriteJSON(map[string]string{
		"type":    "error",
		"code":    code,
		"message": message,
	})
}

func NewWatchTogetherHandler(
	service *watchtogether.Service,
	scopeResolver WatchTogetherScopeResolver,
	tokenService *watchtogether.RoomTokenService,
) *WatchTogetherHandler {
	return &WatchTogetherHandler{
		Service:       service,
		ScopeResolver: scopeResolver,
		TokenService:  tokenService,
	}
}

func (h *WatchTogetherHandler) buildRoomResponse(
	ctx context.Context,
	snapshot watchtogether.Snapshot,
	userID int,
	profileID string,
) (watchTogetherRoomResponse, error) {
	response := watchTogetherRoomResponse{Room: snapshot}
	if h == nil || h.TokenService == nil {
		return response, nil
	}

	token, _, err := h.TokenService.Mint(watchtogether.RoomTokenClaims{
		RoomID:    snapshot.RoomID,
		UserID:    userID,
		ProfileID: profileID,
	})
	if err != nil {
		return watchTogetherRoomResponse{}, err
	}
	response.RoomAccessToken = token
	return response, nil
}

// serveRoomConnection preserves the existing room message and disconnect loop.
// closeTransport ends the connection on the way out, before the room's own
// writer is torn down, so a close frame the caller owes the peer is sent
// while the transport is still open.
func (h *WatchTogetherHandler) serveRoomConnection(parent context.Context, conn *websocket.Conn, roomID string, userID int, profileID string, otherApp bool, loginSessionID string, closeTransport func()) {
	realtimeConn := newWatchTogetherRoomConn(conn)
	defer func() {
		closeTransport()
		_ = realtimeConn.Close()
	}()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	connect := h.Service.Connect
	if otherApp {
		connect = h.Service.ConnectOtherApp
	}
	reg, _, err := connect(ctx, roomID, userID, profileID, realtimeConn)
	if err != nil {
		// Terminal failures use room_closed so clients stop reconnecting
		// instead of retrying a room that will never come back.
		if errors.Is(err, watchtogether.ErrRoomNotFound) {
			_ = writeWebSocketJSON(conn, map[string]string{watchTogetherTypeKey: watchTogetherRoomClosed, watchTogetherReasonKey: watchTogetherNotFound})
		} else if errors.Is(err, watchtogether.ErrMemberAlreadyConnected) {
			writeWebSocketError(conn, "forbidden", otherAppSeatHeldMessage)
		} else if errors.Is(err, watchtogether.ErrRoomClosed) {
			_ = writeWebSocketJSON(conn, map[string]string{watchTogetherTypeKey: watchTogetherRoomClosed, watchTogetherReasonKey: "ended"})
		} else {
			writeWebSocketError(conn, "internal_error", "Failed to connect room socket")
		}
		return
	}
	defer h.Service.Disconnect(reg, false)

	configureWebSocket(conn)
	// Measure round-trip latency from protocol-level ping/pong on the server
	// clock; client-reported timestamps are subject to clock skew and cannot
	// be trusted for command scheduling.
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
		if sentAt := realtimeConn.TakePingSentAt(); !sentAt.IsZero() {
			_ = h.Service.HandlePingForConnection(ctx, reg, userID, profileID, time.Since(sentAt).Milliseconds())
		}
		return nil
	})
	startWebSocketPingLoop(ctx, realtimeConn.WritePing)
	// Prime an RTT sample right away instead of waiting for the first tick.
	_ = realtimeConn.WritePing()

	for {
		_, data, err := wsconn.ReadMessage(conn, wsMaxClientMessageBytes)
		if err != nil {
			return
		}
		if ctx.Err() != nil {
			// The connection is ending: keep reading for the peer's close
			// reply, but act on nothing more it sends.
			continue
		}

		if err := h.handleRoomClientMessage(ctx, realtimeConn, reg, userID, profileID, roomCaller{otherApp: otherApp, loginSessionID: loginSessionID}, data); err != nil {
			realtimeConn.WriteError("bad_request", err.Error())
		}
	}
}

// otherAppSeatHeldMessage is the error frame's message when a session
// another app opened would take a member's seat that is still held. The room
// does not record which login session holds a seat, and a seat stays held for
// a while after a socket that dropped without closing (or whose API server
// died), so the holder may be the app's own earlier connection: the message
// says the seat is held and invites a retry, never which device holds it.
const otherAppSeatHeldMessage = "This profile's place in the room is still held by another connection; try again shortly"

// errOtherAppTransport refuses a transport request from a session another
// app opened, as the error frame any refused request gets.
var errOtherAppTransport = errors.New("watch together transport is not allowed for a sign-in another app opened")

// errOtherAppAttach refuses an attach from a session another app opened of a
// playback session its own login session did not start.
var errOtherAppAttach = errors.New("watch together attach is not allowed for a stream this sign-in did not start")

// roomCaller is what the room socket knows of the connection's sign-in.
type roomCaller struct {
	// otherApp marks a session another app's key opened.
	otherApp bool
	// loginSessionID is the connection's login session.
	loginSessionID string
}

func (h *WatchTogetherHandler) handleRoomClientMessage(
	ctx context.Context,
	rc *watchTogetherRoomConn,
	reg *watchtogether.Registration,
	userID int,
	profileID string,
	caller roomCaller,
	data []byte,
) error {
	otherApp := caller.otherApp
	var base watchTogetherClientMessage
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}

	switch base.Type {
	case "attach_session":
		var msg watchTogetherAttachMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.SessionID == "" {
			return errors.New("session_id is required")
		}
		if otherApp && !h.ownStream(ctx, caller.loginSessionID, msg.SessionID) {
			return errOtherAppAttach
		}
		_, err := h.Service.AttachSessionForConnection(ctx, reg, userID, profileID, msg.SessionID)
		return err
	case "transport_request":
		// A session another app opened watches along; it does not play,
		// pause or seek for the room, whatever the room's guest policy.
		if otherApp {
			return errOtherAppTransport
		}
		var msg watchTogetherTransportRequestMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		_, err := h.Service.HandleTransportRequestForConnection(ctx, reg, userID, profileID, watchtogether.TransportRequest{
			Action:          msg.Action,
			PositionSeconds: msg.PositionSeconds,
			IsPaused:        msg.IsPaused,
		})
		return err
	case "state_report":
		var msg watchTogetherStateReportMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.SessionID == "" {
			return errors.New("session_id is required")
		}
		_, err := h.Service.HandleStateReportForConnection(ctx, reg, userID, profileID, watchtogether.StateReport{
			CommandID:       msg.CommandID,
			SessionID:       msg.SessionID,
			PositionSeconds: msg.PositionSeconds,
			IsPaused:        msg.IsPaused,
			IsReady:         msg.IsReady,
		})
		return err
	case "ready":
		var msg watchTogetherReadyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.SessionID == "" {
			return errors.New("session_id is required")
		}
		_, err := h.Service.HandleReadyForConnection(ctx, reg, userID, profileID, watchtogether.StateReport{
			CommandID:       msg.CommandID,
			SessionID:       msg.SessionID,
			PositionSeconds: msg.PositionSeconds,
			IsPaused:        msg.IsPaused,
		})
		return err
	case "buffering":
		var msg watchTogetherBufferingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.SessionID == "" {
			return errors.New("session_id is required")
		}
		_, err := h.Service.HandleBufferingForConnection(ctx, reg, userID, profileID, watchtogether.StateReport{
			SessionID:       msg.SessionID,
			PositionSeconds: msg.PositionSeconds,
			IsPaused:        msg.IsPaused,
		})
		return err
	case "lobby_ready":
		var msg watchTogetherLobbyReadyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		_, err := h.Service.HandleLobbyReadyForConnection(ctx, reg, userID, profileID, msg.Ready)
		return err
	case "ping":
		var msg watchTogetherPingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		// The echoed timestamps only serve the client's clock-offset estimate;
		// latency for command scheduling is measured server-side from
		// protocol-level ping/pong.
		now := time.Now().UTC()
		return rc.WriteJSON(map[string]string{
			"type":               "pong",
			"client_sent_at":     msg.ClientSentAt,
			"server_received_at": now.Format(time.RFC3339Nano),
			"server_sent_at":     time.Now().UTC().Format(time.RFC3339Nano),
		})
	default:
		return errors.New("unsupported room websocket message")
	}
}

// ownStream reports whether the caller's login session started the playback
// session, by its attempt row; a missing row or a failed read is no.
func (h *WatchTogetherHandler) ownStream(ctx context.Context, loginSessionID, playbackSessionID string) bool {
	if h.StreamLoginSession == nil || loginSessionID == "" {
		return false
	}
	started, err := h.StreamLoginSession(ctx, playbackSessionID)
	return err == nil && started == loginSessionID
}
