package watchtogether

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"

	"github.com/jackc/pgx/v5"
)

const (
	connectionLease       = 45 * time.Second
	leaseRefreshInterval  = 15 * time.Second
	roomReconcileInterval = 2 * time.Second
)

// Room mutations and their membership/readiness changes commit together under
// the room row lock. PubSub only wakes readers; correctness does not depend on
// a message arriving or on a particular API server surviving.
type roomRuntime struct {
	SelectionRevision int64                    `json:"selection_revision"`
	Command           *TransportCommand        `json:"command,omitempty"`
	Members           map[string]runtimeMember `json:"members"`
	BufferingWaitAt   time.Time                `json:"buffering_wait_at,omitzero"`
}

type runtimeMember struct {
	UserID            int               `json:"user_id"`
	ProfileID         string            `json:"profile_id"`
	DisplayName       string            `json:"display_name"`
	ConnectionID      string            `json:"connection_id"`
	Connected         bool              `json:"connected"`
	LeaseUntil        time.Time         `json:"lease_until"`
	DisconnectedAt    time.Time         `json:"disconnected_at"`
	SessionID         string            `json:"session_id"`
	IsReady           bool              `json:"is_ready"`
	IsBuffering       bool              `json:"is_buffering"`
	IgnoreWait        bool              `json:"ignore_wait"`
	LastPingMS        int64             `json:"last_ping_ms"`
	CorrectionCommand *TransportCommand `json:"correction_command,omitempty"`
	WaitingCommand    *TransportCommand `json:"waiting_command,omitempty"`
	SyncingToRoom     bool              `json:"syncing_to_room,omitempty"`
	LobbyReady        bool              `json:"lobby_ready,omitempty"`
	LastStallAt       time.Time         `json:"last_stall_at,omitzero"`
}

type roomOperationKey struct{}
type roomOperation struct {
	tx          pgx.Tx
	afterCommit []func()
	publish     *Room
}

func operationFrom(ctx context.Context) *roomOperation {
	op, _ := ctx.Value(roomOperationKey{}).(*roomOperation)
	return op
}

func afterRoomCommit(ctx context.Context, fn func()) {
	if op := operationFrom(ctx); op != nil {
		op.afterCommit = append(op.afterCommit, fn)
	} else {
		fn()
	}
}

func (r *Repository) queryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if op := operationFrom(ctx); op != nil {
		return op.tx.QueryRow(ctx, sql, args...)
	}
	return r.pool.QueryRow(ctx, sql, args...)
}

func memberConnected(member *memberState) bool {
	return member != nil && (member.connection != nil || member.remoteConnected)
}

