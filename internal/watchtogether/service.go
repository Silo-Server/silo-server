package watchtogether

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/google/uuid"
)

var (
	ErrRoomClosed            = errors.New("watch together room is closed")
	ErrRoomForbidden         = errors.New("watch together room action forbidden")
	ErrInvalidJoinRequest    = errors.New("watch together join request is invalid")
	ErrSessionMismatch       = errors.New("watch together playback session mismatch")
	ErrTransportNotAllowed   = errors.New("watch together transport action not allowed")
	ErrConnectionNotAttached = errors.New("watch together session is not attached")
	ErrInvalidSelection      = errors.New("watch together selection is invalid")
	ErrSuggestionNotFound    = errors.New("watch together suggestion not found")
	ErrDuplicateVote         = errors.New("watch together already voted")
	ErrNotVoted              = errors.New("watch together not voted")
)

const (
	defaultTransportLead = 500 * time.Millisecond
	minTransportLead     = 350 * time.Millisecond
	// maxTransportLead bounds how far in the future transport commands may be
	// scheduled, so a single member with a huge (or bogus) measured latency
	// cannot stall the whole room.
	maxTransportLead = 5 * time.Second
	// maxBufferingAnchorDriftSeconds bounds how far a buffering member's
	// reported position may move the shared room anchor.
	maxBufferingAnchorDriftSeconds = 5.0
	// waitingResumeDeadline is how long a room stays in the waiting state
	// before stragglers are skipped and playback resumes for everyone ready.
	waitingResumeDeadline = 30 * time.Second
	// roomIdleTTL is how long a room may go without any playback-anchor
	// activity before the janitor closes it.
	roomIdleTTL = 24 * time.Hour
	// janitorInterval is how often idle rooms are swept.
	janitorInterval = 10 * time.Minute
)

type RoomConnection interface {
	WriteJSON(v any) error
	Close() error
}

type RoomStore interface {
	CreateRoom(ctx context.Context, room Room) (*Room, error)
	GetRoomByID(ctx context.Context, roomID string) (*Room, error)
	GetRoomByCode(ctx context.Context, code string) (*Room, error)
	GetRoomByJoinToken(ctx context.Context, joinToken string) (*Room, error)
	UpdatePolicy(ctx context.Context, roomID string, policy GuestControlPolicy, generation int64, expectedGeneration int64) (*Room, error)
	UpdateAnchor(
		ctx context.Context,
		roomID string,
		positionSeconds float64,
		isPaused bool,
		playbackState RoomPlaybackState,
		resumeOnReady bool,
		anchorUpdatedAt time.Time,
		generation int64,
		expectedGeneration int64,
	) (*Room, error)
	CloseRoom(ctx context.Context, roomID string, closedAt time.Time) (*Room, error)
	ListIdleRoomIDs(ctx context.Context, cutoff time.Time, limit int) ([]string, error)
	UpdateSelection(
		ctx context.Context,
		roomID string,
		selection SelectItemInput,
		phase RoomPhase,
		playbackState RoomPlaybackState,
		resumeOnReady bool,
		anchorPosition float64,
		isPaused bool,
		anchorUpdatedAt time.Time,
		selectionRevision int64,
		generation int64,
		expectedGeneration int64,
	) (*Room, error)
}

type RoomSessionLookup interface {
	GetSession(sessionID string) (*playback.Session, error)
}

type MediaFileLookup interface {
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
}

type WatchTogetherSelectionResolver interface {
	ResolveSelection(ctx context.Context, userID int, profileID string, input SelectItemInput) (*ResolvedSelection, error)
}

type Registration struct {
	roomID     string
	memberKey  string
	connection RoomConnection
}

type memberState struct {
	userID      int
	profileID   string
	displayName string
	sessionID   string
	connection  RoomConnection
	isReady     bool
	isBuffering bool
	// ignoreWait excludes a member from room-wide readiness barriers. It is
	// set when the member fails to become ready before waitingResumeDeadline
	// and cleared once they attach or report ready again.
	ignoreWait bool
	lastPingMS int64
}

type liveRoom struct {
	room           Room
	members        map[string]*memberState
	hostCloseTimer *time.Timer
	waitingTimer   *time.Timer
	// waitingEpoch identifies the current waiting period; deadline callbacks
	// carry the epoch they were armed for so a stale timer cannot act on a
	// newer waiting period.
	waitingEpoch int64
}

type snapshotDispatch struct {
	conn    RoomConnection
	payload map[string]any
}

type commandDispatch struct {
	conn      RoomConnection
	payload   map[string]any
	memberKey string
}

type Service struct {
	repo              RoomStore
	suggestions       SuggestionStore
	sessions          RoomSessionLookup
	files             MediaFileLookup
	selectionResolver WatchTogetherSelectionResolver
	profileNames      ProfileNameResolver
	hostDisconnectTTL time.Duration
	now               func() time.Time

	janitorStop chan struct{}

	mu    sync.Mutex
	rooms map[string]*liveRoom
}

func NewService(
	repo RoomStore,
	sessions RoomSessionLookup,
	files MediaFileLookup,
	selectionResolver WatchTogetherSelectionResolver,
	suggestions SuggestionStore,
	profileNames ProfileNameResolver,
) *Service {
	s := &Service{
		repo:              repo,
		suggestions:       suggestions,
		sessions:          sessions,
		files:             files,
		selectionResolver: selectionResolver,
		profileNames:      profileNames,
		hostDisconnectTTL: 15 * time.Second,
		now: func() time.Time {
			return time.Now().UTC()
		},
		janitorStop: make(chan struct{}),
		rooms:       make(map[string]*liveRoom),
	}
	go s.runJanitor()
	return s
}

// Close stops the service's background maintenance loop.
func (s *Service) Close() {
	if s == nil || s.janitorStop == nil {
		return
	}
	select {
	case <-s.janitorStop:
	default:
		close(s.janitorStop)
	}
}

func (s *Service) CreateRoom(ctx context.Context, input CreateRoomInput) (*Room, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("watch together service unavailable")
	}

	now := s.now()
	selectionMode := input.SelectionMode
	if selectionMode != RoomSelectionModeVote {
		selectionMode = RoomSelectionModeHostPick
	}
	room := Room{
		ID:                    uuid.NewString(),
		Code:                  randomToken(8),
		JoinToken:             randomToken(24),
		HostUserID:            input.HostUserID,
		HostProfileID:         input.HostProfileID,
		Phase:                 RoomPhaseLobby,
		PlaybackState:         RoomPlaybackStateIdle,
		ResumeOnReady:         false,
		SelectionMode:         selectionMode,
		SelectionRevision:     0,
		GuestControlPolicy:    GuestControlPolicyHostOnly,
		AnchorPositionSeconds: 0,
		IsPaused:              true,
		AnchorUpdatedAt:       now,
		Generation:            1,
		CreatedAt:             now,
	}

	created, err := s.repo.CreateRoom(ctx, room)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.rooms[created.ID] = &liveRoom{
		room:    *created,
		members: make(map[string]*memberState),
	}
	s.mu.Unlock()
	return created, nil
}

