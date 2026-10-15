// Package nodecontrol owns the transcode node control plane: the
// /api/internal/nodes/* endpoints a transcode node calls with its own key and
// the authentication in front of them on the core, and the node's client for
// them (configuration, recipe resolution, reports, logs and health changes).
package nodecontrol

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/logthrottle"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// KeySource returns the key of a transcode node by id, whether or not the
// node is enabled, and an error for a deleted, keyless, or non-transcode
// node. Its ReloadNodeKey bounds how often a bad signature from a known node
// can reload the keys. *nodepool.KeyRegistry implements it.
type KeySource = nodeauth.NodeKeys

type nodeIDContextKey struct{}

// RequireNode authenticates a request signed by a transcode node with its own
// key (nodeauth.VerifyFromNode) and puts the verified node id in the request
// context, where NodeID reads it. A disabled node is accepted, because it
// keeps serving the streams it already has; a deleted node, a node without a
// key, a proxy, and anything unsigned or badly signed get a plain-text 401.
// A signature that fails against a known node's loaded key is checked once
// more after a reload, so a node just issued a new key is not refused until
// the next periodic refresh. A request refused because the node it names has
// no key is logged with the node and the reason, at most once a minute per
// node. Handlers must take the node's identity from NodeID, never from a body.
func RequireNode(keys KeySource) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nodeID, err := nodeauth.VerifyFromNode(r, keys)
			if err != nil {
				warnNoNodeKey(r, err)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nodeIDContextKey{}, nodeID)))
		})
	}
}

// noKeyLogInterval spaces the warnings about one node's keyless requests: a
// deleted or keyless node that is still running calls every second.
const noKeyLogInterval = time.Minute

// noKeyLog throttles those warnings per node id. The id comes from an
// unauthenticated header; the throttle bounds how many it tracks.
var noKeyLog = logthrottle.New[int](noKeyLogInterval)

// warnNoNodeKey logs a request refused because the node it names has no key
// on this server, which otherwise answers only a bare 401.
func warnNoNodeKey(r *http.Request, err error) {
	var keyErr *nodeauth.NodeKeyError
	if !errors.As(err, &keyErr) {
		return
	}
	suppressed, ok := noKeyLog.Allow(keyErr.NodeID)
	if !ok {
		return
	}
	slog.WarnContext(r.Context(), "refused a transcode node's request: this server has no key for the node it names",
		"component", "nodecontrol", "node_id", keyErr.NodeID, "reason", noKeyReason(keyErr.Err),
		"remote_addr", r.RemoteAddr, "suppressed", suppressed)
}

// noKeyReason names why a node has no key, from the key source's error.
func noKeyReason(err error) string {
	switch {
	case errors.Is(err, nodepool.ErrNodeNotFound):
		return "unknown or deleted node"
	case errors.Is(err, nodepool.ErrNodeKeyNotIssuable):
		return "not a transcode node"
	case errors.Is(err, nodeauth.ErrNoNodeKey):
		return "no key issued, or its stored key cannot be decrypted"
	default:
		return "key lookup failed: " + err.Error()
	}
}

// NodeID returns the id of the transcode node RequireNode authenticated for
// this request, and false outside a RequireNode chain.
func NodeID(ctx context.Context) (int, bool) {
	id, ok := ctx.Value(nodeIDContextKey{}).(int)
	return id, ok && id > 0
}
