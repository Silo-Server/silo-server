package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/realtimeadmission"
)

const realtimeInputCloseReason = realtimeadmission.InputCloseReason
const realtimeFrameError = "error"
const realtimeBadRequest = "bad_request"
const roomMessageReady = "ready"
const roomMessageStateReport = "state_report"
const roomMessageBuffering = "buffering"
const roomMessageLobbyReady = "lobby_ready"
const roomActionApplied = "applied"
const realtimeInitialRetryGrace = 65 * time.Second

type realtimeErrorFrame struct {
	Type         string `json:"type"`
	Code         string `json:"code"`
	Message      string `json:"message"`
	RequestID    string `json:"request_id,omitempty"`
	RetryAfterMS int64  `json:"retry_after_ms,omitzero"`
	WorkStarted  *bool  `json:"work_started,omitempty"`
}

func realtimeRefusalFrame(err *realtimeadmission.Refusal, requestID string) realtimeErrorFrame {
	return realtimeErrorFrame{Type: realtimeFrameError, Code: err.Code, Message: err.Message, RequestID: requestID,
		RetryAfterMS: max(int64(1), int64((err.RetryAfter+time.Millisecond-1)/time.Millisecond)), WorkStarted: new(false)}
}

func realtimeInputError() realtimeErrorFrame {
	return realtimeErrorFrame{Type: realtimeFrameError, Code: realtimeadmission.InputLimitedCode, Message: "Realtime input allowance exceeded.", WorkStarted: new(false)}
}

func writeRealtimeHTTPRefusal(w http.ResponseWriter, err error) bool {
	refusal, ok := errors.AsType[*realtimeadmission.Refusal](err)
	if !ok {
		return false
	}
	status := http.StatusTooManyRequests
	if refusal.Code == realtimeadmission.UnavailableCode {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Retry-After", strconv.FormatInt(max(1, int64((refusal.RetryAfter+time.Second-1)/time.Second)), 10))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, realtimeRefusalFrame(refusal, ""))
	return true
}

func defaultRealtimeAdmission() *realtimeadmission.Controller {
	return realtimeadmission.New(ratelimit.NewMemoryWorkStore(), "memory")
}

// SetRealtimeAdmission is called before mounting routes. Both event handlers
// receive the same controller as the room handler.
func (h *EventsHandler) SetRealtimeAdmission(admission *realtimeadmission.Controller) {
	h.admission = admission
}

func (h *EventsHandler) workAdmission() *realtimeadmission.Controller {
	h.admissionOnce.Do(func() {
		if h.admission == nil {
			h.admission = defaultRealtimeAdmission()
		}
	})
	return h.admission
}

func (h *WatchTogetherHandler) workAdmission() *realtimeadmission.Controller {
	h.admissionOnce.Do(func() {
		if h.Admission == nil {
			h.Admission = defaultRealtimeAdmission()
		}
	})
	return h.Admission
}

func realtimeStatus(admission *realtimeadmission.Controller) *realtimeadmission.Status {
	if admission == nil {
		return nil
	}
	status := admission.Status()
	return &status
}

func (h *EventsSocketV2) RealtimeWorkStatus() *realtimeadmission.Status {
	return realtimeStatus(h.Events.workAdmission())
}

func (h *WatchTogetherSocketV2) RealtimeWorkStatus() *realtimeadmission.Status {
	return realtimeStatus(h.Room.workAdmission())
}

func (h *WatchTogetherHandler) RealtimeWorkStatus() *realtimeadmission.Status {
	return realtimeStatus(h.workAdmission())
}

func (h *EventsHandler) RealtimeWorkStatus() *realtimeadmission.Status {
	return realtimeStatus(h.workAdmission())
}

// A consumed credential carries at most one initial batch. Starting the initial
// attempt spends it; failed upgrades never refund it. A frame-only selection
// cannot keep unused credit beyond its original ticket deadline.
type eventsInitialWork struct {
	prepaid bool
	until   time.Time
}

type eventsInitialWorkKey struct{}

func withEventsInitialWork(ctx context.Context, prepaid bool, until time.Time) context.Context {
	return context.WithValue(ctx, eventsInitialWorkKey{}, &eventsInitialWork{prepaid: prepaid, until: until})
}

func (w *eventsInitialWork) consume(now time.Time) bool {
	if w == nil || !w.prepaid {
		return false
	}
	w.prepaid = false
	return now.Before(w.until)
}