func (s *Service) JoinRoom(ctx context.Context, input JoinInput) (*Room, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("watch together service unavailable")
	}

	switch {
	case strings.TrimSpace(input.JoinToken) != "":
		return s.loadRoom(ctx, func() (*Room, error) {
			return s.repo.GetRoomByJoinToken(ctx, strings.TrimSpace(input.JoinToken))
		})
	case strings.TrimSpace(input.Code) != "":
		return s.loadRoom(ctx, func() (*Room, error) {
			return s.repo.GetRoomByCode(ctx, strings.TrimSpace(input.Code))
		})
	default:
		return nil, ErrInvalidJoinRequest
	}
}

func (s *Service) GetRoom(ctx context.Context, roomID string) (*Room, error) {
	return s.loadRoom(ctx, func() (*Room, error) {
		return s.repo.GetRoomByID(ctx, roomID)
	})
}

func (s *Service) Snapshot(ctx context.Context, roomID string, userID int, profileID string) (Snapshot, error) {
	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buildSnapshotLocked(live, userID, profileID), nil
}

func (s *Service) Connect(
	ctx context.Context,
	roomID string,
	userID int,
	profileID string,
	conn RoomConnection,
) (*Registration, Snapshot, error) {
	room, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return nil, Snapshot{}, err
	}

	memberKey := buildMemberKey(userID, profileID)
	displayName := fallbackMemberName
	if s.profileNames != nil {
		displayName = s.profileNames.ProfileDisplayName(ctx, userID, profileID)
	}

	s.mu.Lock()
	current := live.members[memberKey]
	var previousConn RoomConnection
	if current == nil {
		current = &memberState{userID: userID, profileID: profileID}
		live.members[memberKey] = current
	} else if current.connection != nil && current.connection != conn {
		previousConn = current.connection
	}
	current.connection = conn
	current.displayName = displayName

	if room.HostUserID == userID && room.HostProfileID == profileID && live.hostCloseTimer != nil {
		live.hostCloseTimer.Stop()
		live.hostCloseTimer = nil
	}

	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()

	if previousConn != nil {
		_ = previousConn.Close()
	}
	s.runDispatches(dispatches)
	return &Registration{roomID: roomID, memberKey: memberKey, connection: conn}, snapshot, nil
}

func (s *Service) Disconnect(reg *Registration, explicitLeave bool) {
	if s == nil || reg == nil {
		return
	}

	s.mu.Lock()
	live := s.rooms[reg.roomID]
	if live == nil {
		s.mu.Unlock()
		return
	}

	member := live.members[reg.memberKey]
	if member == nil || member.connection != reg.connection {
		s.mu.Unlock()
		return
	}

	isHost := member.userID == live.room.HostUserID && member.profileID == live.room.HostProfileID
	member.connection = nil
	member.sessionID = ""
	delete(live.members, reg.memberKey)

	if isHost {
		if explicitLeave {
			s.mu.Unlock()
			_ = s.CloseRoom(context.Background(), reg.roomID, member.userID, member.profileID)
			return
		}
		if live.hostCloseTimer != nil {
			live.hostCloseTimer.Stop()
		}
		roomID := reg.roomID
		hostUserID := live.room.HostUserID
		hostProfileID := live.room.HostProfileID
		live.hostCloseTimer = time.AfterFunc(s.hostDisconnectTTL, func() {
			s.closeIfHostStillDisconnected(roomID, hostUserID, hostProfileID)
		})
	}

	// A departing member may have been the last participant the room was
	// waiting on; re-evaluate readiness so the others aren't stuck.
	dispatches, commandDispatches := s.maybeResumeFromWaitingLocked(context.Background(), live, false)
	if dispatches == nil {
		dispatches = s.prepareSnapshotDispatchesLocked(live)
	}
	s.mu.Unlock()
	s.runDispatches(dispatches)
	s.runCommandDispatches(commandDispatches)
}

// closeIfHostStillDisconnected closes a room after the host-disconnect grace
// period, unless the host reconnected while the timer was in flight
// (Timer.Stop cannot recall a callback that already fired).
func (s *Service) closeIfHostStillDisconnected(roomID string, hostUserID int, hostProfileID string) {
	s.mu.Lock()
	live := s.rooms[roomID]
	// A nil live room means nobody (host included) is connected; closing is
	// safe. Only a live room with the host re-connected aborts the close.
	if live != nil && s.hostConnectedLocked(live) {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	_ = s.CloseRoom(context.Background(), roomID, hostUserID, hostProfileID)
}

func (s *Service) AttachSessionForConnection(
	ctx context.Context,
	reg *Registration,
	userID int,
	profileID string,
	sessionID string,
) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}

	room, live, err := s.getOrLoadLiveRoom(ctx, reg.roomID)
	if err != nil {
		return Snapshot{}, err
	}

	session, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	if session.UserID != userID || session.ProfileID != profileID {
		return Snapshot{}, ErrSessionMismatch
	}
	if err := s.validateSessionContent(ctx, room, session); err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	member := live.members[reg.memberKey]
	if member == nil || member.connection == nil || member.connection != reg.connection {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomForbidden
	}
	member.sessionID = sessionID
	member.isReady = false
	member.ignoreWait = false
	member.isBuffering = live.room.Phase == RoomPhasePlaying

	var commandDispatches []commandDispatch
	if live.room.Phase == RoomPhasePlaying {
		if live.room.PlaybackState == RoomPlaybackStatePlaying && s.activeParticipantCountLocked(live) > 1 {
			position := s.expectedPositionLocked(live)
			commandDispatches, _ = s.enterWaitingLocked(live, position, true)
			conflict, err := s.persistAnchorLocked(ctx, live)
			if err != nil {
				s.mu.Unlock()
				return Snapshot{}, err
			}
			if conflict {
				snapshot := s.buildSnapshotLocked(live, userID, profileID)
				s.mu.Unlock()
				return snapshot, nil
			}
		} else {
			commandDispatches = s.syncMemberToRoomLocked(live, sessionID)
		}
	}

	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()

	s.runDispatches(dispatches)
	s.runCommandDispatches(commandDispatches)
	return snapshot, nil
}

