package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/realtimeadmission"
)

type eventsClientInput struct {
	data      []byte
	violation bool
}

func readEventsInputs(ctx context.Context, conn *websocket.Conn, shield *realtimeadmission.InputShield) (<-chan eventsClientInput, <-chan struct{}) {
	inputs := make(chan eventsClientInput, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// No JSON decode, shared-store decision or downstream work occurs
			// for a frame refused by either local input bucket.
			input := eventsClientInput{data: data}
			if !shield.Allow() {
				input = eventsClientInput{violation: true}
			}
			select {
			case inputs <- input:
			case <-ctx.Done():
				return
			}
			if input.violation {
				// Leave the main writer to send the notice and policy close.
				// Keeping done open prevents its select racing that notice.
				<-ctx.Done()
				return
			}
		}
	}()
	return inputs, done
}

type eventsSelectionResult struct {
	selection map[evt.EventChannel]struct{}
	refusal   *realtimeadmission.Refusal
	applied   bool
	close     bool
	invalid   string
}

type eventsSelectionOutput struct {
	write    func(any) error
	snapshot func(evt.EventChannel) error
}

func (h *EventsHandler) admitEventsSelection(ctx context.Context, userID int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	initial, _ := ctx.Value(eventsInitialWorkKey{}).(*eventsInitialWork)
	if initial.consume(time.Now()) {
		return ctx.Err()
	}
	return h.workAdmission().Admit(ctx, realtimeadmission.Events, realtimeadmission.Identity{UserID: userID})
}

func (h *EventsHandler) handleEventsClientMessage(conn *websocket.Conn, r *http.Request, claims *auth.Claims, boundProfileID string, data []byte, allowed []evt.EventChannel) eventsSelectionResult {
	result := h.applyEventsSelection(r, claims, boundProfileID, data, allowed, eventsSelectionOutput{
		write: func(frame any) error { return writeWebSocketJSON(conn, frame) },
		snapshot: func(channel evt.EventChannel) error {
			return h.writeSnapshotFrame(conn, r, claims, boundProfileID, channel)
		},
	})
	if result.invalid != "" {
		_ = writeWebSocketControl(conn, websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, result.invalid))
	}
	return result
}

// applyEventsSelection publishes only after admission, and preserves the
// acknowledgement-before-snapshot ordering of a complete subscription batch.
func (h *EventsHandler) applyEventsSelection(r *http.Request, claims *auth.Claims, boundProfileID string, data []byte, allowed []evt.EventChannel, output eventsSelectionOutput) eventsSelectionResult {
	var base struct {
		Type string `json:"type"`
	}
	if err := readWebSocketJSON(data, &base); err != nil {
		_ = output.write(realtimeErrorFrame{Type: realtimeFrameError, Code: realtimeBadRequest, Message: "Malformed JSON"})
		return eventsSelectionResult{close: true, invalid: "malformed json"}
	}
	if base.Type != requiredActionSubscribe {
		_ = output.write(realtimeErrorFrame{Type: realtimeFrameError, Code: realtimeBadRequest, Message: "Unknown message type"})
		return eventsSelectionResult{close: true, invalid: "unknown message type"}
	}
	var message evt.EventsSubscribeMessage
	if err := readWebSocketJSON(data, &message); err != nil {
		_ = output.write(realtimeErrorFrame{Type: realtimeFrameError, Code: realtimeBadRequest, Message: "Invalid subscribe payload"})
		return eventsSelectionResult{close: true, invalid: "invalid subscribe payload"}
	}
	next, accepted, rejected := resolveChannelSelection(message.Channels, allowed, boundProfileID)
	if err := h.admitEventsSelection(r.Context(), claims.UserID); err != nil {
		if refusal, ok := errors.AsType[*realtimeadmission.Refusal](err); ok {
			writeErr := output.write(realtimeRefusalFrame(refusal, message.RequestID))
			return eventsSelectionResult{refusal: refusal, close: writeErr != nil}
		}
		return eventsSelectionResult{close: true}
	}
	if err := output.write(evt.EventsSubscribedMessage{Type: frameTypeSubscribed, RequestID: message.RequestID, Channels: accepted, Rejected: rejected}); err != nil {
		return eventsSelectionResult{close: true}
	}
	for _, channel := range accepted {
		if err := output.snapshot(channel); err != nil {
			return eventsSelectionResult{close: true}
		}
	}
	return eventsSelectionResult{selection: next, applied: true}
}
