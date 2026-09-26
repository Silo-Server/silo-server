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
		hasViewers := false
		if live != nil && live.room.Phase != RoomPhaseEnded {
			for _, member := range live.members {
				if member != nil && member.connection != nil {
					hasViewers = true
					break
				}
			}
		}
		s.mu.Unlock()
		if !hasViewers {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		rows, err := s.suggestions.ListSuggestions(ctx, incoming.RoomID, 0, "")
		cancel()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.rooms[incoming.RoomID] != live {
			s.mu.Unlock()
			return
		}
		dispatches := s.prepareSuggestionDispatchesLocked(live, rows)
		s.mu.Unlock()
		s.runDispatches(dispatches)
		return
	}
	if event.Type != "watch_together_room_state" {
		return
	}
	var incoming clusterRoomEvent
	if json.Unmarshal([]byte(event.Payload), &incoming) != nil || incoming.Source == s.instanceID || incoming.RoomID == "" {
		return
	}
	s.queueRoomReconciliation(incoming.RoomID)
}

// queueRoomReconciliation keeps SQL and row-lock waits off the PubSub reader.
// Each locally active room has at most one worker and one pending refresh.
func (s *Service) queueRoomReconciliation(roomID string) {
	s.mu.Lock()
	select {
	case <-s.janitorStop:
		s.mu.Unlock()
		return
	default:
	}
	live := s.rooms[roomID]
	if live == nil || !hasLocalRoomWork(live) {
		s.mu.Unlock()
		return
	}
	if live.reconciling {
		live.reconcilePending = true
		s.mu.Unlock()
		return
	}
	live.reconciling = true
	s.mu.Unlock()
	go func() {
		for {
			_ = s.reconcileRoom(context.Background(), roomID)
			s.mu.Lock()
			stopped := false
			select {
			case <-s.janitorStop:
				stopped = true
			default:
			}
			if stopped || s.rooms[roomID] != live || !live.reconcilePending {
				live.reconciling = false
				live.reconcilePending = false
				s.mu.Unlock()
				return
			}
			live.reconcilePending = false
			s.mu.Unlock()
		}
	}()
}