func (s *Service) HandleTransportRequestForConnection(
	ctx context.Context,
	reg *Registration,
	userID int,
	profileID string,
	request TransportRequest,
) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, reg.roomID)
	if err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	member := live.members[reg.memberKey]
	if member == nil || member.connection == nil || member.connection != reg.connection || member.sessionID == "" {
		s.mu.Unlock()
		return Snapshot{}, ErrConnectionNotAttached
	}
	if err := s.ensureTransportAllowedLocked(live, userID, profileID, request.Action); err != nil {
		s.mu.Unlock()
		return Snapshot{}, err
	}

	position := live.room.AnchorPositionSeconds
	if request.PositionSeconds != nil {
		position = math.Max(0, *request.PositionSeconds)
	} else if !live.room.IsPaused {
		position = s.expectedPositionLocked(live)
	}

	now := s.now()
	live.room.AnchorPositionSeconds = position
	live.room.AnchorUpdatedAt = now
	var commandDispatches []commandDispatch
	executeAt := now.Add(s.highestPingLocked(live))
	switch request.Action {
	case TransportActionPlay:
		live.room.ResumeOnReady = true
		live.room.IsPaused = false
		live.room.PlaybackState = RoomPlaybackStatePlaying
		s.disarmWaitingDeadlineLocked(live)
		commandDispatches = s.transportCommandDispatchesLocked(
			live,
			TransportActionPlay,
			position,
			executeAt,
		)
	case TransportActionPause:
		live.room.ResumeOnReady = false
		live.room.IsPaused = true
		live.room.PlaybackState = RoomPlaybackStatePaused
		s.disarmWaitingDeadlineLocked(live)
		commandDispatches = s.transportCommandDispatchesLocked(
			live,
			TransportActionPause,
			position,
			executeAt,
		)
	case TransportActionSeek:
		live.room.ResumeOnReady = !request.IsPaused
		live.room.IsPaused = true
		live.room.PlaybackState = RoomPlaybackStateWaiting
		s.resetMemberReadinessLocked(live, false)
		s.armWaitingDeadlineLocked(live)
		commandDispatches = s.transportCommandDispatchesLocked(
			live,
			TransportActionSeek,
			position,
			executeAt,
		)
	default:
		s.mu.Unlock()
		return Snapshot{}, ErrTransportNotAllowed
	}
	conflict, updateErr := s.persistAnchorLocked(ctx, live)
	if updateErr != nil {
		s.mu.Unlock()
		return Snapshot{}, updateErr
	}
	if conflict {
		snapshot := s.buildSnapshotLocked(live, userID, profileID)
		s.mu.Unlock()
		return snapshot, nil
	}
	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()

	s.runDispatches(dispatches)
	s.runCommandDispatches(commandDispatches)
	return snapshot, nil
}

func (s *Service) HandleStateReportForConnection(
	ctx context.Context,
	reg *Registration,
	userID int,
	profileID string,
	report StateReport,
) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, reg.roomID)
	if err != nil {
		return Snapshot{}, err
	}

	var dispatches []snapshotDispatch
	var correctionDispatches []commandDispatch

	s.mu.Lock()
	member := live.members[reg.memberKey]
	if member == nil || member.connection == nil || member.connection != reg.connection {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomForbidden
	}
	if member.sessionID == "" || member.sessionID != report.SessionID {
		s.mu.Unlock()
		return Snapshot{}, ErrConnectionNotAttached
	}

	isHost := userID == live.room.HostUserID && profileID == live.room.HostProfileID
	expected := s.expectedPositionLocked(live)
	pauseMismatch := report.IsPaused != live.room.IsPaused
	drift := math.Abs(report.PositionSeconds - expected)

	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	if isHost && (pauseMismatch || drift > 1.5) {
		live.room.AnchorPositionSeconds = math.Max(0, report.PositionSeconds)
		live.room.IsPaused = report.IsPaused
		live.room.AnchorUpdatedAt = s.now()
		conflict, updateErr := s.persistAnchorLocked(ctx, live)
		if updateErr != nil {
			s.mu.Unlock()
			return Snapshot{}, updateErr
		}
		snapshot = s.buildSnapshotLocked(live, userID, profileID)
		if conflict {
			s.mu.Unlock()
			return snapshot, nil
		}
		dispatches = s.prepareSnapshotDispatchesLocked(live)
	} else if !isHost && (pauseMismatch || drift > 1.0) {
		correctionDispatches = s.targetedCommandDispatchesLocked(live, report.SessionID, TransportCommand{
			CommandID:         uuid.NewString(),
			SelectionRevision: live.room.SelectionRevision,
			Action: func() TransportAction {
				if live.room.PlaybackState == RoomPlaybackStatePlaying {
					return TransportActionPlay
				}
				return TransportActionPause
			}(),
			PositionSeconds: math.Max(0, expectedPosition(live.room, s.now())),
			ExecuteAt:       s.now().Add(s.highestPingLocked(live)).UTC().Format(time.RFC3339Nano),
			IssuedAt:        s.now().UTC().Format(time.RFC3339Nano),
			PlaybackState:   live.room.PlaybackState,
		})
	}
	s.mu.Unlock()
	if isHost && (pauseMismatch || drift > 1.5) {
		s.runDispatches(dispatches)
		return snapshot, nil
	}

	if len(correctionDispatches) > 0 {
		s.runCommandDispatches(correctionDispatches)
	}

	return snapshot, nil
}

func (s *Service) HandleReadyForConnection(
	ctx context.Context,
	reg *Registration,
	userID int,
	profileID string,
	report StateReport,
) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, reg.roomID)
	if err != nil {
		return Snapshot{}, err
	}

	var dispatches []snapshotDispatch
	var commandDispatches []commandDispatch

	s.mu.Lock()
	member := live.members[reg.memberKey]
	if member == nil || member.connection == nil || member.connection != reg.connection {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomForbidden
	}
	if member.sessionID == "" || member.sessionID != report.SessionID {
		s.mu.Unlock()
		return Snapshot{}, ErrConnectionNotAttached
	}

	member.isReady = true
	member.isBuffering = false
	member.ignoreWait = false

	dispatches, commandDispatches = s.maybeResumeFromWaitingLocked(ctx, live, false)
	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	if dispatches == nil {
		dispatches = s.prepareSnapshotDispatchesLocked(live)
	}
	s.mu.Unlock()

	s.runDispatches(dispatches)
	s.runCommandDispatches(commandDispatches)
	return snapshot, nil
}

func (s *Service) HandleBufferingForConnection(
	ctx context.Context,
	reg *Registration,
	userID int,
	profileID string,
	report StateReport,
) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, reg.roomID)
	if err != nil {
		return Snapshot{}, err
	}

	var dispatches []snapshotDispatch
	var commandDispatches []commandDispatch

	s.mu.Lock()
	member := live.members[reg.memberKey]
	if member == nil || member.connection == nil || member.connection != reg.connection {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomForbidden
	}
	if member.sessionID == "" || member.sessionID != report.SessionID {
		s.mu.Unlock()
		return Snapshot{}, ErrConnectionNotAttached
	}

	member.isBuffering = true
	member.isReady = false
	// Members already excluded from the readiness barrier must not drag the
	// whole room back into waiting while they catch up.
	if live.room.Phase != RoomPhasePlaying || member.ignoreWait {
		snapshot := s.buildSnapshotLocked(live, userID, profileID)
		s.mu.Unlock()
		return snapshot, nil
	}

	if live.room.PlaybackState != RoomPlaybackStateWaiting {
		// Bound how far a single member's report can move the shared anchor.
		position := math.Max(0, report.PositionSeconds)
		expected := math.Max(0, s.expectedPositionLocked(live))
		if math.Abs(position-expected) > maxBufferingAnchorDriftSeconds {
			position = expected
		}
		commandDispatches, _ = s.enterWaitingLocked(
			live,
			position,
			live.room.PlaybackState == RoomPlaybackStatePlaying || live.room.ResumeOnReady,
		)
		conflict, updateErr := s.persistAnchorLocked(ctx, live)
		if updateErr != nil {
			s.mu.Unlock()
			return Snapshot{}, updateErr
		}
		if conflict {
			snapshot := s.buildSnapshotLocked(live, userID, profileID)
			s.mu.Unlock()
			return snapshot, nil
		}
	}

	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	dispatches = s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()

	s.runDispatches(dispatches)
	s.runCommandDispatches(commandDispatches)
	return snapshot, nil
}

