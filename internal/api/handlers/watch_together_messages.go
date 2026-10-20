package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Silo-Server/silo-server/internal/realtimeadmission"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func (h *WatchTogetherHandler) handleRoomClientMessage(
	ctx context.Context,
	rc *watchTogetherRoomConn,
	reg *watchtogether.Registration,
	userID int,
	profileID string,
	data []byte,
) (err error) {
	var base watchTogetherClientMessage
	workStarted := false
	defer func() {
		if err != nil {
			err = &roomClientMessageError{cause: err, requestID: base.RequestID, workStarted: workStarted}
		}
	}()
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}

	admit := func(class realtimeadmission.Class) error {
		if err := h.workAdmission().Admit(ctx, class, realtimeadmission.Identity{UserID: userID, ProfileID: profileID, RoomID: rc.roomID}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		workStarted = true
		return nil
	}

	switch base.Type {
	case "attach_session":
		var msg watchTogetherAttachMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.SessionID == "" {
			return errors.New("session_id is required")
		}
		if err := admit(realtimeadmission.RoomRecovery); err != nil {
			return err
		}
		_, err := h.Service.AttachSessionForConnection(ctx, reg, userID, profileID, msg.SessionID)
		return err
	case "transport_request":
		var msg watchTogetherTransportRequestMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.Action != watchtogether.TransportActionPlay && msg.Action != watchtogether.TransportActionPause && msg.Action != watchtogether.TransportActionSeek {
			return errors.New("unsupported transport action")
		}
		if err := admit(realtimeadmission.RoomAction); err != nil {
			return err
		}
		_, err := h.Service.HandleTransportRequestForConnection(ctx, reg, userID, profileID, watchtogether.TransportRequest{
			Action:          msg.Action,
			PositionSeconds: msg.PositionSeconds,
			IsPaused:        msg.IsPaused,
			ReportConflict:  rc.includeMemberStatus,
		})
		return rc.finishClientAction(base, err)
	case roomMessageStateReport, roomMessageReady, roomMessageBuffering:
		return h.handleRoomReportMessage(ctx, reg, userID, profileID, base.Type, data, admit)
	case roomMessageLobbyReady:
		if !rc.includeMemberStatus {
			return errors.New("unsupported room websocket message")
		}
		var msg watchTogetherLobbyReadyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if err := admit(realtimeadmission.RoomAction); err != nil {
			return err
		}
		_, err := h.Service.HandleLobbyReadyForConnection(ctx, reg, userID, profileID, msg.Ready)
		return rc.finishClientAction(base, err)
	case "ping":
		var msg watchTogetherPingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		// The echoed timestamps only serve the client's clock-offset estimate;
		// latency for command scheduling is measured server-side from
		// protocol-level ping/pong.
		now := time.Now().UTC()
		return rc.WriteJSON(map[string]string{
			watchTogetherTypeKey: "pong",
			"client_sent_at":     msg.ClientSentAt,
			"server_received_at": now.Format(time.RFC3339Nano),
			"server_sent_at":     time.Now().UTC().Format(time.RFC3339Nano),
		})
	default:
		return errors.New("unsupported room websocket message")
	}
}

func (h *WatchTogetherHandler) handleRoomReportMessage(ctx context.Context, reg *watchtogether.Registration, userID int, profileID, messageType string, data []byte, admit func(realtimeadmission.Class) error) error {
	var report watchtogether.StateReport
	switch messageType {
	case roomMessageStateReport:
		var msg watchTogetherStateReportMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		report = watchtogether.StateReport{CommandID: msg.CommandID, SessionID: msg.SessionID, PositionSeconds: msg.PositionSeconds, IsPaused: msg.IsPaused, IsReady: msg.IsReady}
	case roomMessageReady:
		var msg watchTogetherReadyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		report = watchtogether.StateReport{CommandID: msg.CommandID, SessionID: msg.SessionID, PositionSeconds: msg.PositionSeconds, IsPaused: msg.IsPaused}
	case roomMessageBuffering:
		var msg watchTogetherBufferingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		report = watchtogether.StateReport{SessionID: msg.SessionID, PositionSeconds: msg.PositionSeconds, IsPaused: msg.IsPaused}
	default:
		return errors.New("unsupported room websocket message")
	}
	if report.SessionID == "" {
		return errors.New("session_id is required")
	}
	if err := admit(realtimeadmission.RoomRecovery); err != nil {
		return err
	}
	var err error
	switch messageType {
	case roomMessageStateReport:
		_, err = h.Service.HandleStateReportForConnection(ctx, reg, userID, profileID, report)
	case roomMessageReady:
		_, err = h.Service.HandleReadyForConnection(ctx, reg, userID, profileID, report)
	case roomMessageBuffering:
		_, err = h.Service.HandleBufferingForConnection(ctx, reg, userID, profileID, report)
	}
	return err
}
