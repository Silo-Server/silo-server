package handlers

import (
	"context"
	"net/http"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/gorilla/websocket"
)

// eventSocket is the connection work the event handler owns. Keeping the
// transport boundary narrow also permits finite lifecycle tests without sockets.
type eventSocket interface {
	Close() error
	ReadMessage() (int, []byte, error)
	WriteJSON(any) error
	WriteControl(int, []byte, time.Time) error
	SetWriteDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetReadLimit(int64)
	SetPongHandler(func(string) error)
}

// SetClientSocketAdmission installs the process-wide client accounting boundary.
// Both native handlers and compatibility must receive the same instance.
func (h *EventsHandler) SetClientSocketAdmission(admission *evt.ClientSocketAdmission, appContext context.Context, shuttingDown func() bool) {
	h.socketAdmission = admission
	h.appContext = appContext
	h.shuttingDown = shuttingDown
}

func (h *EventsHandler) socketDraining() bool {
	return (h.shuttingDown != nil && h.shuttingDown()) || (h.appContext != nil && h.appContext.Err() != nil)
}

func refuseEventSocket(w http.ResponseWriter, refusal evt.ClientSocketRefusal) {
	w.Header().Set("Retry-After", "5")
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusServiceUnavailable
	if refusal == evt.ClientSocketAccountLimit {
		status = http.StatusTooManyRequests
	}
	http.Error(w, "realtime connection unavailable", status)
}

func (h *EventsHandler) admitClientSocket(w http.ResponseWriter, r *http.Request, accountID int) (*http.Request, func(), bool) {
	reservation, refusal := h.socketAdmission.Reserve(accountID, h.socketDraining())
	if refusal != "" {
		refuseEventSocket(w, refusal)
		return r, nil, false
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := func() bool { return true }
	if h.appContext != nil {
		stop = context.AfterFunc(h.appContext, cancel)
	}
	release := func() {
		cancel()
		stop()
		reservation.Release()
	}
	if ctx.Err() != nil || h.socketDraining() {
		release()
		refuseEventSocket(w, evt.ClientSocketDraining)
		return r, nil, false
	}
	return r.WithContext(ctx), release, true
}

func (h *EventsHandler) upgradeEventSocket(w http.ResponseWriter, r *http.Request, upgrader websocket.Upgrader) (eventSocket, error) {
	if h.upgradeSocket != nil {
		return h.upgradeSocket(w, r, upgrader)
	}
	return upgrader.Upgrade(w, r, nil)
}