func (s *Service) HandlePingForConnection(
	_ context.Context,
	reg *Registration,
	userID int,
	profileID string,
	pingMS int64,
) error {
	if reg == nil {
		return ErrRoomForbidden
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.rooms[reg.roomID]
	if live == nil {
		return ErrRoomNotFound
	}
	member := live.members[buildMemberKey(userID, profileID)]
	if member == nil || member.connection == nil || member.connection != reg.connection {
		return ErrRoomForbidden
	}
	if pingMS > 0 {
		if maxMS := maxTransportLead.Milliseconds(); pingMS > maxMS {
			pingMS = maxMS
		}
		member.lastPingMS = pingMS
	}
	return nil
}

func (s *Service) UpdatePolicy(
	ctx context.Context,
	roomID string,
	userID int,
	profileID string,
	policy GuestControlPolicy,
) (Snapshot, error) {
	if policy != GuestControlPolicyHostOnly && policy != GuestControlPolicyGuestPlayPause {
		return Snapshot{}, ErrTransportNotAllowed
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	if live.room.HostUserID != userID || live.room.HostProfileID != profileID {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomForbidden
	}

	live.room.GuestControlPolicy = policy
	conflict, updateErr := s.persistRoomChangeLocked(ctx, live, func(room Room, expectedGeneration int64) (*Room, error) {
		return s.repo.UpdatePolicy(ctx, roomID, room.GuestControlPolicy, room.Generation, expectedGeneration)
	})
	if updateErr != nil {
		s.mu.Unlock()
		return Snapshot{}, updateErr
	}
	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	if conflict {
		s.mu.Unlock()
		return snapshot, nil
	}
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()

	s.runDispatches(dispatches)
	return snapshot, nil
}

func (s *Service) SelectItem(
	ctx context.Context,
	roomID string,
	userID int,
	profileID string,
	input SelectItemInput,
) (Snapshot, error) {
	if strings.TrimSpace(input.ContentID) == "" {
		return Snapshot{}, ErrInvalidSelection
	}
	if s.selectionResolver == nil {
		return Snapshot{}, fmt.Errorf("watch together selection resolver unavailable")
	}

	resolved, err := s.selectionResolver.ResolveSelection(ctx, userID, profileID, input)
	if err != nil {
		if errors.Is(err, catalog.ErrWatchTargetNotPlayable) {
			return Snapshot{}, ErrInvalidSelection
		}
		return Snapshot{}, err
	}
	if resolved == nil || strings.TrimSpace(resolved.ContentID) == "" {
		return Snapshot{}, ErrInvalidSelection
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	if live.room.HostUserID != userID || live.room.HostProfileID != profileID {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomForbidden
	}
	if live.room.Phase == RoomPhaseEnded {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomClosed
	}

	now := s.now()
	live.room.Phase = RoomPhasePlaying
	live.room.PlaybackState = RoomPlaybackStateWaiting
	live.room.ResumeOnReady = true
	live.room.SelectedContentID = &resolved.ContentID
	live.room.SelectedFileID = resolved.FileID
	live.room.SelectedLibraryID = resolved.LibraryID
	live.room.AnchorPositionSeconds = 0
	live.room.IsPaused = true
	live.room.AnchorUpdatedAt = now
	live.room.SelectionRevision++
	// Sessions attached for the previous selection are stale: readiness for
	// the new content must come from a fresh attach, not an old session.
	for _, member := range live.members {
		if member == nil {
			continue
		}
		member.sessionID = ""
		member.isReady = false
		member.isBuffering = false
		member.ignoreWait = false
	}
	s.disarmWaitingDeadlineLocked(live)

	conflict, updateErr := s.persistRoomChangeLocked(ctx, live, func(room Room, expectedGeneration int64) (*Room, error) {
		return s.repo.UpdateSelection(
			ctx,
			roomID,
			SelectItemInput{
				ContentID: resolved.ContentID,
				FileID:    resolved.FileID,
				LibraryID: resolved.LibraryID,
			},
			room.Phase,
			room.PlaybackState,
			room.ResumeOnReady,
			room.AnchorPositionSeconds,
			room.IsPaused,
			room.AnchorUpdatedAt,
			room.SelectionRevision,
			room.Generation,
			expectedGeneration,
		)
	})
	if updateErr != nil {
		s.mu.Unlock()
		return Snapshot{}, updateErr
	}
	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	if conflict {
		s.mu.Unlock()
		return snapshot, nil
	}
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()

	s.runDispatches(dispatches)
	return snapshot, nil
}

func (s *Service) CloseRoom(ctx context.Context, roomID string, userID int, profileID string) error {
	if s == nil || s.repo == nil {
		return fmt.Errorf("watch together service unavailable")
	}

	var roomForAuth *Room
	s.mu.Lock()
	live := s.rooms[roomID]
	if live != nil {
		roomCopy := live.room
		roomForAuth = &roomCopy
	}
	s.mu.Unlock()

	if roomForAuth == nil {
		var err error
		roomForAuth, err = s.GetRoom(ctx, roomID)
		if err != nil {
			return err
		}
	}

	if roomForAuth.HostUserID != userID || roomForAuth.HostProfileID != profileID {
		return ErrRoomForbidden
	}

	closedAt := s.now()
	room, err := s.repo.CloseRoom(ctx, roomID, closedAt)
	if err != nil {
		return err
	}

	var dispatches []snapshotDispatch
	s.mu.Lock()
	live = s.rooms[roomID]
	if live != nil {
		live.room = *room
		dispatches = s.prepareRoomClosedDispatchesLocked(live)
		if live.hostCloseTimer != nil {
			live.hostCloseTimer.Stop()
		}
		if live.waitingTimer != nil {
			live.waitingTimer.Stop()
		}
		delete(s.rooms, roomID)
	}
	s.mu.Unlock()

	s.runDispatches(dispatches)
	return nil
}

func (s *Service) loadRoom(ctx context.Context, load func() (*Room, error)) (*Room, error) {
	room, err := load()
	if err != nil {
		return nil, err
	}
	if room.Phase == RoomPhaseEnded {
		return nil, ErrRoomClosed
	}
	return room, nil
}

// persistRoomChangeLocked persists a caller-applied mutation of live.room.
// It bumps the room generation, releases s.mu for the database round-trip,
// then re-acquires it and reconciles live.room with the persisted row. It
// must be called with s.mu held and always returns with s.mu held.
//
// When it returns conflict=true the caller lost an optimistic-concurrency
// race: live.room has been refreshed from the database and the caller should
// rebuild its snapshot from it and skip dispatching transport commands.
func (s *Service) persistRoomChangeLocked(
	ctx context.Context,
	live *liveRoom,
	persist func(room Room, expectedGeneration int64) (*Room, error),
) (conflict bool, err error) {
	expected := live.room.Generation
	live.room.Generation++
	roomCopy := live.room

	s.mu.Unlock()
	persisted, persistErr := persist(roomCopy, expected)
	var refreshed *Room
	if errors.Is(persistErr, ErrRoomStateConflict) {
		refreshed, _ = s.repo.GetRoomByID(ctx, roomCopy.ID)
	}
	s.mu.Lock()

	if persistErr != nil {
		// This writer's optimistic increment never landed; undo it so a
		// failed write cannot leave a phantom generation that makes every
		// later CAS conflict. Concurrent writers' stacked increments are
		// preserved because each writer undoes exactly its own.
		live.room.Generation--
		if errors.Is(persistErr, ErrRoomStateConflict) {
			// Adopt the database row only if it is at least as new as the
			// local copy — a concurrent writer may have advanced live.room
			// while the lock was released, and a stale refresh must not
			// overwrite that newer state.
			if refreshed != nil && refreshed.Generation >= live.room.Generation {
				live.room = *refreshed
			}
			return true, nil
		}
		return false, persistErr
	}
	// A concurrent writer may have advanced the local copy while the lock was
	// released; never regress it to an older persisted generation.
	if persisted != nil && persisted.Generation >= live.room.Generation {
		live.room = *persisted
	}
	return false, nil
}

// persistAnchorLocked persists the room's anchor/playback-state fields via
// persistRoomChangeLocked. Must be called with s.mu held; returns with it held.
func (s *Service) persistAnchorLocked(ctx context.Context, live *liveRoom) (bool, error) {
	return s.persistRoomChangeLocked(ctx, live, func(room Room, expectedGeneration int64) (*Room, error) {
		return s.repo.UpdateAnchor(
			ctx,
			room.ID,
			room.AnchorPositionSeconds,
			room.IsPaused,
			room.PlaybackState,
			room.ResumeOnReady,
			room.AnchorUpdatedAt,
			room.Generation,
			expectedGeneration,
		)
	})
}

func (s *Service) armWaitingDeadlineLocked(live *liveRoom) {
	if live.waitingTimer != nil {
		live.waitingTimer.Stop()
	}
	live.waitingEpoch++
	epoch := live.waitingEpoch
	roomID := live.room.ID
	live.waitingTimer = time.AfterFunc(waitingResumeDeadline, func() {
		s.handleWaitingDeadline(roomID, epoch)
	})
}

func (s *Service) disarmWaitingDeadlineLocked(live *liveRoom) {
	if live.waitingTimer != nil {
		live.waitingTimer.Stop()
		live.waitingTimer = nil
	}
	live.waitingEpoch++
}

// handleWaitingDeadline fires when a waiting period outlives
// waitingResumeDeadline: members that never became ready stop blocking the
// readiness barrier (ignoreWait) and playback resumes for everyone else.
func (s *Service) handleWaitingDeadline(roomID string, epoch int64) {
	s.mu.Lock()
	live := s.rooms[roomID]
	if live == nil || live.waitingEpoch != epoch || live.room.PlaybackState != RoomPlaybackStateWaiting {
		s.mu.Unlock()
		return
	}
	for _, member := range live.members {
		if member == nil || member.connection == nil || member.sessionID == "" {
			continue
		}
		if !member.isReady {
			member.ignoreWait = true
		}
	}
	dispatches, commandDispatches := s.maybeResumeFromWaitingLocked(context.Background(), live, true)
	s.mu.Unlock()
	s.runDispatches(dispatches)
	s.runCommandDispatches(commandDispatches)
}

// maybeResumeFromWaitingLocked leaves the waiting state once every remaining
// participant is ready (or unconditionally when force is set), persists the
// transition, and prepares the resulting snapshot and transport dispatches.
// It returns (nil, nil) when the room is not ready to resume. Must be called
// with s.mu held; the lock is temporarily released for persistence.
func (s *Service) maybeResumeFromWaitingLocked(
	ctx context.Context,
	live *liveRoom,
	force bool,
) ([]snapshotDispatch, []commandDispatch) {
	if live.room.Phase != RoomPhasePlaying || live.room.PlaybackState != RoomPlaybackStateWaiting {
		return nil, nil
	}
	if !force && !s.allParticipantsReadyLocked(live) {
		return nil, nil
	}

	saved := live.room
	live.room.AnchorPositionSeconds = math.Max(0, live.room.AnchorPositionSeconds)
	live.room.AnchorUpdatedAt = s.now()
	action := TransportActionPause
	if live.room.ResumeOnReady {
		live.room.IsPaused = false
		live.room.PlaybackState = RoomPlaybackStatePlaying
		action = TransportActionPlay
	} else {
		live.room.IsPaused = true
		live.room.PlaybackState = RoomPlaybackStatePaused
	}

	conflict, err := s.persistAnchorLocked(ctx, live)
	if err != nil {
		// The transition never landed in the database. Restore the waiting
		// state so snapshots keep matching persisted reality, and re-arm the
		// deadline so the resume is retried instead of silently dropped.
		live.room.AnchorPositionSeconds = saved.AnchorPositionSeconds
		live.room.AnchorUpdatedAt = saved.AnchorUpdatedAt
		live.room.IsPaused = saved.IsPaused
		live.room.PlaybackState = saved.PlaybackState
		s.armWaitingDeadlineLocked(live)
		return s.prepareSnapshotDispatchesLocked(live), nil
	}
	if conflict {
		// live.room now reflects the database row that won the race; if it is
		// still waiting the armed deadline keeps covering it.
		return s.prepareSnapshotDispatchesLocked(live), nil
	}
	s.disarmWaitingDeadlineLocked(live)
	commandDispatches := s.transportCommandDispatchesLocked(
		live,
		action,
		live.room.AnchorPositionSeconds,
		s.now().Add(s.highestPingLocked(live)),
	)
	return s.prepareSnapshotDispatchesLocked(live), commandDispatches
}

func (s *Service) runJanitor() {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.janitorStop:
			return
		case <-ticker.C:
			s.sweepIdleRooms()
		}
	}
}

// sweepIdleRooms evicts live rooms with no connected members from memory and
// closes rooms whose playback anchor has been idle for longer than
// roomIdleTTL, so abandoned rooms do not accumulate forever.
func (s *Service) sweepIdleRooms() {
	s.mu.Lock()
	for roomID, live := range s.rooms {
		if live == nil || s.connectedMemberCountLocked(live) == 0 {
			// Room state is fully persisted; it reloads on next access. Any
			// pending host-close timer keeps working from the database.
			if live != nil && live.waitingTimer != nil {
				live.waitingTimer.Stop()
			}
			delete(s.rooms, roomID)
		}
	}
	s.mu.Unlock()

	if s.repo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cutoff := s.now().Add(-roomIdleTTL)
	roomIDs, err := s.repo.ListIdleRoomIDs(ctx, cutoff, 100)
	if err != nil {
		return
	}
	closedAt := s.now()
	for _, roomID := range roomIDs {
		s.mu.Lock()
		live := s.rooms[roomID]
		hasMembers := live != nil && s.connectedMemberCountLocked(live) > 0
		s.mu.Unlock()
		if hasMembers {
			continue
		}
		_, _ = s.repo.CloseRoom(ctx, roomID, closedAt)
	}
}

func (s *Service) getOrLoadLiveRoom(ctx context.Context, roomID string) (*Room, *liveRoom, error) {
	s.mu.Lock()
	if live := s.rooms[roomID]; live != nil {
		roomCopy := live.room
		s.mu.Unlock()
		if roomCopy.Phase == RoomPhaseEnded {
			return nil, nil, ErrRoomClosed
		}
		return &roomCopy, live, nil
	}
	s.mu.Unlock()

	room, err := s.GetRoom(ctx, roomID)
	if err != nil {
		return nil, nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if live := s.rooms[roomID]; live != nil {
		roomCopy := live.room
		return &roomCopy, live, nil
	}

	live := &liveRoom{
		room:    *room,
		members: make(map[string]*memberState),
	}
	s.rooms[roomID] = live
	return room, live, nil
}

func (s *Service) ensureTransportAllowedLocked(
	live *liveRoom,
	userID int,
	profileID string,
	action TransportAction,
) error {
	if live.room.Phase != RoomPhasePlaying {
		return ErrTransportNotAllowed
	}
	isHost := live.room.HostUserID == userID && live.room.HostProfileID == profileID
	if isHost {
		return nil
	}
	if live.room.GuestControlPolicy == GuestControlPolicyGuestPlayPause &&
		(action == TransportActionPlay || action == TransportActionPause) {
		return nil
	}
	return ErrTransportNotAllowed
}

func (s *Service) validateSessionContent(ctx context.Context, room *Room, session *playback.Session) error {
	if room == nil || session == nil || s.files == nil {
		return ErrSessionMismatch
	}
	if room.Phase != RoomPhasePlaying || room.SelectedContentID == nil || *room.SelectedContentID == "" {
		return ErrSessionMismatch
	}
	file, err := s.files.GetByID(ctx, session.MediaFileID)
	if err != nil {
		return err
	}
	if file == nil {
		return ErrSessionMismatch
	}
	if file.ContentID != *room.SelectedContentID && file.EpisodeID != *room.SelectedContentID {
		return ErrSessionMismatch
	}
	if room.SelectedFileID != nil && session.MediaFileID != *room.SelectedFileID {
		return ErrSessionMismatch
	}
	return nil
}

func (s *Service) buildSnapshotLocked(live *liveRoom, userID int, profileID string) Snapshot {
	member := live.members[buildMemberKey(userID, profileID)]
	isHost := live.room.HostUserID == userID && live.room.HostProfileID == profileID
	canControl := isHost || (live.room.Phase == RoomPhasePlaying && live.room.GuestControlPolicy == GuestControlPolicyGuestPlayPause)
	invitePath := ""
	if isHost {
		invitePath = fmt.Sprintf("/rooms/join?token=%s", live.room.JoinToken)
	}

	members := make([]MemberSummary, 0, len(live.members))
	for _, m := range live.members {
		if m == nil || m.connection == nil {
			continue
		}
		members = append(members, MemberSummary{
			UserID:      m.userID,
			ProfileID:   m.profileID,
			DisplayName: m.displayName,
			IsHost:      m.userID == live.room.HostUserID && m.profileID == live.room.HostProfileID,
			IsSelf:      m.userID == userID && m.profileID == profileID,
			Connected:   true,
		})
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].IsHost != members[j].IsHost {
			return members[i].IsHost
		}
		if members[i].DisplayName != members[j].DisplayName {
			return members[i].DisplayName < members[j].DisplayName
		}
		return members[i].ProfileID < members[j].ProfileID
	})

	return Snapshot{
		RoomID:                  live.room.ID,
		Phase:                   live.room.Phase,
		PlaybackState:           live.room.PlaybackState,
		SelectionMode:           live.room.SelectionMode,
		SelectionRevision:       live.room.SelectionRevision,
		SelectedContentID:       live.room.SelectedContentID,
		SelectedFileID:          live.room.SelectedFileID,
		SelectedLibraryID:       live.room.SelectedLibraryID,
		Code:                    live.room.Code,
		GuestControlPolicy:      live.room.GuestControlPolicy,
		IsPaused:                live.room.IsPaused,
		AnchorPositionSeconds:   s.expectedPositionLocked(live),
		AnchorUpdatedAt:         live.room.AnchorUpdatedAt.UTC().Format(time.RFC3339),
		Generation:              live.room.Generation,
		MemberCount:             s.connectedMemberCountLocked(live),
		HostConnected:           s.hostConnectedLocked(live),
		SelfRole:                roleFor(live.room, userID, profileID),
		SelfCanControlTransport: canControl,
		SelfCanManageRoom:       isHost,
		SelfIgnoreWait: func() bool {
			if member == nil {
				return false
			}
			return member.ignoreWait
		}(),
		AttachedSessionID: func() string {
			if member == nil {
				return ""
			}
			return member.sessionID
		}(),
		InvitePath: invitePath,
		Members:    members,
	}
}

