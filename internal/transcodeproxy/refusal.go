package transcodeproxy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
)

// NodeRefusedRelay reports whether a transcode node's status refuses the relay
// itself rather than answering the client: a 401. Every relay authenticates
// its client (a login, a stream token or a grant) before it signs anything to
// a node, so a node's 401 is about the relay's own signature (a key rotated
// or not yet loaded on one side) or the stream token the relay forwarded,
// never about the client's credentials. A relay answers it as the node being
// unavailable, in its own error shape: passed on, a player can read it as its
// own sign-in failing and sign out.
func NodeRefusedRelay(status int) bool {
	return status == http.StatusUnauthorized
}

// NodeUnsignable reports whether err is a relay that could not sign its
// request to a transcode node because that node has no key or is not
// registered at the URL. Either is the node being unavailable, not a fault of
// this server or of the client, and a relay that moves a transcode off a dead
// node treats it like one.
func NodeUnsignable(err error) bool {
	return errors.Is(err, nodeauth.ErrNoNodeKey) || errors.Is(err, nodeauth.ErrUnknownNode)
}

// unavailableLogInterval is how often one node's refusals of each kind are
// logged.
const unavailableLogInterval = time.Minute

type unavailableKey struct {
	node, kind string
}

var unavailableLog = struct {
	mu   sync.Mutex
	last map[unavailableKey]time.Time
}{last: map[unavailableKey]time.Time{}}

// allowUnavailableLog reports whether a warning of kind about node may be
// logged now, at most once per unavailableLogInterval.
func allowUnavailableLog(node, kind string, now time.Time) bool {
	unavailableLog.mu.Lock()
	defer unavailableLog.mu.Unlock()
	key := unavailableKey{node: node, kind: kind}
	if last, ok := unavailableLog.last[key]; ok && now.Sub(last) < unavailableLogInterval {
		return false
	}
	if len(unavailableLog.last) > 1024 {
		for k, last := range unavailableLog.last {
			if now.Sub(last) >= unavailableLogInterval {
				delete(unavailableLog.last, k)
			}
		}
	}
	unavailableLog.last[key] = now
	return true
}

// WarnNodeRefusedRelay logs that the node at nodeURL refused a relay with
// status, at most once a minute per node, and asks keys, the keyring the
// relay signed with, to reload: the node may hold a key issued after this
// server loaded its keys, and a reload heals that within seconds. It never
// moves the transcode.
func WarnNodeRefusedRelay(ctx context.Context, component, nodeURL string, status int, keys nodeauth.Keyring) {
	nodeauth.ReloadAfterRefusal(keys)
	node := logredact.SanitizeURL(nodeURL)
	if !allowUnavailableLog(node, "refused", time.Now()) {
		return
	}
	slog.WarnContext(ctx, "transcode node refused the relay's request; answering the client as node unavailable",
		"component", component, "node", node, "status", status)
}

// WarnNodeUnsignable logs that a relay could not sign its request to the node
// at nodeURL, at most once a minute per node.
func WarnNodeUnsignable(ctx context.Context, component, nodeURL string, err error) {
	node := logredact.SanitizeURL(nodeURL)
	if !allowUnavailableLog(node, "unsignable", time.Now()) {
		return
	}
	slog.WarnContext(ctx, "cannot sign the relay's request to a transcode node; answering the client as node unavailable",
		"component", component, "node", node, "error", err)
}
