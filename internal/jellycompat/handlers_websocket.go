package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.opentelemetry.io/otel/trace"
)

const wsKeepAlive = "KeepAlive"

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// wsMessage is one socket frame. Jellyfin stamps every outbound message with
// a fresh MessageId, and jellyfin-sdk-kotlin rejects messages without one.
type wsMessage struct {
	MessageType string          `json:"MessageType"`
	Data        json.RawMessage `json:"Data,omitempty"`
	MessageID   string          `json:"MessageId,omitempty"`
}

// NewSocketHandler implements Jellyfin's application KeepAlive protocol without
// advertising remote-control commands. Tokens are revalidated while connected.
func NewSocketHandler(sessions *SessionStore, keys *AdminAPIKeyAuthenticator) http.HandlerFunc {
	return NewSocketHandlerWithUserData(sessions, keys, nil, nil)
}

// NewSocketHandlerWithUserData is NewSocketHandler that also forwards the
// session's watched-state changes as Jellyfin UserDataChanged messages, so
// clients such as Jellyfin Web refresh Continue Watching without a reload.
func NewSocketHandlerWithUserData(sessions *SessionStore, keys *AdminAPIKeyAuthenticator, events UserStateEvents, codec *ResourceIDCodec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := ExtractToken(r)
		resolve := func(ctx context.Context) *Session {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if strings.HasPrefix(token, "sa_") {
				session, _, _ := keys.resolveSession(ctx, token)
				return session
			}
			if sessions == nil {
				return nil
			}
			if sessions.repo != nil {
				session, err := sessions.repo.GetByToken(ctx, token, sessions.now())
				if err != nil {
					return nil
				}
				return session
			}
			session, ok := sessions.Get(token)
			if !ok {
				return nil
			}
			return session
		}
		validate := func(ctx context.Context) bool { return resolve(ctx) != nil }
		if !ok {
			writeError(w, 401, "Unauthorized", "Invalid or expired authentication token")
			return
		}
		session := resolve(r.Context())
		if session == nil {
			writeError(w, 401, "Unauthorized", "Invalid or expired authentication token")
			return
		}
		var notices <-chan wsMessage
		if events != nil && codec != nil {
			var stop func()
			notices, stop = userDataNotices(events, session, codec)
			defer stop()
		}
		serveCompatSocketWithNotices(w, r, validate, 12*time.Second, notices)
	}
}

// userDataNotices subscribes to the realtime hub and yields the session's
// UserDataChanged messages until stop is called. A slow socket drops notices
// rather than blocking the hub: any later one refreshes the same list.
func userDataNotices(events UserStateEvents, session *Session, codec *ResourceIDCodec) (<-chan wsMessage, func()) {
	envelopes, unsubscribe := events.Subscribe()
	notices := make(chan wsMessage, 8)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case env, ok := <-envelopes:
				if !ok {
					return
				}
				msg, ok := userDataChangedFor(env, session, codec)
				if !ok {
					continue
				}
				select {
				case notices <- msg:
				default:
				}
			}
		}
	}()
	var once sync.Once
	return notices, func() {
		once.Do(func() {
			close(done)
			unsubscribe()
		})
	}
}

// serveCompatSocket runs the KeepAlive protocol and revalidates the session
// every checkInterval. Those checks run outside the request's server span: a
// socket stays open for hours, and a trace that gained a session lookup every
// check would grow without bound. Each check starts its own trace instead,
// while the request's cancellation and values still apply.
func serveCompatSocket(w http.ResponseWriter, r *http.Request, validate func(context.Context) bool, checkInterval time.Duration) {
	serveCompatSocketWithNotices(w, r, validate, checkInterval, nil)
}

// serveCompatSocketWithNotices is serveCompatSocket that also writes each
// server-initiated message from notices to the client. A nil channel sends
// none.
func serveCompatSocketWithNotices(w http.ResponseWriter, r *http.Request, validate func(context.Context) bool, checkInterval time.Duration, notices <-chan wsMessage) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	checkCtx := trace.ContextWithSpanContext(r.Context(), trace.SpanContext{})
	conn.SetReadLimit(64 * 1024)
	done := make(chan struct{})
	defer close(done)
	messages := make(chan wsMessage, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			var msg wsMessage
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			select {
			case messages <- msg:
			case <-done:
				return
			}
		}
	}()
	write := func(message wsMessage) error {
		if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		message.MessageID = uuid.NewString()
		return conn.WriteJSON(message)
	}
	force := wsMessage{MessageType: "ForceKeepAlive", Data: json.RawMessage("60")}
	if err := write(force); err != nil {
		return
	}
	lastKeepAlive := time.Now()
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-readDone:
			return
		case notice := <-notices:
			if err := write(notice); err != nil {
				return
			}
		case msg := <-messages:
			if msg.MessageType == wsKeepAlive {
				lastKeepAlive = time.Now()
				if err := write(wsMessage{MessageType: wsKeepAlive}); err != nil {
					return
				}
			}
		case <-ticker.C:
			if !validate(checkCtx) {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "Authentication expired"), time.Now().Add(time.Second))
				return
			}
			elapsed := time.Since(lastKeepAlive)
			if elapsed >= 60*time.Second {
				return
			}
			if elapsed >= 45*time.Second {
				if err := write(force); err != nil {
					return
				}
			}
		}
	}
}