func (s *Service) prepareSnapshotDispatchesLocked(live *liveRoom) []snapshotDispatch {
	dispatches := make([]snapshotDispatch, 0, len(live.members))
	for _, member := range live.members {
		if member == nil || member.connection == nil {
			continue
		}
		dispatches = append(dispatches, snapshotDispatch{
			conn: member.connection,
			payload: map[string]any{
				"type": "snapshot",
				"room": s.buildSnapshotLocked(live, member.userID, member.profileID),
			},
		})
	}
	return dispatches
}

func (s *Service) prepareRoomClosedDispatchesLocked(live *liveRoom) []snapshotDispatch {
	dispatches := make([]snapshotDispatch, 0, len(live.members))
	for _, member := range live.members {
		if member == nil || member.connection == nil {
			continue
		}
		dispatches = append(dispatches, snapshotDispatch{
			conn: member.connection,
			payload: map[string]any{
				"type":   "room_closed",
				"reason": "host_left",
			},
		})
	}
	return dispatches
}

func (s *Service) runDispatches(dispatches []snapshotDispatch) {
	for _, dispatch := range dispatches {
		if dispatch.conn == nil {
			continue
		}
		_ = dispatch.conn.WriteJSON(dispatch.payload)
	}
}

func (s *Service) connectedMemberCountLocked(live *liveRoom) int {
	count := 0
	for _, member := range live.members {
		if member != nil && member.connection != nil {
			count++
		}
	}
	return count
}

