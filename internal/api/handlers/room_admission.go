package handlers

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Silo-Server/silo-server/internal/realtimeadmission"
)

type roomClientMessageError struct {
	cause       error
	requestID   string
	workStarted bool
}

func (e *roomClientMessageError) Error() string { return e.cause.Error() }
func (e *roomClientMessageError) Unwrap() error { return e.cause }

func (c *watchTogetherRoomConn) writeClientError(err error) {
	frame := realtimeErrorFrame{Type: realtimeFrameError, Code: realtimeBadRequest, Message: err.Error(), WorkStarted: new(false)}
	if clientErr, ok := errors.AsType[*roomClientMessageError](err); ok {
		frame.RequestID = clientErr.requestID
		frame.WorkStarted = new(clientErr.workStarted)
	}
	if refusal, ok := errors.AsType[*realtimeadmission.Refusal](err); ok {
		frame = realtimeRefusalFrame(refusal, frame.RequestID)
	}
	_ = c.WriteJSON(frame)
}

func (c *watchTogetherRoomConn) finishClientAction(message watchTogetherClientMessage, err error) error {
	if err != nil || !c.includeMemberStatus {
		return err
	}
	// Service dispatches enqueue synchronously after commit and before the
	// service returns, so this follows the operation's snapshot/command frames.
	return c.WriteJSON(struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id,omitempty"`
		Action    string `json:"action"`
		Status    string `json:"status"`
	}{Type: "action_result", RequestID: message.RequestID, Action: message.Type, Status: roomActionApplied})
}

func (c *watchTogetherRoomConn) closeInputViolation() {
	data, _ := json.Marshal(realtimeInputError())
	if c.enqueue(watchTogetherFrame{data: data, policyClose: true}) != nil {
		return
	}
	timer := time.NewTimer(watchTogetherReplacementTimeout)
	defer timer.Stop()
	select {
	case <-c.done:
	case <-timer.C:
		_ = c.Close()
	}
}
