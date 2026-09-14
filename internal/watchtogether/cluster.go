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

func (s *Service) publishSuggestionUpdate(roomID string) {
	if s == nil || s.clusterBus == nil {
		return
	}
	payload, err := json.Marshal(clusterSuggestionEvent{Source: s.instanceID, RoomID: roomID})
	if err != nil {
		return
	}
	bus := s.clusterBus
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = bus.Publish(ctx, cache.ChannelPlayback, cache.Event{Type: "watch_together_suggestions", Payload: string(payload)})
	}()
}

func (s *Service) publishRoomState(room Room) {
	if s == nil || s.clusterBus == nil {
		return
	}
	bus := s.clusterBus
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
	if event.Type == "watch_together_suggestions" {
		if s.suggestions == nil {
			return
		}
		var incoming clusterSuggestionEvent
		if json.Unmarshal([]byte(event.Payload), &incoming) != nil || incoming.Source == s.instanceID || incoming.RoomID == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.mu.Lock()
		live := s.rooms[incoming.RoomID]
		members := make([]*memberState, 0)
		if live != nil {
			for _, member := range live.members {
				if member != nil && member.connection != nil {
					members = append(members, member)
				}
			}
		}
		s.mu.Unlock()
		for _, member := range members {
			rows, err := s.suggestions.ListSuggestions(ctx, incoming.RoomID, member.userID, member.profileID)
			if err == nil {
				s.runDispatches([]snapshotDispatch{{conn: member.connection, payload: map[string]any{"type": "suggestions_update", "suggestions": rows}}})
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
	var commands []commandDispatch
	if room.Phase == RoomPhasePlaying {
		action := TransportActionPause
		if room.PlaybackState == RoomPlaybackStatePlaying {
			action = TransportActionPlay
		}
		commands = s.transportCommandDispatchesLocked(live, action, expectedPosition(*room, s.now()), s.now().Add(s.highestPingLocked(live)))
	}
	s.mu.Unlock()
	s.runDispatches(dispatches)
	s.runCommandDispatches(commands)
}