func (s *Service) hostConnectedLocked(live *liveRoom) bool {
	member := live.members[buildMemberKey(live.room.HostUserID, live.room.HostProfileID)]
	return member != nil && member.connection != nil
}

func (s *Service) expectedPositionLocked(live *liveRoom) float64 {
	return expectedPosition(live.room, s.now())
}

func (s *Service) resetMemberReadinessLocked(live *liveRoom, markBuffering bool) {
	for _, member := range live.members {
		if member == nil || member.connection == nil || member.sessionID == "" {
			continue
		}
		member.isReady = false
		if markBuffering {
			member.isBuffering = true
		}
	}
}

func (s *Service) activeParticipantCountLocked(live *liveRoom) int {
	count := 0
	for _, member := range live.members {
		if member == nil || member.connection == nil || member.sessionID == "" || member.ignoreWait {
			continue
		}
		count++
	}
	return count
}

func (s *Service) allParticipantsReadyLocked(live *liveRoom) bool {
	participants := 0
	for _, member := range live.members {
		if member == nil || member.connection == nil || member.sessionID == "" || member.ignoreWait {
			continue
		}
		participants++
		if !member.isReady {
			return false
		}
	}
	return participants > 0
}

// highestPingLocked returns the scheduling lead for transport commands: the
// worst measured round-trip time across participants, bounded to
// [minTransportLead, maxTransportLead] so one member's bogus latency cannot
// stall the room.
func (s *Service) highestPingLocked(live *liveRoom) time.Duration {
	highest := defaultTransportLead
	for _, member := range live.members {
		if member == nil || member.connection == nil || member.sessionID == "" {
			continue
		}
		if member.lastPingMS <= 0 {
			continue
		}
		delay := time.Duration(member.lastPingMS) * time.Millisecond
		if delay > highest {
			highest = delay
		}
	}
	if highest < minTransportLead {
		return minTransportLead
	}
	if highest > maxTransportLead {
		return maxTransportLead
	}
	return highest
}