func withRoomOperation[T any](ctx context.Context, s *Service, roomID string, fn func(context.Context) (T, error)) (result T, err error) {
	if s == nil || s.repo == nil {
		return result, fmt.Errorf("watch together unavailable")
	}
	repo, shared := s.repo.(*Repository)
	if !shared || operationFrom(ctx) != nil {
		return fn(ctx)
	}
	// Keep this process's post-commit deliveries in the same order as its
	// transactions. Other rooms have independent locks and socket queues.
	s.mu.Lock()
	gate := s.rooms[roomID]
	if gate == nil {
		gate = &liveRoom{members: make(map[string]*memberState)}
		s.rooms[roomID] = gate
	}
	gate.activeOperations++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		gate.activeOperations--
		if gate.activeOperations == 0 && gate.room.Phase == RoomPhaseEnded && s.rooms[roomID] == gate {
			delete(s.rooms, roomID)
		}
		s.mu.Unlock()
	}()
	gate.operationMu.Lock()
	defer gate.operationMu.Unlock()
	defer func() {
		if err == nil {
			return
		}
		s.mu.Lock()
		if s.rooms[roomID] == gate && gate.room.ID == "" {
			delete(s.rooms, roomID)
		}
		s.mu.Unlock()
	}()
	// Bound row-lock waits as well as SQL, including background callbacks.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer rollbackCancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	room, err := scanRoom(tx.QueryRow(ctx, `SELECT `+roomColumns+` FROM watch_together_rooms WHERE id=$1 FOR UPDATE`, roomID))
	if err != nil {
		return result, err
	}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT runtime FROM watch_together_rooms WHERE id=$1`, roomID).Scan(&raw); err != nil {
		return result, err
	}
	var state roomRuntime
	if err = json.Unmarshal(raw, &state); err != nil {
		return result, err
	}
	op := &roomOperation{tx: tx}
	ctx = context.WithValue(ctx, roomOperationKey{}, op)
	s.mu.Lock()
	live := s.rooms[roomID]
	if live == nil {
		live = &liveRoom{members: make(map[string]*memberState)}
		s.rooms[roomID] = live
	}
	previousRoom, previousCommand, previousBufferingWaitAt := live.room, live.command, live.bufferingWaitAt
	previousMembers := make(map[string]*memberState, len(live.members))
	for key, member := range live.members {
		copy := *member
		previousMembers[key] = &copy
	}
	s.adoptRuntimeLocked(ctx, live, *room, state)
	s.mu.Unlock()

	result, err = fn(ctx)
	if err == nil {
		s.mu.Lock()
		updated := s.runtimeLocked(live)
		updatedRoom := live.room
		s.mu.Unlock()
		var encoded []byte
		encoded, err = json.Marshal(updated)
		if err == nil {
			// jsonb normalizes whitespace; compare decoded values to avoid
			// turning every reconciliation read into a database write.
			original, _ := json.Marshal(state)
			if !bytes.Equal(encoded, original) {
				_, err = tx.Exec(ctx, `UPDATE watch_together_rooms SET runtime=$2 WHERE id=$1`, roomID, encoded)
				if err == nil {
					s.publishRoomStateAfterCommit(ctx, updatedRoom)
				}
			}
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
	}
	if err != nil {
		s.mu.Lock()
		if s.rooms[roomID] == live || s.rooms[roomID] == nil {
			s.rooms[roomID] = live
			live.room, live.command, live.members = previousRoom, previousCommand, previousMembers
			live.bufferingWaitAt = previousBufferingWaitAt
			live.broadcastState = ""
		}
		s.mu.Unlock()
		return result, err
	}
	for _, dispatch := range op.afterCommit {
		dispatch()
	}
	if op.publish != nil {
		s.publishRoomState(*op.publish)
	}
	return result, nil
}

func (s *Service) runtimeLocked(live *liveRoom) roomRuntime {
	runtime := roomRuntime{
		SelectionRevision: live.room.SelectionRevision, Command: live.command, BufferingWaitAt: live.bufferingWaitAt,
		Members: make(map[string]runtimeMember, len(live.members)),
	}
	for key, m := range live.members {
		runtime.Members[key] = runtimeMember{
			UserID: m.userID, ProfileID: m.profileID, DisplayName: m.displayName, ConnectionID: m.connectionID,
			Connected: memberConnected(m), LeaseUntil: m.leaseUntil, DisconnectedAt: m.disconnectedAt,
			SessionID: m.sessionID, IsReady: m.isReady, IsBuffering: m.isBuffering, IgnoreWait: m.ignoreWait,
			LastPingMS: m.lastPingMS, CorrectionCommand: m.correctionCommand, WaitingCommand: m.waitingCommand,
			SyncingToRoom: m.syncingToRoom, LobbyReady: m.lobbyReady, LastStallAt: m.lastStallAt,
		}
	}
	return runtime
}

func (s *Service) adoptRuntimeLocked(ctx context.Context, live *liveRoom, room Room, state roomRuntime) {
	now := s.now()
	selectionChanged := state.SelectionRevision != room.SelectionRevision
	if live.room.PlaybackState != room.PlaybackState || live.room.SelectionRevision != room.SelectionRevision ||
		(live.command != nil && (state.Command == nil || live.command.CommandID != state.Command.CommandID)) {
		s.disarmWaitingDeadlineLocked(live)
	}
	live.room = room
	live.command = state.Command
	live.bufferingWaitAt = state.BufferingWaitAt
	if selectionChanged {
		live.command = nil
		live.bufferingWaitAt = time.Time{}
		s.disarmWaitingDeadlineLocked(live)
	}
	members := make(map[string]*memberState, len(state.Members))
	for key, stored := range state.Members {
		m := &memberState{
			userID: stored.UserID, profileID: stored.ProfileID, displayName: stored.DisplayName,
			connectionID: stored.ConnectionID, leaseUntil: stored.LeaseUntil, disconnectedAt: stored.DisconnectedAt,
			sessionID: stored.SessionID, isReady: stored.IsReady, isBuffering: stored.IsBuffering,
			ignoreWait: stored.IgnoreWait, lastPingMS: stored.LastPingMS, waitingCommand: stored.WaitingCommand,
			correctionCommand: stored.CorrectionCommand,
			remoteConnected:   stored.Connected, syncingToRoom: stored.SyncingToRoom, lobbyReady: stored.LobbyReady,
			lastStallAt: stored.LastStallAt,
		}
		if old := live.members[key]; old != nil && old.connectionID == stored.ConnectionID && stored.Connected {
			m.connection, m.lastCommandID = old.connection, old.lastCommandID
			if m.connection != nil {
				m.remoteConnected = false
				m.lastPingMS = old.lastPingMS
				if m.leaseUntil.Sub(now) <= connectionLease-leaseRefreshInterval {
					m.leaseUntil = now.Add(connectionLease)
				}
			}
		}
		if m.connection == nil && m.remoteConnected && !m.leaseUntil.After(now) {
			m.remoteConnected = false
			m.disconnectedAt = m.leaseUntil
		}
		if selectionChanged {
			m.resetForSelection()
		}
		// Stage and mode changes clear lobby readiness in the same transaction
		// as the room update. A stale local room must not erase a later ready.
		// Retain detached sessions through the reconnect grace period only.
		if !memberConnected(m) && !m.disconnectedAt.IsZero() && now.Sub(m.disconnectedAt) > s.hostDisconnectTTL && (m.userID != room.HostUserID || m.profileID != room.HostProfileID) {
			continue
		}
		members[key] = m
	}
	for key, old := range live.members {
		if old.connection == nil {
			continue
		}
		current := members[key]
		if current == nil || current.connection != old.connection {
			conn := old.connection
			if room.Phase != RoomPhaseEnded && memberConnected(current) && current.connectionID != old.connectionID {
				afterRoomCommit(ctx, func() { closeReplacedConnection(conn) })
			} else {
				afterRoomCommit(ctx, func() { _ = conn.Close() })
			}
		}
	}
	live.members = members
}

func (s *Service) sendDispatches(ctx context.Context, dispatches []snapshotDispatch) {
	afterRoomCommit(ctx, func() { s.runDispatches(dispatches) })
}
func (s *Service) sendCommandDispatches(ctx context.Context, dispatches []commandDispatch) {
	afterRoomCommit(ctx, func() { s.runCommandDispatches(dispatches) })
}
func (s *Service) publishRoomStateAfterCommit(ctx context.Context, room Room) {
	if op := operationFrom(ctx); op != nil {
		op.publish = &room
		return
	}
	s.publishRoomState(room)
}

// Reconcile repairs missed notifications and renews the leases of sockets this
// process still owns. The same row lock protects expiration and readiness.
func (s *Service) reconcileRoom(ctx context.Context, roomID string) error {
	s.refreshItemEnd(ctx, roomID)
	_, err := withRoomOperation(ctx, s, roomID, func(ctx context.Context) (struct{}, error) {
		s.mu.Lock()
		pending := make([]pendingDisconnect, 0)
		if live := s.rooms[roomID]; live != nil {
			for _, disconnect := range live.pendingDisconnects {
				pending = append(pending, disconnect)
			}
		}
		s.mu.Unlock()
		for _, disconnect := range pending {
			s.disconnect(ctx, disconnect.registration, disconnect.explicit)
			afterRoomCommit(ctx, func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if live := s.rooms[roomID]; live != nil && live.pendingDisconnects[disconnect.registration.memberKey].registration == disconnect.registration {
					delete(live.pendingDisconnects, disconnect.registration.memberKey)
				}
			})
		}
		s.mu.Lock()
		live := s.rooms[roomID]
		if live == nil {
			s.mu.Unlock()
			return struct{}{}, nil
		}
		if live.room.Phase == RoomPhaseEnded {
			dispatches := s.prepareRoomClosedDispatchesLocked(live)
			s.disarmWaitingDeadlineLocked(live)
			// Keep the operation gate until commit; rollback must retain sockets.
			if operationFrom(ctx) == nil {
				delete(s.rooms, roomID)
			}
			s.mu.Unlock()
			s.sendDispatches(ctx, dispatches)
			return struct{}{}, nil
		}
		host := live.members[buildMemberKey(live.room.HostUserID, live.room.HostProfileID)]
		if host != nil && !memberConnected(host) && !host.disconnectedAt.IsZero() && s.now().Sub(host.disconnectedAt) >= s.hostDisconnectTTL {
			user, profile := live.room.HostUserID, live.room.HostProfileID
			s.mu.Unlock()
			return struct{}{}, s.closeRoom(ctx, roomID, user, profile)
		}
		// The item has finished: everyone goes back to the lobby with it
		// staged, as if the host had stopped playback, to pick what's next.
		if s.itemFinishedLocked(live) {
			user, profile := live.room.HostUserID, live.room.HostProfileID
			s.mu.Unlock()
			_, err := s.stopPlaybackOnce(ctx, roomID, user, profile)
			return struct{}{}, err
		}
		force := s.skipUnreadyMembersLocked(live, s.now())
		snapshots, commands := s.maybeResumeFromWaitingLocked(ctx, live, force)
		state := s.runtimeLocked(live)
		for key, member := range state.Members {
			member.LeaseUntil = time.Time{}
			member.LastPingMS = 0
			state.Members[key] = member
		}
		fingerprint, _ := json.Marshal(struct {
			Room    Room
			Runtime roomRuntime
		}{live.room, state})
		if snapshots == nil && string(fingerprint) != live.broadcastState {
			snapshots = s.prepareSnapshotDispatchesLocked(live)
		}
		live.broadcastState = string(fingerprint)
		if command := live.command; command != nil && command.SelectionRevision == live.room.SelectionRevision {
			for _, member := range live.members {
				if member.connection == nil || member.sessionID == "" || member.lastCommandID == command.CommandID {
					continue
				}
				commands = append(commands, s.targetedCommandDispatchesLocked(live, member.sessionID, *command)...)
				member.lastCommandID = command.CommandID
			}
		}
		s.mu.Unlock()
		s.sendDispatches(ctx, snapshots)
		s.sendCommandDispatches(ctx, commands)
		return struct{}{}, nil
	})
	return err
}

func (s *Service) runReconciler() {
	ticker := time.NewTicker(roomReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.janitorStop:
			return
		case <-ticker.C:
			s.mu.Lock()
			rooms := make([]string, 0, len(s.rooms))
			for id, live := range s.rooms {
				if hasLocalRoomWork(live) {
					rooms = append(rooms, id)
				}
			}
			s.mu.Unlock()
			s.reconcileRooms(context.Background(), rooms)
		}
	}
}

func (s *Service) reconcileRooms(ctx context.Context, rooms []string) {
	work := make(chan string, len(rooms))
	for _, id := range rooms {
		work <- id
	}
	close(work)
	var workers sync.WaitGroup
	for range min(8, len(rooms)) {
		workers.Go(func() {
			for id := range work {
				select {
				case <-s.janitorStop:
					return
				case <-ctx.Done():
					return
				default:
				}
				_ = s.reconcileRoom(ctx, id)
			}
		})
	}
	workers.Wait()
}

func (s *Service) Connect(ctx context.Context, roomID string, user int, profile string, conn RoomConnection) (*Registration, Snapshot, error) {
	type connected struct {
		reg      *Registration
		snapshot Snapshot
	}
	displayName := fallbackMemberName
	if s.profileNames != nil {
		displayName = s.profileNames.ProfileDisplayName(ctx, user, profile)
	}
	result, err := withRoomOperation(ctx, s, roomID, func(ctx context.Context) (connected, error) {
		reg, snapshot, err := s.connect(ctx, roomID, user, profile, conn, displayName)
		return connected{reg, snapshot}, err
	})
	return result.reg, result.snapshot, err
}

func (s *Service) Disconnect(reg *Registration, explicit bool) {
	if s == nil || reg == nil {
		return
	}
	_, err := withRoomOperation(context.Background(), s, reg.roomID, func(ctx context.Context) (struct{}, error) { s.disconnect(ctx, reg, explicit); return struct{}{}, nil })
	if err == nil {
		return
	}
	// Socket teardown is irreversible even when its database transaction rolls
	// back. Stop renewing the closed connection and retry through reconciliation.
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.rooms[reg.roomID]
	if live == nil {
		return
	}
	member := live.members[reg.memberKey]
	if member == nil || member.connection != reg.connection {
		return
	}
	member.connection = nil
	member.remoteConnected = false
	if live.pendingDisconnects == nil {
		live.pendingDisconnects = make(map[string]pendingDisconnect)
	}
	live.pendingDisconnects[reg.memberKey] = pendingDisconnect{registration: reg, explicit: explicit}
}

func (s *Service) AttachSessionForConnection(ctx context.Context, reg *Registration, user int, profile, sessionID string) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}
	session, err := s.lookupSession(ctx, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	if session == nil || session.UserID != user || session.ProfileID != profile || s.files == nil {
		return Snapshot{}, ErrSessionMismatch
	}
	file, err := s.files.GetByID(ctx, session.MediaFileID)
	if err != nil {
		return Snapshot{}, err
	}
	return withRoomOperation(ctx, s, reg.roomID, func(ctx context.Context) (Snapshot, error) {
		return s.attachSessionForConnection(ctx, reg, user, profile, sessionID, session, file)
	})
}
func (s *Service) HandleTransportRequestForConnection(ctx context.Context, reg *Registration, user int, profile string, request TransportRequest) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}
	return withRoomOperation(ctx, s, reg.roomID, func(ctx context.Context) (Snapshot, error) {
		return s.handleTransportRequestForConnection(ctx, reg, user, profile, request)
	})
}
func (s *Service) HandleReadyForConnection(ctx context.Context, reg *Registration, user int, profile string, report StateReport) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}
	return withRoomOperation(ctx, s, reg.roomID, func(ctx context.Context) (Snapshot, error) {
		return s.handleReadyForConnection(ctx, reg, user, profile, report)
	})
}
func (s *Service) HandleBufferingForConnection(ctx context.Context, reg *Registration, user int, profile string, report StateReport) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}
	return withRoomOperation(ctx, s, reg.roomID, func(ctx context.Context) (Snapshot, error) {
		return s.handleBufferingForConnection(ctx, reg, user, profile, report)
	})
}
func (s *Service) HandleStateReportForConnection(ctx context.Context, reg *Registration, user int, profile string, report StateReport) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}
	return withRoomOperation(ctx, s, reg.roomID, func(ctx context.Context) (Snapshot, error) {
		return s.handleStateReportForConnection(ctx, reg, user, profile, report)
	})
}
func (s *Service) CloseRoom(ctx context.Context, roomID string, user int, profile string) error {
	_, err := withRoomOperation(ctx, s, roomID, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.closeRoom(ctx, roomID, user, profile)
	})
	return err
}
func (s *Service) handleWaitingDeadline(roomID string, epoch int64) {
	_, _ = withRoomOperation(context.Background(), s, roomID, func(ctx context.Context) (struct{}, error) {
		s.waitingDeadline(ctx, roomID, epoch)
		return struct{}{}, nil
	})
}

func (s *Service) closeIfHostStillDisconnected(roomID string, user int, profile string) {
	_, _ = withRoomOperation(context.Background(), s, roomID, func(ctx context.Context) (struct{}, error) {
		s.mu.Lock()
		live := s.rooms[roomID]
		connected := live != nil && s.hostConnectedLocked(live)
		if live != nil {
			host := live.members[buildMemberKey(user, profile)]
			if host != nil && !host.disconnectedAt.IsZero() && s.now().Sub(host.disconnectedAt) < s.hostDisconnectTTL {
				connected = true
			}
		}
		s.mu.Unlock()
		if connected {
			return struct{}{}, nil
		}
		return struct{}{}, s.closeRoom(ctx, roomID, user, profile)
	})
}

func (s *Service) UpdatePolicy(ctx context.Context, roomID string, user int, profile string, policy GuestControlPolicy) (Snapshot, error) {
	return withRoomOperation(ctx, s, roomID, func(ctx context.Context) (Snapshot, error) { return s.updatePolicy(ctx, roomID, user, profile, policy) })
}

// StageItem puts content on a host-pick lobby without starting it.
func (s *Service) StageItem(ctx context.Context, roomID string, user int, profile string, input SelectItemInput) (Snapshot, error) {
	return withRoomOperation(ctx, s, roomID, func(ctx context.Context) (Snapshot, error) { return s.stageItem(ctx, roomID, user, profile, input) })
}

// UpdateSelectionMode switches a lobby between host picks and voting.
func (s *Service) UpdateSelectionMode(ctx context.Context, roomID string, user int, profile string, mode RoomSelectionMode) (Snapshot, error) {
	return withRoomOperation(ctx, s, roomID, func(ctx context.Context) (Snapshot, error) {
		return s.updateSelectionMode(ctx, roomID, user, profile, mode)
	})
}

// HandleLobbyReadyForConnection records a member's lobby "I'm ready".
func (s *Service) HandleLobbyReadyForConnection(ctx context.Context, reg *Registration, user int, profile string, ready bool) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}
	return withRoomOperation(ctx, s, reg.roomID, func(ctx context.Context) (Snapshot, error) {
		return s.handleLobbyReadyForConnection(ctx, reg, user, profile, ready)
	})
}

// StartStagedOnce starts a lobby's staged item; a playing room is a no-op receipt.
func (s *Service) StartStagedOnce(ctx context.Context, roomID string, user int, profile string) (Snapshot, error) {
	return withRoomOperation(ctx, s, roomID, func(ctx context.Context) (Snapshot, error) { return s.startStagedOnce(ctx, roomID, user, profile) })
}

// StopPlaybackOnce returns a playing room to its lobby with the item still staged.
func (s *Service) StopPlaybackOnce(ctx context.Context, roomID string, user int, profile string) (Snapshot, error) {
	return withRoomOperation(ctx, s, roomID, func(ctx context.Context) (Snapshot, error) { return s.stopPlaybackOnce(ctx, roomID, user, profile) })
}
func (s *Service) selectItemOnce(ctx context.Context, roomID string, user int, profile string, input SelectItemInput, viaVote, promotion bool) (Snapshot, error) {
	resolved, err := s.resolveSelection(ctx, user, profile, input)
	if err != nil {
		return Snapshot{}, err
	}
	return withRoomOperation(ctx, s, roomID, func(ctx context.Context) (Snapshot, error) {
		return s.selectItemOnceInRoom(ctx, roomID, user, profile, resolved, viaVote, promotion)
	})
}

func (s *Service) selectItem(ctx context.Context, roomID string, user int, profile string, input SelectItemInput, viaVote bool) (Snapshot, error) {
	resolved, err := s.resolveSelection(ctx, user, profile, input)
	if err != nil {
		return Snapshot{}, err
	}
	return withRoomOperation(ctx, s, roomID, func(ctx context.Context) (Snapshot, error) {
		return s.selectItemInRoom(ctx, roomID, user, profile, resolved, viaVote)
	})
}

func (s *Service) Snapshot(ctx context.Context, roomID string, user int, profile string) (Snapshot, error) {
	return withRoomOperation(ctx, s, roomID, func(ctx context.Context) (Snapshot, error) { return s.snapshot(ctx, roomID, user, profile) })
}
func (s *Service) resolveSelection(ctx context.Context, user int, profile string, input SelectItemInput) (*ResolvedSelection, error) {
	if strings.TrimSpace(input.ContentID) == "" {
		return nil, ErrInvalidSelection
	}
	if s == nil || s.selectionResolver == nil {
		return nil, fmt.Errorf("watch together selection resolver unavailable")
	}
	resolved, err := s.selectionResolver.ResolveSelection(ctx, user, profile, input)
	if err != nil {
		if errors.Is(err, catalog.ErrWatchTargetNotPlayable) {
			return nil, ErrInvalidSelection
		}
		return nil, err
	}
	if resolved == nil || strings.TrimSpace(resolved.ContentID) == "" {
		return nil, ErrInvalidSelection
	}
	return resolved, nil
}

// The initial selection can precede playback attachment by an arbitrary time.
// Start its deadline with the first waiting command, not the selection's age.
func waitingDeadlineReached(live *liveRoom, now time.Time) bool {
	if live.room.PlaybackState != RoomPlaybackStateWaiting {
		return false
	}
	var started time.Time
	consider := func(command *TransportCommand) {
		if command == nil || command.PlaybackState != RoomPlaybackStateWaiting || command.SelectionRevision != live.room.SelectionRevision {
			return
		}
		issued, err := time.Parse(time.RFC3339Nano, command.IssuedAt)
		if err == nil && (started.IsZero() || issued.Before(started)) {
			started = issued
		}
	}
	if live.command != nil {
		consider(live.command)
	} else {
		for _, member := range live.members {
			if memberConnected(member) && member.sessionID != "" {
				consider(member.waitingCommand)
			}
		}
	}
	return !started.IsZero() && now.Sub(started) >= waitingResumeDeadline
}
