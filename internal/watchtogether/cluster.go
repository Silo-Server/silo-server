package watchtogether

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

type clusterRoomEvent struct {
	Source     string `json:"source"`
	RoomID     string `json:"room_id"`
	Generation int64  `json:"generation"`
}

type clusterSuggestionEvent struct {
	Source string `json:"source"`
	RoomID string `json:"room_id"`
}

const (
	clusterMessageTypeKey       = "type"
	suggestionsUpdateType       = "suggestions_update"
	clusterSuggestionsEventType = "watch_together_suggestions"
)

func (s *Service) publishSuggestionUpdate(roomID string) {
	if s == nil {
		return
	}
	s.clusterMu.Lock()
	bus := s.clusterBus
	s.clusterMu.Unlock()
	if bus == nil {
		return
	}
	payload, err := json.Marshal(clusterSuggestionEvent{Source: s.instanceID, RoomID: roomID})
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = bus.Publish(ctx, cache.ChannelPlayback, cache.Event{Type: clusterSuggestionsEventType, Payload: string(payload)})
	}()
}

func (s *Service) publishRoomState(room Room) {
	if s == nil {
		return
	}
	s.clusterMu.Lock()
	bus := s.clusterBus
	s.clusterMu.Unlock()
	if bus == nil {
		return
	}
	payload, err := json.Marshal(clusterRoomEvent{Source: s.instanceID, RoomID: room.ID, Generation: room.Generation})
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = bus.Publish(ctx, cache.ChannelPlayback, cache.Event{Type: "watch_together_room_state", Payload: string(payload)})
	}()
}

func (s *Service) handleClusterEvent(event cache.Event) {
	if event.Type == clusterSuggestionsEventType {
		if s.suggestions == nil {
			return
		}
		var incoming clusterSuggestionEvent
		if json.Unmarshal([]byte(event.Payload), &incoming) != nil || incoming.Source == s.instanceID || incoming.RoomID == "" {
			return
		}
		s.mu.Lock()
		live := s.rooms[incoming.RoomID]
		members := make([]*memberState, 0)
		if live != nil && live.room.Phase != RoomPhaseEnded {
			for _, member := range live.members {
				if member != nil && member.connection != nil {
					members = append(members, member)
				}
			}
		}
		s.mu.Unlock()
		for _, member := range members {
			memberCtx, memberCancel := context.WithTimeout(context.Background(), 2*time.Second)
			rows, err := s.suggestions.ListSuggestions(memberCtx, incoming.RoomID, member.userID, member.profileID)
			memberCancel()
			if err == nil {
				s.runDispatches([]snapshotDispatch{{conn: member.connection, payload: map[string]any{clusterMessageTypeKey: suggestionsUpdateType, "suggestions": rows}}})
			}
		}
		return
	}
	if event.Type != "watch_together_room_state" {
		return
	}
	var incoming clusterRoomEvent
	if json.Unmarshal([]byte(event.Payload), &incoming) != nil || incoming.Source == s.instanceID || incoming.RoomID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	room, err := s.repo.GetRoomByID(ctx, incoming.RoomID)
	if err != nil || room == nil {
		return
	}
	s.mu.Lock()
	live := s.rooms[incoming.RoomID]
	if live == nil {
		s.mu.Unlock()
		return
	}
	if room.Phase == RoomPhaseEnded {
		dispatches := s.prepareRoomClosedDispatchesLocked(live)
		if live.hostCloseTimer != nil {
			live.hostCloseTimer.Stop()
		}
		if live.waitingTimer != nil {
			live.waitingTimer.Stop()
		}
		delete(s.rooms, incoming.RoomID)
		s.mu.Unlock()
		s.runDispatches(dispatches)
		return
	}
	if room.Generation <= live.room.Generation {
		s.mu.Unlock()
		return
	}
	if room.SelectionRevision != live.room.SelectionRevision {
		s.disarmWaitingDeadlineLocked(live)
		for _, member := range live.members {
			if member == nil {
				continue
			}
			member.sessionID = ""
			member.isReady = false
			member.isBuffering = false
			member.ignoreWait = false
		}
	}
	live.room = *room
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()
	s.runDispatches(dispatches)
}