func (s *Service) targetedCommandDispatchesLocked(
	live *liveRoom,
	sessionID string,
	command TransportCommand,
) []commandDispatch {
	dispatches := make([]commandDispatch, 0, 1)
	for memberKey, member := range live.members {
		if member == nil || member.connection == nil || member.sessionID == "" || member.sessionID != sessionID {
			continue
		}
		payload := command
		payload.SessionID = member.sessionID
		dispatches = append(dispatches, commandDispatch{
			conn:      member.connection,
			memberKey: memberKey,
			payload: map[string]any{
				"type":    "transport_command",
				"command": payload,
			},
		})
	}
	return dispatches
}

func (s *Service) transportCommandDispatchesLocked(
	live *liveRoom,
	action TransportAction,
	positionSeconds float64,
	executeAt time.Time,
) []commandDispatch {
	dispatches := make([]commandDispatch, 0, len(live.members))
	for memberKey, member := range live.members {
		if member == nil || member.connection == nil || member.sessionID == "" {
			continue
		}
		command := TransportCommand{
			CommandID:         uuid.NewString(),
			SessionID:         member.sessionID,
			SelectionRevision: live.room.SelectionRevision,
			Action:            action,
			PositionSeconds:   math.Max(0, positionSeconds),
			ExecuteAt:         executeAt.UTC().Format(time.RFC3339Nano),
			IssuedAt:          s.now().UTC().Format(time.RFC3339Nano),
			PlaybackState:     live.room.PlaybackState,
		}
		dispatches = append(dispatches, commandDispatch{
			conn:      member.connection,
			memberKey: memberKey,
			payload: map[string]any{
				"type":    "transport_command",
				"command": command,
			},
		})
	}
	return dispatches
}

func (s *Service) runCommandDispatches(dispatches []commandDispatch) {
	for _, dispatch := range dispatches {
		if dispatch.conn == nil {
			continue
		}
		_ = dispatch.conn.WriteJSON(dispatch.payload)
	}
}

func (s *Service) enterWaitingLocked(live *liveRoom, positionSeconds float64, resumeOnReady bool) ([]commandDispatch, bool) {
	if live.room.Phase != RoomPhasePlaying {
		return nil, false
	}
	live.room.AnchorPositionSeconds = math.Max(0, positionSeconds)
	live.room.IsPaused = true
	live.room.PlaybackState = RoomPlaybackStateWaiting
	live.room.ResumeOnReady = resumeOnReady
	live.room.AnchorUpdatedAt = s.now()
	s.resetMemberReadinessLocked(live, false)

	if s.activeParticipantCountLocked(live) == 0 {
		return nil, false
	}
	s.armWaitingDeadlineLocked(live)
	executeAt := s.now().Add(s.highestPingLocked(live))
	return s.transportCommandDispatchesLocked(
		live,
		TransportActionPause,
		live.room.AnchorPositionSeconds,
		executeAt,
	), true
}

func (s *Service) syncMemberToRoomLocked(live *liveRoom, sessionID string) []commandDispatch {
	if sessionID == "" {
		return nil
	}
	position := expectedPosition(live.room, s.now())
	action := TransportActionPause
	if live.room.PlaybackState == RoomPlaybackStatePlaying {
		action = TransportActionPlay
	}
	return s.targetedCommandDispatchesLocked(live, sessionID, TransportCommand{
		CommandID:         uuid.NewString(),
		SelectionRevision: live.room.SelectionRevision,
		Action:            action,
		PositionSeconds:   math.Max(0, position),
		ExecuteAt:         s.now().Add(s.highestPingLocked(live)).UTC().Format(time.RFC3339Nano),
		IssuedAt:          s.now().UTC().Format(time.RFC3339Nano),
		PlaybackState:     live.room.PlaybackState,
	})
}

func expectedPosition(room Room, now time.Time) float64 {
	position := math.Max(0, room.AnchorPositionSeconds)
	if room.IsPaused {
		return position
	}
	elapsed := now.UTC().Sub(room.AnchorUpdatedAt.UTC()).Seconds()
	if elapsed <= 0 {
		return position
	}
	return position + elapsed
}

func buildMemberKey(userID int, profileID string) string {
	return fmt.Sprintf("%d:%s", userID, profileID)
}

func roleFor(room Room, userID int, profileID string) MemberRole {
	if room.HostUserID == userID && room.HostProfileID == profileID {
		return MemberRoleHost
	}
	return MemberRoleGuest
}

// --- Suggestion and Voting methods ---

