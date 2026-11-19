package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// Close codes the v2 sockets (events, admin logs, playback control, watch
// together) end a connection with when the server ends it because of the
// credential it was opened with. Each names what the client does next;
// docs/realtime-api.md documents them. The reason is the matching snake_case
// string below.
const (
	// SocketCloseAccessChanged: the credential still holds but the access the
	// ticket was minted under changed. Refetch access-dependent state, mint a
	// new ticket and reconnect at once.
	SocketCloseAccessChanged = 4001
	// SocketCloseCredentialInvalid: the API key, account, login session or
	// profile the socket was opened under is gone or disabled. Do not
	// reconnect with the same credential.
	SocketCloseCredentialInvalid = 4002
	// SocketCloseLifetimeEnded: the connection reached its maximum lifetime or
	// the expiry of the access it was opened under. Mint a new ticket and
	// reconnect.
	SocketCloseLifetimeEnded = 4003
	// SocketCloseAuthorityUnavailable: the server could not recheck the
	// credential. Reconnect with backoff; the credential may still hold.
	SocketCloseAuthorityUnavailable = 4004
)

const (
	eventsAccessChanged         = "access_changed"
	socketReasonGone            = "credential_invalid"
	socketReasonLifetimeEnded   = "lifetime_ended"
	socketReasonUnavailable     = "authority_unavailable"
	socketCloseReplyGrace       = time.Second
	socketAuthorityCheckTimeout = 2 * time.Second
)

var (
	// errSocketAccessChanged is the validator's answer when the login session
	// or API key still holds but the access a ticket was minted under no
	// longer does: the account role, the effective role, the scope
	// fingerprint, profile verification, or the key's revision changed. It
	// wraps evt.ErrSocketTicket, so callers that only tell a usable credential
	// from an unusable one treat it as unusable.
	errSocketAccessChanged = fmt.Errorf("%w: access changed", evt.ErrSocketTicket)
	// errSocketCredentialInvalid: the key, account, login session or profile
	// is definitely gone or disabled.
	errSocketCredentialInvalid = fmt.Errorf("%w: credential no longer valid", evt.ErrSocketTicket)
	// errSocketLifetimeEnded: the access the identity was minted under has
	// expired, or the connection reached its maximum lifetime.
	errSocketLifetimeEnded = fmt.Errorf("%w: lifetime ended", evt.ErrSocketTicket)
)

// socketAuthorityUnavailable wraps a failed lookup so it is told apart from a
// credential that is gone.
func socketAuthorityUnavailable(err error) error {
	return fmt.Errorf("%w: %w", evt.ErrSocketAuthorityUnavailable, err)
}

// socketCloseFrame names the close code and reason for why a socket ended, or
// reports false when the cause is not one the server announces (the
// connection loop returned, the request ended).
func socketCloseFrame(cause error) (int, string, bool) {
	switch {
	case errors.Is(cause, errSocketAccessChanged):
		return SocketCloseAccessChanged, eventsAccessChanged, true
	case errors.Is(cause, errSocketLifetimeEnded):
		return SocketCloseLifetimeEnded, socketReasonLifetimeEnded, true
	case errors.Is(cause, evt.ErrSocketAuthorityUnavailable):
		return SocketCloseAuthorityUnavailable, socketReasonUnavailable, true
	case errors.Is(cause, evt.ErrSocketTicket):
		return SocketCloseCredentialInvalid, socketReasonGone, true
	}
	return 0, "", false
}

// socketLifetime bounds a v2 socket's context: it ends with
// errSocketLifetimeEnded at the deadline, and cancel ends it early with the
// cause it is given.
func socketLifetime(parent context.Context, deadline time.Time) (context.Context, context.CancelCauseFunc) {
	lifetime, stop := context.WithDeadlineCause(parent, deadline, errSocketLifetimeEnded)
	ctx, cancel := context.WithCancelCause(lifetime)
	return ctx, func(cause error) {
		cancel(cause)
		stop()
	}
}

// socketCloser ends one v2 socket connection exactly once. When the cause
// names a close code, it first sends that close frame and waits up to
// socketCloseReplyGrace for the peer's close reply, so the client learns why
// before the TCP connection goes; a peer that does not answer is dropped when
// the grace ends.
type socketCloser struct {
	conn    *websocket.Conn
	once    sync.Once
	replied chan struct{}
}

// newSocketCloser installs a close handler that notes the peer's close frame
// and answers it as gorilla's default handler does.
func newSocketCloser(conn *websocket.Conn) *socketCloser {
	c := &socketCloser{conn: conn, replied: make(chan struct{})}
	var replied sync.Once
	conn.SetCloseHandler(func(code int, _ string) error {
		replied.Do(func() { close(c.replied) })
		_ = writeWebSocketControl(conn, websocket.CloseMessage, websocket.FormatCloseMessage(code, ""))
		return nil
	})
	return c
}

func (c *socketCloser) close(cause error) {
	c.once.Do(func() {
		if code, reason, ok := socketCloseFrame(cause); ok {
			if writeWebSocketControl(c.conn, websocket.CloseMessage, websocket.FormatCloseMessage(code, reason)) == nil {
				grace := time.NewTimer(socketCloseReplyGrace)
				select {
				case <-c.replied:
				case <-grace.C:
				}
				grace.Stop()
			}
		}
		_ = c.conn.Close()
	})
}

// closeWhenDone closes the connection with ctx's cause once ctx ends. The
// returned func detaches it; call c.close on the way out as well, so a
// connection whose loop returned first still ends once.
func (c *socketCloser) closeWhenDone(ctx context.Context) (stop func() bool) {
	return context.AfterFunc(ctx, func() { c.close(context.Cause(ctx)) })
}

// recheckSocketAuthority calls check every interval (eventsSessionCheckInterval
// when it is not positive) until ctx ends and cancels ctx with the first
// error, which names the close code.
func recheckSocketAuthority(ctx context.Context, cancel context.CancelCauseFunc, interval time.Duration, check func(context.Context) error) {
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
			checkCtx, stop := context.WithTimeout(ctx, socketAuthorityCheckTimeout)
			err := check(checkCtx)
			stop()
			if err != nil {
				cancel(err)
				return
			}
		}
	}
}

// refuseSocketHandshake answers an upgrade whose credential did not validate:
// 503 when it could not be checked, so the client retries rather than treat
// the credential as gone, and 401 otherwise.
func refuseSocketHandshake(w http.ResponseWriter, err error, expired string) {
	if errors.Is(err, evt.ErrSocketAuthorityUnavailable) {
		http.Error(w, "realtime authority could not be checked", http.StatusServiceUnavailable)
		return
	}
	http.Error(w, expired, http.StatusUnauthorized)
}
