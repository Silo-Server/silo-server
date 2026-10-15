package transcodeproxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/logthrottle"
)

// NodeRefusedRelay reports whether a transcode node's status refuses the relay
// itself rather than answering the client: a 401. Every relay authenticates
// its client before it signs anything to a node, so a node's 401 is about the
// relay's own signature (a key rotated or not yet loaded on one side) or the
// stream token the relay forwarded, never about the client's login. A relay
// answers it as the node being unavailable, in its own error shape: passed on,
// a player can read it as its own sign-in failing and sign out.
//
// A node answers no 403 of its own (it maps the core's resolve refusals to
// 400, 401, 404, 410 and 503), so a 403 is left to the relay's ordinary
// handling.
func NodeRefusedRelay(status int) bool {
	return status == http.StatusUnauthorized
}

// refusalLogInterval is how often one node's relay refusals are logged.
const refusalLogInterval = time.Minute

var refusalLog = logthrottle.New[string](refusalLogInterval)

// WarnNodeRefusedRelay logs that the node at nodeURL refused a relay with
// status, at most once a minute per node; each line counts the refusals
// suppressed since the previous one.
func WarnNodeRefusedRelay(ctx context.Context, component, nodeURL string, status int) {
	node := logredact.SanitizeURL(nodeURL)
	suppressed, ok := refusalLog.Allow(node)
	if !ok {
		return
	}
	slog.WarnContext(ctx, "transcode node refused the relay's request; answering the client as node unavailable",
		"component", component, "node", node, "status", status, "suppressed", suppressed)
}