func (s *Service) CreateSuggestion(
	ctx context.Context,
	roomID string,
	userID int,
	profileID string,
	input CreateSuggestionInput,
) ([]Suggestion, error) {
	if s == nil || s.suggestions == nil {
		return nil, fmt.Errorf("watch together suggestions unavailable")
	}
	if input.ContentType != "movie" && input.ContentType != "episode" {
		return nil, ErrInvalidSelection
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if live.room.Phase == RoomPhaseEnded {
		s.mu.Unlock()
		return nil, ErrRoomClosed
	}
	s.mu.Unlock()

	suggestion := Suggestion{
		ID:                 uuid.NewString(),
		RoomID:             roomID,
		SuggesterUserID:    userID,
		SuggesterProfileID: profileID,
		ContentID:          input.ContentID,
		ContentType:        input.ContentType,
		Title:              input.Title,
		Subtitle:           input.Subtitle,
		PosterURL:          input.PosterURL,
		Note:               input.Note,
		VoteCount:          0,
		CreatedAt:          s.now(),
	}

	if _, err := s.suggestions.CreateSuggestion(ctx, suggestion); err != nil {
		return nil, err
	}

	suggestions, err := s.suggestions.ListSuggestions(ctx, roomID, profileID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	live = s.rooms[roomID]
	if live != nil {
		dispatches := s.prepareSuggestionDispatchesLocked(live, suggestions)
		s.mu.Unlock()
		s.runDispatches(dispatches)
	} else {
		s.mu.Unlock()
	}

	return suggestions, nil
}

func (s *Service) ListSuggestions(
	ctx context.Context,
	roomID string,
	profileID string,
) ([]Suggestion, error) {
	if s == nil || s.suggestions == nil {
		return nil, fmt.Errorf("watch together suggestions unavailable")
	}
	return s.suggestions.ListSuggestions(ctx, roomID, profileID)
}

func (s *Service) DeleteSuggestion(
	ctx context.Context,
	roomID string,
	suggestionID string,
	userID int,
	profileID string,
) ([]Suggestion, error) {
	if s == nil || s.suggestions == nil {
		return nil, fmt.Errorf("watch together suggestions unavailable")
	}

	existing, err := s.suggestions.GetSuggestion(ctx, suggestionID)
	if err != nil {
		return nil, err
	}
	if existing.RoomID != roomID {
		return nil, ErrSuggestionNotFound
	}

	// Host can delete any; suggester can delete own
	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	isHost := live.room.HostUserID == userID && live.room.HostProfileID == profileID
	s.mu.Unlock()

	isSuggester := existing.SuggesterUserID == userID && existing.SuggesterProfileID == profileID
	if !isHost && !isSuggester {
		return nil, ErrRoomForbidden
	}

	if err := s.suggestions.DeleteSuggestion(ctx, suggestionID); err != nil {
		return nil, err
	}

	suggestions, err := s.suggestions.ListSuggestions(ctx, roomID, profileID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	live = s.rooms[roomID]
	if live != nil {
		dispatches := s.prepareSuggestionDispatchesLocked(live, suggestions)
		s.mu.Unlock()
		s.runDispatches(dispatches)
	} else {
		s.mu.Unlock()
	}

	return suggestions, nil
}

func (s *Service) Vote(
	ctx context.Context,
	roomID string,
	suggestionID string,
	userID int,
	profileID string,
) ([]Suggestion, error) {
	if s == nil || s.suggestions == nil {
		return nil, fmt.Errorf("watch together suggestions unavailable")
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}

	// Verify suggestion belongs to this room
	existing, err := s.suggestions.GetSuggestion(ctx, suggestionID)
	if err != nil {
		return nil, err
	}
	if existing.RoomID != roomID {
		return nil, ErrSuggestionNotFound
	}

	if err := s.suggestions.AddVote(ctx, suggestionID, profileID); err != nil {
		return nil, err
	}

	suggestions, err := s.suggestions.ListSuggestions(ctx, roomID, profileID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	live = s.rooms[roomID]
	if live != nil {
		dispatches := s.prepareSuggestionDispatchesLocked(live, suggestions)
		s.mu.Unlock()
		s.runDispatches(dispatches)
	} else {
		s.mu.Unlock()
	}

	return suggestions, nil
}

func (s *Service) Unvote(
	ctx context.Context,
	roomID string,
	suggestionID string,
	userID int,
	profileID string,
) ([]Suggestion, error) {
	if s == nil || s.suggestions == nil {
		return nil, fmt.Errorf("watch together suggestions unavailable")
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}

	// Verify suggestion belongs to this room
	existing, err := s.suggestions.GetSuggestion(ctx, suggestionID)
	if err != nil {
		return nil, err
	}
	if existing.RoomID != roomID {
		return nil, ErrSuggestionNotFound
	}

	if err := s.suggestions.RemoveVote(ctx, suggestionID, profileID); err != nil {
		return nil, err
	}

	suggestions, err := s.suggestions.ListSuggestions(ctx, roomID, profileID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	live = s.rooms[roomID]
	if live != nil {
		dispatches := s.prepareSuggestionDispatchesLocked(live, suggestions)
		s.mu.Unlock()
		s.runDispatches(dispatches)
	} else {
		s.mu.Unlock()
	}

	return suggestions, nil
}

func (s *Service) PromoteSuggestion(
	ctx context.Context,
	roomID string,
	suggestionID string,
	userID int,
	profileID string,
) (Snapshot, error) {
	if s == nil || s.suggestions == nil {
		return Snapshot{}, fmt.Errorf("watch together suggestions unavailable")
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	if live.room.HostUserID != userID || live.room.HostProfileID != profileID {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomForbidden
	}
	s.mu.Unlock()

	suggestion, err := s.suggestions.GetSuggestion(ctx, suggestionID)
	if err != nil {
		return Snapshot{}, err
	}
	if suggestion.RoomID != roomID {
		return Snapshot{}, ErrSuggestionNotFound
	}

	return s.SelectItem(ctx, roomID, userID, profileID, SelectItemInput{
		ContentID: suggestion.ContentID,
	})
}

func (s *Service) prepareSuggestionDispatchesLocked(live *liveRoom, suggestions []Suggestion) []snapshotDispatch {
	// Strip voted_by_me from broadcast since it is relative to the requester.
	// Clients merge vote state from their local knowledge on receipt.
	broadcast := make([]Suggestion, len(suggestions))
	copy(broadcast, suggestions)
	for i := range broadcast {
		broadcast[i].VotedByMe = false
	}

	dispatches := make([]snapshotDispatch, 0, len(live.members))
	for _, member := range live.members {
		if member == nil || member.connection == nil {
			continue
		}
		dispatches = append(dispatches, snapshotDispatch{
			conn: member.connection,
			payload: map[string]any{
				"type":        "suggestions_update",
				"suggestions": broadcast,
			},
		})
	}
	return dispatches
}

const roomTokenAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func randomToken(length int) string {
	if length <= 0 {
		return ""
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return uuid.NewString()
	}
	for i := range buf {
		buf[i] = roomTokenAlphabet[int(buf[i])%len(roomTokenAlphabet)]
	}
	return string(buf)
}
