package proxy

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// deliveryRecorder tells the API's idle sweep that this node is still
// delivering media for a playback session. The API cannot see the bytes this
// node serves, so without it a client that never reports progress (a Cast
// receiver whose sender phone went to sleep) is reaped mid-playback.
//
// meterEgress gives every stream request one recorder; a playback handler names
// the session with noteDelivery before it writes. Delivery is then recorded
// only as body bytes reach the client under a 2xx status, so a failed relay, an
// unsatisfiable range, or a client that stopped reading records nothing, and a
// long response keeps recording for as long as it flows.
type deliveryRecorder struct {
	record    func(sessionID string)
	sessionID string
	status    int
}

type deliveryRecorderKey struct{}

// noteDelivery marks the response to r as playback media for the claims'
// session. Responses that are not marked, such as subtitles, record nothing.
// HEAD is never marked: net/http accepts and discards body writes for it, so a
// handler that streams anyway would otherwise count bytes nobody received.
func noteDelivery(r *http.Request, claims *streamtoken.Claims) {
	if r.Method == http.MethodHead || claims == nil {
		return
	}
	if d, ok := r.Context().Value(deliveryRecorderKey{}).(*deliveryRecorder); ok {
		d.sessionID = claims.SessionID
	}
}

func (d *deliveryRecorder) wroteHeader(status int) {
	if d != nil && d.status == 0 && status >= http.StatusOK {
		d.status = status
	}
}

func (d *deliveryRecorder) wroteBody(n int64) {
	if d == nil || n <= 0 || d.sessionID == "" {
		return
	}
	// A body written without an explicit header is sent as 200.
	d.wroteHeader(http.StatusOK)
	if d.status >= http.StatusMultipleChoices {
		return
	}
	d.record(d.sessionID)
}
