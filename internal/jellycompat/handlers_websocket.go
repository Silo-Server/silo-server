package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.opentelemetry.io/otel/trace"
)

const wsKeepAlive = "KeepAlive"

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

type compatSocket interface {
	Close() error
	ReadJSON(any) error
	WriteJSON(any) error
	WriteControl(int, []byte, time.Time) error
	SetWriteDeadline(time.Time) error
	SetReadLimit(int64)
}

// SocketLifecycle is independent of optional user-data notifications. Every
// compatibility socket, including KeepAlive-only connections, spends capacity.
type SocketLifecycle struct {
	Admission    *evt.ClientSocketAdmission
	AppContext   context.Context
	ShuttingDown func() bool
	upgrade      func(http.ResponseWriter, *http.Request) (compatSocket, error)
}

func (l SocketLifecycle) draining() bool {
	return (l.ShuttingDown != nil && l.ShuttingDown()) || (l.AppContext != nil && l.AppContext.Err() != nil)
}

func refuseCompatSocket(w http.ResponseWriter, refusal evt.ClientSocketRefusal) {
	w.Header().Set("Retry-After", "5")
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusServiceUnavailable
	if refusal == evt.ClientSocketAccountLimit {
		status = http.StatusTooManyRequests
	}
	writeError(w, status, http.StatusText(status), "Realtime connection unavailable")
}

// wsMessage is one socket frame. Jellyfin stamps every outbound message with
// a fresh MessageId, and jellyfin-sdk-kotlin rejects messages without one.
type wsMessage struct {
	MessageType string          `json:"MessageType"`
	Data        json.RawMessage `json:"Data,omitempty"`
	MessageID   string          `json:"MessageId,omitempty"`
}

// NewSocketHandler implements Jellyfin's application KeepAlive protocol without
// advertising remote-control commands. Tokens are revalidated while connected.
func NewSocketHandler(sessions *SessionStore, keys *AdminAPIKeyAuthenticator, lifecycle SocketLifecycle) http.HandlerFunc {
	return NewSocketHandlerWithUserData(sessions, keys, nil, nil, lifecycle)
}

// NewSocketHandlerWithUserData is NewSocketHandler that also forwards the
// session's watched-state changes as Jellyfin UserDataChanged messages, so
// clients such as Jellyfin Web refresh Continue Watching without a reload.
func NewSocketHandlerWithUserData(sessions *SessionStore, keys *AdminAPIKeyAuthenticator, events UserStateEvents, codec *ResourceIDCodec, lifecycle SocketLifecycle) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := ExtractToken(r)
		// resolve returns ErrSessionNotFound for a token that is signed out;
		// any other error means the session could not be checked.
		resolve := func(ctx context.Context) (*Session, error) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if strings.HasPrefix(token, "sa_") {
				if session, _, _ := keys.resolveSession(ctx, token); session != nil {
					return session, nil
				}
				return nil, ErrSessionNotFound
			}
			if sessions == nil {
				return nil, ErrSessionNotFound
			}
			return sessions.Recheck(ctx, token)
		}
		// A check that could not run keeps the socket open; only a
		// signed-out session closes it.
		validate := func(ctx context.Context) bool {
			_, err := resolve(ctx)
			return !errors.Is(err, ErrSessionNotFound)
		}
		if !ok {
			writeError(w, 401, "Unauthorized", "Invalid or expired authentication token")
			return
		}
		session, err := resolve(r.Context())
		if errors.Is(err, ErrSessionNotFound) {
			writeError(w, 401, "Unauthorized", "Invalid or expired authentication token")
			return
		}
		if err != nil {
			writeSessionLookupFailed(w, r, token, err)
			return
		}
		reservation, refusal := lifecycle.Admission.Reserve(session.StreamAppUserID, lifecycle.draining())
		if refusal != "" {
			refuseCompatSocket(w, refusal)
			return
		}
		defer reservation.Release()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		if lifecycle.AppContext != nil {
			stop := context.AfterFunc(lifecycle.AppContext, cancel)
			defer stop()
		}
		if ctx.Err() != nil || lifecycle.draining() {
			refuseCompatSocket(w, evt.ClientSocketDraining)
			return
		}
		startNotices := func() (<-chan wsMessage, func()) {
			if events != nil && codec != nil {
				return userDataNotices(events, session, codec)
			}
			return nil, func() {}
		}
		serveCompatSocketWithSource(w, r.WithContext(ctx), validate, 12*time.Second, startNotices, lifecycle.upgrade)
	}
}

// userDataNotices subscribes to the realtime hub and yields the session's
// UserDataChanged messages until stop is called. A slow socket drops notices
// rather than blocking the hub: any later one refreshes the same list.
func userDataNotices(events UserStateEvents, session *Session, codec *ResourceIDCodec) (<-chan wsMessage, func()) {
	envelopes, unsubscribe := events.Subscribe()
	notices := make(chan wsMessage, 8)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
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
	return notices, sync.OnceFunc(func() {
		close(done)
		unsubscribe()
		<-stopped
	})
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
	serveCompatSocketWithSource(w, r, validate, checkInterval, func() (<-chan wsMessage, func()) { return notices, func() {} }, nil)
}

func serveCompatSocketWithSource(w http.ResponseWriter, r *http.Request, validate func(context.Context) bool, checkInterval time.Duration, startNotices func() (<-chan wsMessage, func()), upgrade func(http.ResponseWriter, *http.Request) (compatSocket, error)) {
	if r.Context().Err() != nil {
		return
	}
	if upgrade == nil {
		upgrade = func(w http.ResponseWriter, r *http.Request) (compatSocket, error) { return upgrader.Upgrade(w, r, nil) }
	}
	conn, err := upgrade(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	stopClose := context.AfterFunc(r.Context(), func() { _ = conn.Close() })
	defer stopClose()
	if r.Context().Err() != nil {
		return
	}
	notices, stopNotices := startNotices()
	defer stopNotices()
	checkCtx := trace.ContextWithSpanContext(r.Context(), trace.SpanContext{})
	conn.SetReadLimit(64 * 1024)
	done := make(chan struct{})
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
	defer func() {
		close(done)
		_ = conn.Close()
		<-readDone
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
