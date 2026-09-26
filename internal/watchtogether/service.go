package watchtogether

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
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
	// ErrNotVoteWinner preserves the v1 winner-only promotion contract.
	ErrNotVoteWinner = errors.New("watch together suggestion is not the vote winner")
	// ErrNoVotesCast is returned by VoteWinner when nobody has voted yet, so
	// there is no leader to report.
	ErrNoVotesCast = errors.New("watch together room has no votes yet")
	// ErrVoteRoomSelection is returned when a vote room's selection is set
	// directly instead of through the vote.
	ErrVoteRoomSelection = errors.New("watch together vote room selects by vote")
	ErrDuplicateVote     = errors.New("watch together already voted")
	ErrNotVoted          = errors.New("watch together not voted")
	ErrInvalidPosition   = errors.New("watch together position is invalid")
	// ErrRoomNotInLobby is returned when a lobby-only action (staging,
	// switching the selection mode, marking ready) arrives after playback
	// has started.
	ErrRoomNotInLobby = errors.New("watch together room is not in the lobby")
	// ErrNoStagedSelection is returned when the host starts playback with
	// nothing staged.
	ErrNoStagedSelection = errors.New("watch together room has nothing staged")
)

const (
	roomPayloadKey       = "room"
	snapshotMessageType  = "snapshot"
	transportMessageType = "transport_command"
	commandPayloadKey    = "command"
	defaultTransportLead = 500 * time.Millisecond
	minTransportLead     = 350 * time.Millisecond
	// maxTransportLead bounds how far in the future transport commands may be
	// scheduled, so a single member with a huge (or bogus) measured latency
	// cannot stall the whole room.
	maxTransportLead = 5 * time.Second
	// guestCorrectionRetryInterval prevents repeated state reports from
	// rebuilding a guest's stream while the previous correction is still being
	// applied. A later report retries the correction if the guest remains out of
	// sync after this bounded interval.
	guestCorrectionRetryInterval = 5 * time.Second
	// maxBufferingAnchorDriftSeconds bounds how far a buffering member's
	// reported position may move the shared room anchor.
	maxBufferingAnchorDriftSeconds = 5.0
	// readySeekToleranceSeconds allows media clock rounding after a seek,
	// while rejecting readiness from the stream that is being replaced.
	readySeekToleranceSeconds = 1.0
	// hostReadySeekToleranceSeconds is the seek tolerance for the host. A
	// rebuilt stream lands on a keyframe or segment boundary short of the
	// requested position; the host's real position becomes the room anchor
	// rather than holding everyone until the exact target is reached.
	hostReadySeekToleranceSeconds = 15.0
	// maxPositionSeconds rejects corrupt client reports before they can poison
	// the shared anchor or produce unusable transport commands.
	maxPositionSeconds = 7 * 24 * 60 * 60
	// waitingResumeDeadline is how long a room stays in the waiting state
	// before stragglers are skipped and playback resumes for everyone ready.
	// It is a safety net past any legitimate seek plus stream reload, not the
	// expected path: readiness is reported on every media event and state tick.
	waitingResumeDeadline = 10 * time.Second
	// memberStallCooldown is how long a member must play without stalling
	// before a stall of theirs may pause the room again. The room waits for a
	// viewer once; a viewer who keeps stalling catches up on their own instead
	// of pausing everyone each time.
	memberStallCooldown = 5 * time.Minute
	// bufferingWaitSpacing bounds how often buffering may pause the room,
	// whoever stalls, so several slow viewers cannot take turns pausing it.
	bufferingWaitSpacing = time.Minute
	// bufferingCorrectionHold is how long a buffering member is left without
	// position corrections. Its stalled stream cannot apply a target, and on
	// copy remux every correction would rebuild the stream. Recovery is
	// acknowledged with ready; the hold bounds a client that never does.
	bufferingCorrectionHold = 30 * time.Second
	// hostAuthorityDriftSeconds is how far the host's passive position may
	// drift before it moves the room anchor. Smaller drift, such as a short
	// local stall, is corrected on the host like any viewer's instead of
	// pulling every other viewer back.
	hostAuthorityDriftSeconds = 2.0
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

// A socket adapter can deliver a terminal replacement reason before closing.
// Older adapters retain their existing close behavior.
func closeReplacedConnection(conn RoomConnection) {
	if replacement, ok := conn.(interface{ CloseReplaced() error }); ok {
		_ = replacement.CloseReplaced()
		return
	}
	_ = conn.Close()
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
	// UpdateSelectionMode switches a lobby between host_pick and vote and
	// drops whatever was staged: the staged item belonged to the old way of
	// deciding.
	UpdateSelectionMode(
		ctx context.Context,
		roomID string,
		mode RoomSelectionMode,
		anchorUpdatedAt time.Time,
		generation int64,
		expectedGeneration int64,
	) (*Room, error)
	// UpdateStagedSelection replaces the lobby's staged item without leaving
	// the lobby. It never touches phase, playback state or the selection
	// revision; only a start does.
	UpdateStagedSelection(
		ctx context.Context,
		roomID string,
		selection SelectItemInput,
		anchorUpdatedAt time.Time,
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
	roomID       string
	memberKey    string
	connection   RoomConnection
	connectionID string
}

type pendingDisconnect struct {
	registration *Registration
	explicit     bool
}

type memberState struct {
	connectionID    string
	remoteConnected bool
	leaseUntil      time.Time
	disconnectedAt  time.Time
	lastCommandID   string
	userID          int
	profileID       string
	displayName     string
	sessionID       string
	connection      RoomConnection
	isReady         bool
	isBuffering     bool
	// correctionCommand is the most recent normal-playing correction sent to
	// this member. It is persisted with the room runtime so another API server
	// does not immediately resend the same correction.
	correctionCommand *TransportCommand
	// waitingCommand is the command this member must finish before resuming.
	waitingCommand *TransportCommand
	// ignoreWait marks a member who is catching up on their own: they are
	// excluded from room-wide readiness barriers and their stalls do not pause
	// the room. It is set when the member misses waitingResumeDeadline or
	// stalls while the room does not wait for them. Reporting ready or
	// attaching again clears it.
	ignoreWait bool
	// lastStallAt is when the member last stalled while the room played,
	// missed a waiting deadline, or started a stream in a playing room. It
	// survives reconnects and recovery so a viewer who keeps stalling cannot
	// pause the room every time.
	lastStallAt time.Time
	// syncingToRoom marks a freshly attached session that has been told the
	// room's position but has not confirmed it yet. Until then the member's
	// state reports describe the stream's starting point, not where the room
	// is, so they are treated as a guest's: corrected, never authoritative.
	// Cleared by a matching state report or the member's transport request.
	syncingToRoom bool
	// lobbyReady is the member's "I'm ready" in the lobby. It is unrelated to
	// isReady, which is the buffering barrier bound to an attached playback
	// session, and it is cleared whenever what the room is about to play
	// changes: staging a different item, starting, or switching modes.
	lobbyReady bool
	lastPingMS int64
}

// resetForSelection drops everything bound to the previous playback epoch.
// A new selection starts every member afresh, including their stall history.
func (m *memberState) resetForSelection() {
	m.sessionID = ""
	m.isReady = false
	m.isBuffering = false
	m.ignoreWait = false
	m.lastStallAt = time.Time{}
	m.waitingCommand = nil
	m.correctionCommand = nil
	m.lastCommandID = ""
	m.syncingToRoom = false
	m.lobbyReady = false
}

type liveRoom struct {
	operationMu        sync.Mutex
	activeOperations   int
	reconciling        bool
	reconcilePending   bool
	pendingDisconnects map[string]pendingDisconnect
	broadcastState     string
	command            *TransportCommand
	room               Room
	members            map[string]*memberState
	hostCloseTimer     *time.Timer
	waitingTimer       *time.Timer
	// waitingEpoch identifies the current waiting period; deadline callbacks
	// carry the epoch they were armed for so a stale timer cannot act on a
	// newer waiting period.
	waitingEpoch int64
	// bufferingWaitAt is when buffering last paused the room.
	bufferingWaitAt time.Time
	// itemEnd caches the playing file's duration for the reconciler.
	itemEnd itemEnd
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
	repo        RoomStore
	suggestions SuggestionStore
	sessions    RoomSessionLookup
	attempts    interface {
		GetAttempt(context.Context, string) (*playback.AttemptRecordV3, error)
	}
	files             MediaFileLookup
	selectionResolver WatchTogetherSelectionResolver
	profileNames      ProfileNameResolver
	hostDisconnectTTL time.Duration
	now               func() time.Time

	janitorStop chan struct{}

	mu            sync.Mutex
	rooms         map[string]*liveRoom
	clusterBus    cache.EventBus
	clusterCancel context.CancelFunc
	instanceID    string
	clusterMu     sync.Mutex
}

// defaultHostDisconnectTTL is how long a room survives its host's socket going
// away without an explicit leave.
//
// This is not "how long before we assume the host left" — an explicit leave and
// an explicit close both tear the room down immediately, so this timer only
// ever covers a host who has NOT said they are going. At 15s it treated any
// transient drop as a departure: a host who backgrounded the app, walked
// through a tunnel, or simply navigated somewhere the client did not hold the
// socket open lost the room for everybody, mid-conversation, with a
// hostLeftReason nobody could explain.
//
// Two minutes is long enough to survive a reconnect, an app switch, or a
// client that drops the socket while its user browses for something to
// suggest; short enough that a genuinely departed host does not leave a room
// sitting open all evening. The janitor still reaps idle rooms independently.
const defaultHostDisconnectTTL = 2 * time.Minute

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
		hostDisconnectTTL: defaultHostDisconnectTTL,
		now: func() time.Time {
			return time.Now().UTC()
		},
		janitorStop: make(chan struct{}),
		rooms:       make(map[string]*liveRoom),
		instanceID:  uuid.NewString(),
	}
	go s.runJanitor()
	if _, shared := repo.(*Repository); shared {
		go s.runReconciler()
		go s.runHostExpirySweeper()
	}
	return s
}

// Close stops the service's background maintenance loop.
func (s *Service) Close() {
	if s == nil || s.janitorStop == nil {
		return
	}
	s.mu.Lock()
	for _, live := range s.rooms {
		s.disarmWaitingDeadlineLocked(live)
		if live.hostCloseTimer != nil {
			live.hostCloseTimer.Stop()
		}
	}
	s.mu.Unlock()
	s.clusterMu.Lock()
	if s.clusterCancel != nil {
		s.clusterCancel()
	}
	s.clusterMu.Unlock()
	select {
	case <-s.janitorStop:
	default:
		close(s.janitorStop)
	}
}

// SetClusterEventBus wires cross-node room state propagation. It is optional
// so in-process users and tests can keep the lightweight constructor.
func (s *Service) SetClusterEventBus(bus cache.EventBus) error {
	if s == nil || bus == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := bus.Subscribe(ctx, cache.ChannelPlayback, func(event cache.Event) { s.handleClusterEvent(event) }); err != nil {
		cancel()
		return err
	}
	s.clusterMu.Lock()
	oldCancel := s.clusterCancel
	s.clusterBus, s.clusterCancel = bus, cancel
	s.clusterMu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}
	return nil
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

func (s *Service) snapshot(ctx context.Context, roomID string, userID int, profileID string) (Snapshot, error) {
	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buildSnapshotLocked(live, userID, profileID), nil
}

func (s *Service) connect(
	ctx context.Context,
	roomID string,
	userID int,
	profileID string,
	conn RoomConnection,
	displayName string,
) (*Registration, Snapshot, error) {
	room, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return nil, Snapshot{}, err
	}

	memberKey := buildMemberKey(userID, profileID)

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
	current.connectionID = uuid.NewString()
	current.remoteConnected = false
	current.leaseUntil = s.now().Add(connectionLease)
	current.disconnectedAt = time.Time{}
	current.lastCommandID = ""
	current.correctionCommand = nil
	if live.room.PlaybackState == RoomPlaybackStateWaiting {
		current.isReady = false
	}
	current.displayName = displayName

	if room.HostUserID == userID && room.HostProfileID == profileID && live.hostCloseTimer != nil {
		live.hostCloseTimer.Stop()
		live.hostCloseTimer = nil
	}

	connectionID := current.connectionID
	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()

	if previousConn != nil {
		afterRoomCommit(ctx, func() { closeReplacedConnection(previousConn) })
	}
	s.sendDispatches(ctx, dispatches)
	return &Registration{roomID: roomID, memberKey: memberKey, connection: conn, connectionID: connectionID}, snapshot, nil
}

func (s *Service) disconnect(ctx context.Context, reg *Registration, explicitLeave bool) {
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
	if member == nil || (reg.connectionID != "" && member.connectionID != reg.connectionID) ||
		(reg.connectionID == "" && member.connection != reg.connection) {
		s.mu.Unlock()
		return
	}

	isHost := member.userID == live.room.HostUserID && member.profileID == live.room.HostProfileID
	member.connection = nil
	member.remoteConnected = false
	member.disconnectedAt = s.now()
	if explicitLeave {
		delete(live.members, reg.memberKey)
	}

	if isHost {
		if explicitLeave {
			s.mu.Unlock()
			_ = s.closeRoom(ctx, reg.roomID, member.userID, member.profileID)
			return
		}
		if live.hostCloseTimer != nil {
			live.hostCloseTimer.Stop()
		}
		// Shared rooms expire from persisted presence. Local timer changes
		// cannot be rolled back with a failed reconnect or close transaction.
		if _, shared := s.repo.(*Repository); !shared {
			roomID := reg.roomID
			hostUserID := live.room.HostUserID
			hostProfileID := live.room.HostProfileID
			live.hostCloseTimer = time.AfterFunc(s.hostDisconnectTTL, func() {
				s.closeIfHostStillDisconnected(roomID, hostUserID, hostProfileID)
			})
		}
	}

	// A departing member may have been the last participant the room was
	// waiting on; re-evaluate readiness so the others aren't stuck.
	dispatches, commandDispatches := s.maybeResumeFromWaitingLocked(ctx, live, false)
	if dispatches == nil {
		dispatches = s.prepareSnapshotDispatchesLocked(live)
	}
	s.mu.Unlock()
	s.sendDispatches(ctx, dispatches)
	s.sendCommandDispatches(ctx, commandDispatches)
}

func (s *Service) attachSessionForConnection(
	ctx context.Context,
	reg *Registration,
	userID int,
	profileID string,
	sessionID string,
	session *playback.Session,
	file *models.MediaFile,
) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}

	room, live, err := s.getOrLoadLiveRoom(ctx, reg.roomID)
	if err != nil {
		return Snapshot{}, err
	}

	if err := validateSessionContent(room, session, file); err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	member := live.members[reg.memberKey]
	if member == nil || member.connection == nil || member.connection != reg.connection {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomForbidden
	}
	member.correctionCommand = nil
	reattaching := member.sessionID == sessionID
	member.ignoreWait = false
	if !reattaching {
		member.sessionID = sessionID
		member.isReady = false
		member.isBuffering = live.room.Phase == RoomPhasePlaying && live.room.PlaybackState == RoomPlaybackStateWaiting
		member.syncingToRoom = false
	}

	var commandDispatches []commandDispatch
	if live.room.Phase == RoomPhasePlaying {
		// A late joiner or replacement stream starts behind a room that keeps
		// playing. Its startup stall counts as its stall, so one viewer
		// starting a stream never pauses the others; it still takes part in
		// the room's explicit seeks.
		if !reattaching && live.room.PlaybackState == RoomPlaybackStatePlaying && s.othersWatchingLocked(live, member) {
			member.lastStallAt = s.now()
		}
		// The (re)joiner is told where the room is. Until they confirm, their
		// stream position is the file's start, not the room's; the host in
		// particular must not drag the anchor back there.
		commandDispatches = s.syncMemberToRoomLocked(live, sessionID)
		member.syncingToRoom = len(commandDispatches) > 0
	}

	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	roomID := live.room.ID
	s.mu.Unlock()

	for _, dispatch := range commandDispatches {
		slog.DebugContext(ctx, "watch together sync queued",
			append(memberCommandLogAttrs(roomID, userID, dispatch), "trigger", "attach", "reattaching", reattaching)...)
	}
	s.sendDispatches(ctx, dispatches)
	s.sendCommandDispatches(ctx, commandDispatches)
	return snapshot, nil
}

func (s *Service) handleTransportRequestForConnection(
	ctx context.Context,
	reg *Registration,
	userID int,
	profileID string,
	request TransportRequest,
) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}
	if request.PositionSeconds != nil && !validPosition(*request.PositionSeconds) {
		return Snapshot{}, ErrInvalidPosition
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
	// An explicit transport request is the member's intent, which supersedes
	// any pending sync to the room's position.
	member.syncingToRoom = false

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

	s.sendDispatches(ctx, dispatches)
	s.sendCommandDispatches(ctx, commandDispatches)
	return snapshot, nil
}

func (s *Service) handleStateReportForConnection(
	ctx context.Context,
	reg *Registration,
	userID int,
	profileID string,
	report StateReport,
) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}
	if !validPosition(report.PositionSeconds) {
		return Snapshot{}, ErrInvalidPosition
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

	// While a seek or buffering barrier is pending, element positions may
	// still describe the old stream. Keep the waiting anchor authoritative:
	// host reports must not undo the seek, and guest corrections must not
	// supersede the transport command that is still loading. A report that
	// carries is_ready is the periodic form of the ready message, so a lost
	// or rejected acknowledgement heals on the next tick.
	if live.room.PlaybackState == RoomPlaybackStateWaiting {
		if !report.IsReady || !s.acceptReadyLocked(ctx, live, member, userID, profileID, report) {
			snapshot := s.buildSnapshotLocked(live, userID, profileID)
			s.mu.Unlock()
			return snapshot, nil
		}
		return s.finishReadyLocked(ctx, live, member, userID, profileID)
	}

	isHost := userID == live.room.HostUserID && profileID == live.room.HostProfileID
	memberIsHost := isHost
	now := s.now()
	expected := expectedPosition(live.room, now)
	pauseMismatch := report.IsPaused != live.room.IsPaused
	drift := math.Abs(report.PositionSeconds - expected)
	// A member whose report matches the room is ready, whether it just joined,
	// is catching up, or is still marked buffering. Clients acknowledge
	// recovery with ready; this keeps one that never does, and a late joiner
	// that was never asked to, from staying unready. Stall history is kept.
	caughtUp := (!member.isReady || member.isBuffering || member.ignoreWait) && !pauseMismatch && drift <= readySeekToleranceSeconds
	if caughtUp {
		member.isBuffering = false
		member.ignoreWait = false
		member.isReady = true
	}
	// A host who has just (re)attached and not yet reached the room's
	// position is reporting the stream's start, not a decision. Anchoring
	// there would rewind everyone; correct the host like a guest instead. The
	// first report that matches the room ends the sync.
	if isHost && member.syncingToRoom {
		if !pauseMismatch && drift <= 1.5 {
			member.syncingToRoom = false
		} else {
			isHost = false
		}
	}
	// A host who is buffering or catching up reports a stalled stream, and a
	// host inside the catch-up band has only drifted. Neither is a decision
	// the room should follow.
	if isHost && (member.ignoreWait || member.isBuffering || (!pauseMismatch && drift <= hostAuthorityDriftSeconds)) {
		isHost = false
	}
	// A stalled stream cannot apply a target; recovery is acknowledged with
	// ready, which sends a fresh one.
	holdCorrection := member.isBuffering && now.Sub(member.lastStallAt) < bufferingCorrectionHold

	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	if isHost {
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
			s.clearCorrectionCommandsLocked(live)
			s.mu.Unlock()
			return snapshot, nil
		}
		s.clearCorrectionCommandsLocked(live)
		dispatches = s.prepareSnapshotDispatchesLocked(live)
	} else if holdCorrection {
		member.correctionCommand = nil
	} else if pauseMismatch || drift > 1.0 {
		command := TransportCommand{
			CommandID:         uuid.NewString(),
			SessionID:         report.SessionID,
			SelectionRevision: live.room.SelectionRevision,
			Action: func() TransportAction {
				if live.room.PlaybackState == RoomPlaybackStatePlaying {
					return TransportActionPlay
				}
				return TransportActionPause
			}(),
			PositionSeconds: math.Max(0, expected),
			ExecuteAt:       now.Add(s.highestPingLocked(live)).UTC().Format(time.RFC3339Nano),
			IssuedAt:        now.UTC().Format(time.RFC3339Nano),
			PlaybackState:   live.room.PlaybackState,
		}
		if !correctionCommandPending(member, command, now) {
			member.correctionCommand = &command
			correctionDispatches = s.targetedCommandDispatchesLocked(live, report.SessionID, command)
		}
	} else {
		member.correctionCommand = nil
	}
	if caughtUp && dispatches == nil {
		dispatches = s.prepareSnapshotDispatchesLocked(live)
	}
	roomID := live.room.ID
	s.mu.Unlock()
	s.sendDispatches(ctx, dispatches)
	if isHost {
		return snapshot, nil
	}

	for _, dispatch := range correctionDispatches {
		slog.DebugContext(ctx, "watch together correction queued",
			append(memberCommandLogAttrs(roomID, userID, dispatch),
				"host", memberIsHost, "reported_position_seconds", report.PositionSeconds,
				"drift_seconds", drift, "pause_mismatch", pauseMismatch)...)
	}
	if len(correctionDispatches) > 0 {
		s.sendCommandDispatches(ctx, correctionDispatches)
	}

	return snapshot, nil
}

// handleLobbyReadyForConnection records a member's lobby "I'm ready". It is
// advisory: the host may start regardless, and the flag is dropped the moment
// what the room is about to play changes. The flag rides in the shared room
// runtime like the rest of member state, so every API server sees it.
func (s *Service) handleLobbyReadyForConnection(
	ctx context.Context,
	reg *Registration,
	userID int,
	profileID string,
	ready bool,
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
	if member == nil || member.connection == nil || member.connection != reg.connection {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomForbidden
	}
	if live.room.Phase == RoomPhaseEnded {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomClosed
	}
	if live.room.Phase != RoomPhaseLobby {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomNotInLobby
	}
	if member.lobbyReady == ready {
		snapshot := s.buildSnapshotLocked(live, userID, profileID)
		s.mu.Unlock()
		return snapshot, nil
	}
	member.lobbyReady = ready
	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()

	s.sendDispatches(ctx, dispatches)
	return snapshot, nil
}

func (s *Service) handleReadyForConnection(
	ctx context.Context,
	reg *Registration,
	userID int,
	profileID string,
	report StateReport,
) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}
	if !validPosition(report.PositionSeconds) {
		return Snapshot{}, ErrInvalidPosition
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, reg.roomID)
	if err != nil {
		return Snapshot{}, err
	}

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

	if live.room.PlaybackState == RoomPlaybackStateWaiting &&
		!s.acceptReadyLocked(ctx, live, member, userID, profileID, report) {
		snapshot := s.buildSnapshotLocked(live, userID, profileID)
		s.mu.Unlock()
		return snapshot, nil
	}
	// With nobody else watching, the room ran on without an audience while
	// this viewer caught up. Resume from where the viewer is rather than
	// skipping what they have not seen.
	if live.room.PlaybackState != RoomPlaybackStateWaiting && member.ignoreWait && !s.othersWatchingLocked(live, member) {
		live.room.AnchorPositionSeconds = math.Max(0, report.PositionSeconds)
		live.room.AnchorUpdatedAt = s.now()
		if _, err := s.persistAnchorLocked(ctx, live); err != nil {
			s.mu.Unlock()
			return Snapshot{}, err
		}
	}
	return s.finishReadyLocked(ctx, live, member, userID, profileID)
}

// acceptReadyLocked validates a readiness report against the member's waiting
// command. Guests must reach a seek destination within readySeekToleranceSeconds
// so the stream being replaced cannot satisfy the seek. The host is the
// authority on position: within hostReadySeekToleranceSeconds the host's actual
// position becomes the room anchor, so a rebuilt stream that lands short of the
// target resumes from where the host really is instead of waiting out the
// deadline. It returns false, with no state change, when the report is stale
// or the destination has not arrived. Must be called with s.mu held.
func (s *Service) acceptReadyLocked(
	ctx context.Context,
	live *liveRoom,
	member *memberState,
	userID int,
	profileID string,
	report StateReport,
) bool {
	command := member.waitingCommand
	if report.CommandID != "" && (command == nil || report.CommandID != command.CommandID) {
		return false
	}
	if command == nil || command.Action != TransportActionSeek {
		return true
	}
	delta := math.Abs(report.PositionSeconds - command.PositionSeconds)
	isHost := userID == live.room.HostUserID && profileID == live.room.HostProfileID
	if !isHost {
		return delta <= readySeekToleranceSeconds
	}
	if delta > hostReadySeekToleranceSeconds {
		return false
	}
	if delta > readySeekToleranceSeconds {
		live.room.AnchorPositionSeconds = math.Max(0, report.PositionSeconds)
		live.room.AnchorUpdatedAt = s.now()
		if _, err := s.persistAnchorLocked(ctx, live); err != nil {
			return false
		}
		// A lost race reloaded live.room from the winning row; readiness is
		// still recorded against whatever that row says.
	}
	return true
}

// finishReadyLocked records the member as ready, resumes the room when every
// participant is, and sends the resulting snapshots and commands. A member
// recovering while the room plays receives the room's current position; its
// stall cooldown is kept, so recovering does not let it pause the room again
// straight away. It must be called with s.mu held and releases it.
func (s *Service) finishReadyLocked(
	ctx context.Context,
	live *liveRoom,
	member *memberState,
	userID int,
	profileID string,
) (Snapshot, error) {
	member.isReady = true
	member.isBuffering = false
	member.ignoreWait = false

	// Past the deadline, the first viewer to become ready resumes the room.
	force := s.skipUnreadyMembersLocked(live, s.now())
	dispatches, commandDispatches := s.maybeResumeFromWaitingLocked(ctx, live, force)
	syncToRoom := len(commandDispatches) == 0 && live.room.Phase == RoomPhasePlaying && live.room.PlaybackState == RoomPlaybackStatePlaying
	if syncToRoom {
		commandDispatches = s.syncMemberToRoomLocked(live, member.sessionID)
		// Until the member reaches that position its reports describe where
		// it recovered, not a decision; a host must not rewind the room there.
		member.syncingToRoom = len(commandDispatches) > 0
	}
	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	if dispatches == nil {
		dispatches = s.prepareSnapshotDispatchesLocked(live)
	}
	roomID := live.room.ID
	s.mu.Unlock()

	if syncToRoom {
		for _, dispatch := range commandDispatches {
			slog.DebugContext(ctx, "watch together sync queued",
				append(memberCommandLogAttrs(roomID, userID, dispatch), "trigger", "ready")...)
		}
	}
	s.sendDispatches(ctx, dispatches)
	s.sendCommandDispatches(ctx, commandDispatches)
	return snapshot, nil
}

func (s *Service) handleBufferingForConnection(
	ctx context.Context,
	reg *Registration,
	userID int,
	profileID string,
	report StateReport,
) (Snapshot, error) {
	if reg == nil {
		return Snapshot{}, ErrRoomForbidden
	}
	if !validPosition(report.PositionSeconds) {
		return Snapshot{}, ErrInvalidPosition
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

	// A browser can emit waiting/stalled after applying pause. A paused room
	// needs no buffering barrier; a delayed report must not reopen one.
	if live.room.PlaybackState == RoomPlaybackStatePaused {
		snapshot := s.buildSnapshotLocked(live, userID, profileID)
		s.mu.Unlock()
		return snapshot, nil
	}

	member.isBuffering = true
	member.isReady = false
	if live.room.Phase != RoomPhasePlaying {
		snapshot := s.buildSnapshotLocked(live, userID, profileID)
		s.mu.Unlock()
		return snapshot, nil
	}

	if live.room.PlaybackState != RoomPlaybackStateWaiting {
		now := s.now()
		alone := !s.othersWatchingLocked(live, member)
		pauseRoom := alone || s.bufferingMayPauseRoomLocked(live, member, now)
		member.lastStallAt = now
		if alone {
			// Waiting for a lone viewer costs nobody; letting the room run on
			// would only skip what they have not seen.
			member.ignoreWait = false
		}
		if !pauseRoom {
			// The room keeps playing. This member catches up on its own and
			// acknowledges recovery with ready.
			member.ignoreWait = true
			member.correctionCommand = nil
			snapshot := s.buildSnapshotLocked(live, userID, profileID)
			dispatches = s.prepareSnapshotDispatchesLocked(live)
			s.mu.Unlock()
			s.sendDispatches(ctx, dispatches)
			return snapshot, nil
		}
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
		if !alone {
			live.bufferingWaitAt = now
		}
	}

	snapshot := s.buildSnapshotLocked(live, userID, profileID)
	dispatches = s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()

	s.sendDispatches(ctx, dispatches)
	s.sendCommandDispatches(ctx, commandDispatches)
	return snapshot, nil
}

// bufferingMayPauseRoomLocked reports whether a member's stall while the room
// plays should pause everyone. The room waits for a viewer once: not while
// they are already catching up, not again until they have played
// memberStallCooldown without stalling, and not within bufferingWaitSpacing of
// the last buffering pause. Must be called with s.mu held.
func (s *Service) bufferingMayPauseRoomLocked(live *liveRoom, member *memberState, now time.Time) bool {
	if member.ignoreWait {
		return false
	}
	if !member.lastStallAt.IsZero() && now.Sub(member.lastStallAt) < memberStallCooldown {
		return false
	}
	return live.bufferingWaitAt.IsZero() || now.Sub(live.bufferingWaitAt) >= bufferingWaitSpacing
}

// othersWatchingLocked reports whether anyone besides member has playback
// attached, whether or not they are catching up. Must be called with s.mu held.
func (s *Service) othersWatchingLocked(live *liveRoom, member *memberState) bool {
	for _, other := range live.members {
		if other != member && memberConnected(other) && other.sessionID != "" {
			return true
		}
	}
	return false
}

// skipUnreadyMembersLocked lets a waiting room that has passed its deadline
// resume without the members that are still not ready, and reports whether it
// may. They catch up on their own and, like any viewer who stalls, cannot
// pause the room again until their stall cooldown passes. While nobody is
// ready the room keeps waiting: running it without an audience would only
// skip content for everyone, including a viewer watching alone. Must be called
// with s.mu held.
func (s *Service) skipUnreadyMembersLocked(live *liveRoom, now time.Time) bool {
	if !waitingDeadlineReached(live, now) {
		return false
	}
	anyReady := false
	for _, member := range live.members {
		if memberConnected(member) && member.sessionID != "" && member.isReady {
			anyReady = true
			break
		}
	}
	if !anyReady {
		return false
	}
	for _, member := range live.members {
		if !memberConnected(member) || member.sessionID == "" || member.isReady {
			continue
		}
		member.ignoreWait = true
		member.lastStallAt = now
	}
	return true
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

func (s *Service) updatePolicy(
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

	s.sendDispatches(ctx, dispatches)
	return snapshot, nil
}

// StageItem sets what a host-pick lobby will play without starting it. The
// room stays in the lobby; StartStagedOnce moves it to playing. Staging bumps
// the room generation but not the selection revision, because the revision is
// the playback epoch and nothing has started.
func (s *Service) stageItem(
	ctx context.Context,
	roomID string,
	userID int,
	profileID string,
	input SelectItemInput,
) (Snapshot, error) {
	if s == nil || s.selectionResolver == nil {
		return Snapshot{}, fmt.Errorf("watch together selection unavailable")
	}
	if strings.TrimSpace(input.ContentID) == "" {
		return Snapshot{}, ErrInvalidSelection
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
	if live.room.SelectionMode == RoomSelectionModeVote {
		s.mu.Unlock()
		return Snapshot{}, ErrVoteRoomSelection
	}
	if live.room.Phase != RoomPhaseLobby {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomNotInLobby
	}

	contentChanged := live.room.SelectedContentID == nil || *live.room.SelectedContentID != resolved.ContentID
	if !contentChanged && equalSelectionID(live.room.SelectedFileID, resolved.FileID) && equalSelectionID(live.room.SelectedLibraryID, resolved.LibraryID) {
		snapshot := s.buildSnapshotLocked(live, userID, profileID)
		s.mu.Unlock()
		return snapshot, nil
	}
	contentID := resolved.ContentID
	live.room.SelectedContentID = &contentID
	live.room.SelectedFileID = resolved.FileID
	live.room.SelectedLibraryID = resolved.LibraryID
	// Staging is activity: keep the idle janitor away from a lobby that is
	// being used.
	live.room.AnchorUpdatedAt = s.now()
	if contentChanged {
		s.clearLobbyReadyLocked(live)
	}
	conflict, updateErr := s.persistRoomChangeLocked(ctx, live, func(room Room, expectedGeneration int64) (*Room, error) {
		return s.repo.UpdateStagedSelection(
			ctx,
			roomID,
			SelectItemInput{ContentID: contentID, FileID: room.SelectedFileID, LibraryID: room.SelectedLibraryID},
			room.AnchorUpdatedAt,
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

	s.sendDispatches(ctx, dispatches)
	return snapshot, nil
}

// UpdateSelectionMode switches how the lobby decides what to play. Only the
// host may switch, only while nothing is playing. A staged item is dropped;
// suggestions and votes are rows that outlive the switch, so a room that goes
// vote → host_pick can still promote any of them.
func (s *Service) updateSelectionMode(
	ctx context.Context,
	roomID string,
	userID int,
	profileID string,
	mode RoomSelectionMode,
) (Snapshot, error) {
	if mode != RoomSelectionModeHostPick && mode != RoomSelectionModeVote {
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
	if live.room.Phase != RoomPhaseLobby {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomNotInLobby
	}
	if live.room.SelectionMode == mode {
		snapshot := s.buildSnapshotLocked(live, userID, profileID)
		s.mu.Unlock()
		return snapshot, nil
	}

	live.room.SelectionMode = mode
	live.room.SelectedContentID = nil
	live.room.SelectedFileID = nil
	live.room.SelectedLibraryID = nil
	live.room.AnchorUpdatedAt = s.now()
	s.clearLobbyReadyLocked(live)
	conflict, updateErr := s.persistRoomChangeLocked(ctx, live, func(room Room, expectedGeneration int64) (*Room, error) {
		return s.repo.UpdateSelectionMode(ctx, roomID, room.SelectionMode, room.AnchorUpdatedAt, room.Generation, expectedGeneration)
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

	s.sendDispatches(ctx, dispatches)
	return snapshot, nil
}

// clearLobbyReadyLocked forgets every member's lobby "ready". Must be called
// with s.mu held.
func (s *Service) clearLobbyReadyLocked(live *liveRoom) {
	for _, member := range live.members {
		if member != nil {
			member.lobbyReady = false
		}
	}
}

// SelectItem sets what the room plays at the host's direct request. In a vote
// room that request is refused: the vote decides, and PromoteSuggestion is the
// only way in.
func (s *Service) SelectItem(
	ctx context.Context,
	roomID string,
	userID int,
	profileID string,
	input SelectItemInput,
) (Snapshot, error) {
	return s.selectItem(ctx, roomID, userID, profileID, input, false)
}

// selectItem carries out a selection. viaVote is set only by PromoteSuggestion
// once it has confirmed the suggestion is the room's winner — that call has
// already satisfied the vote, so gating it here would leave a vote room with no
// way at all to start playback.
func (s *Service) selectItemInRoom(
	ctx context.Context,
	roomID string,
	userID int,
	profileID string,
	resolved *ResolvedSelection,
	viaVote bool,
) (Snapshot, error) {

	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	if live.room.HostUserID != userID || live.room.HostProfileID != profileID {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomForbidden
	}
	// A vote room decides by tally, and a direct selection is the other door
	// into the room's selection. Gating only PromoteSuggestion would leave the
	// host able to set any title directly and bypass the vote entirely, which
	// makes the counts on everyone else's screen decoration. Once the room is
	// voting, the winner is the only way in.
	if !viaVote && live.room.SelectionMode == RoomSelectionModeVote {
		s.mu.Unlock()
		return Snapshot{}, ErrVoteRoomSelection
	}
	if live.room.Phase == RoomPhaseEnded {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomClosed
	}

	conflict, updateErr := s.applySelectionLocked(ctx, live, resolved, 0, true)
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

	s.sendDispatches(ctx, dispatches)
	return snapshot, nil
}

// applySelectionLocked replaces the shared source and invalidates every old attachment.
func (s *Service) applySelectionLocked(ctx context.Context, live *liveRoom, resolved *ResolvedSelection, position float64, resume bool) (bool, error) {
	now := s.now()
	live.room.Phase = RoomPhasePlaying
	live.room.PlaybackState = RoomPlaybackStateWaiting
	live.room.ResumeOnReady = resume
	live.room.SelectedContentID = &resolved.ContentID
	live.room.SelectedFileID = resolved.FileID
	live.room.SelectedLibraryID = resolved.LibraryID
	live.room.AnchorPositionSeconds = position
	live.room.IsPaused = true
	live.room.AnchorUpdatedAt = now
	live.room.SelectionRevision++
	live.command = nil
	// Sessions attached for the previous selection are stale: readiness for
	// the new content must come from a fresh attach, not an old session.
	for _, member := range live.members {
		if member != nil {
			member.resetForSelection()
		}
	}
	live.bufferingWaitAt = time.Time{}
	s.disarmWaitingDeadlineLocked(live)

	return s.persistRoomChangeLocked(ctx, live, func(room Room, expectedGeneration int64) (*Room, error) {
		return s.repo.UpdateSelection(
			ctx,
			live.room.ID,
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
}

func (s *Service) closeRoom(ctx context.Context, roomID string, userID int, profileID string) error {
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
		if operationFrom(ctx) == nil {
			delete(s.rooms, roomID)
		}
	}
	s.mu.Unlock()

	s.publishRoomStateAfterCommit(ctx, *room)
	s.sendDispatches(ctx, dispatches)
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
	s.publishRoomStateAfterCommit(ctx, live.room)
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
	s.disarmWaitingDeadlineLocked(live)
	if _, shared := s.repo.(*Repository); shared {
		// The reconciler checks the persisted command's issue time. Process
		// timers cannot track a barrier replaced by another API server.
		return
	}
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
func (s *Service) waitingDeadline(ctx context.Context, roomID string, epoch int64) {
	s.mu.Lock()
	live := s.rooms[roomID]
	if live == nil || live.waitingEpoch != epoch || live.room.PlaybackState != RoomPlaybackStateWaiting {
		s.mu.Unlock()
		return
	}
	// Nobody is ready yet: keep waiting. The first ready member resumes the
	// room from finishReadyLocked.
	if !s.skipUnreadyMembersLocked(live, s.now()) {
		s.mu.Unlock()
		return
	}
	dispatches, commandDispatches := s.maybeResumeFromWaitingLocked(ctx, live, true)
	s.mu.Unlock()
	s.sendDispatches(ctx, dispatches)
	s.sendCommandDispatches(ctx, commandDispatches)
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
		if live == nil || (live.activeOperations == 0 && !hasLocalRoomWork(live)) {
			// Room state is fully persisted; it reloads on next access. The
			// shared host-expiry sweep does not depend on this local cache.
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
	for _, roomID := range roomIDs {
		_, _ = withRoomOperation(ctx, s, roomID, func(ctx context.Context) (struct{}, error) {
			s.mu.Lock()
			live := s.rooms[roomID]
			hasMembers := live != nil && s.connectedMemberCountLocked(live) > 0
			s.mu.Unlock()
			if hasMembers {
				return struct{}{}, nil
			}
			room, err := s.repo.CloseRoom(ctx, roomID, s.now())
			if err == nil && room != nil {
				s.publishRoomStateAfterCommit(ctx, *room)
			}
			return struct{}{}, err
		})
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

func validateSessionContent(room *Room, session *playback.Session, file *models.MediaFile) error {
	if room == nil || session == nil {
		return ErrSessionMismatch
	}
	if room.Phase != RoomPhasePlaying || room.SelectedContentID == nil || *room.SelectedContentID == "" {
		return ErrSessionMismatch
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
		if !memberConnected(m) {
			continue
		}
		members = append(members, MemberSummary{
			UserID:      m.userID,
			ProfileID:   m.profileID,
			DisplayName: m.displayName,
			IsHost:      m.userID == live.room.HostUserID && m.profileID == live.room.HostProfileID,
			IsSelf:      m.userID == userID && m.profileID == profileID,
			Connected:   true,
			IsReady:     m.isReady, IsBuffering: m.isBuffering,
			IsSyncing:  live.room.PlaybackState == RoomPlaybackStateWaiting && m.sessionID != "" && !m.isReady && !m.ignoreWait,
			LobbyReady: m.lobbyReady,
		})
	}
	slices.SortFunc(members, func(a, b MemberSummary) int {
		if a.IsHost != b.IsHost {
			if a.IsHost {
				return -1
			}
			return 1
		}
		if order := cmp.Compare(a.DisplayName, b.DisplayName); order != 0 {
			return order
		}
		if order := cmp.Compare(a.ProfileID, b.ProfileID); order != 0 {
			return order
		}
		return cmp.Compare(a.UserID, b.UserID)
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
	base := s.buildSnapshotLocked(live, 0, "")
	for _, member := range live.members {
		if member == nil || member.connection == nil {
			continue
		}
		dispatches = append(dispatches, snapshotDispatch{
			conn: member.connection,
			payload: map[string]any{
				clusterMessageTypeKey: snapshotMessageType,
				roomPayloadKey:        personalizeSnapshot(base, live.room, member),
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
		if memberConnected(member) {
			count++
		}
	}
	return count
}

func (s *Service) hostConnectedLocked(live *liveRoom) bool {
	member := live.members[buildMemberKey(live.room.HostUserID, live.room.HostProfileID)]
	return memberConnected(member)
}

func (s *Service) expectedPositionLocked(live *liveRoom) float64 {
	return expectedPosition(live.room, s.now())
}

func (s *Service) resetMemberReadinessLocked(live *liveRoom, markBuffering bool) {
	for _, member := range live.members {
		if !memberConnected(member) || member.sessionID == "" {
			continue
		}
		member.isReady = false
		member.correctionCommand = nil
		if markBuffering {
			member.isBuffering = true
		}
	}
}

func (s *Service) activeParticipantCountLocked(live *liveRoom) int {
	count := 0
	for _, member := range live.members {
		if !memberConnected(member) || member.sessionID == "" || member.ignoreWait {
			continue
		}
		count++
	}
	return count
}

func (s *Service) allParticipantsReadyLocked(live *liveRoom) bool {
	participants := 0
	for _, member := range live.members {
		if !memberConnected(member) || member.sessionID == "" || member.ignoreWait {
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
		if !memberConnected(member) || member.sessionID == "" {
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
		if payload.PlaybackState == RoomPlaybackStateWaiting {
			member.waitingCommand = &payload
		}
		dispatches = append(dispatches, commandDispatch{
			conn:      member.connection,
			memberKey: memberKey,
			payload: map[string]any{
				clusterMessageTypeKey: transportMessageType,
				commandPayloadKey:     payload,
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
	s.clearCorrectionCommandsLocked(live)
	command := TransportCommand{
		CommandID: uuid.NewString(), SelectionRevision: live.room.SelectionRevision,
		Action: action, PositionSeconds: math.Max(0, positionSeconds),
		ExecuteAt: executeAt.UTC().Format(time.RFC3339Nano),
		IssuedAt:  s.now().UTC().Format(time.RFC3339Nano), PlaybackState: live.room.PlaybackState,
	}
	live.command = &command
	dispatches := make([]commandDispatch, 0, len(live.members))
	for memberKey, member := range live.members {
		if !memberConnected(member) || member.sessionID == "" {
			continue
		}
		payload := command
		payload.SessionID = member.sessionID
		if payload.PlaybackState == RoomPlaybackStateWaiting {
			member.waitingCommand = &payload
		}
		if member.connection != nil {
			member.lastCommandID = command.CommandID
			dispatches = append(dispatches, commandDispatch{conn: member.connection, memberKey: memberKey,
				payload: map[string]any{clusterMessageTypeKey: transportMessageType, commandPayloadKey: payload}})
		}
	}
	return dispatches
}

func (s *Service) clearCorrectionCommandsLocked(live *liveRoom) {
	for _, member := range live.members {
		if member != nil {
			member.correctionCommand = nil
		}
	}
}

// memberCommandLogAttrs describes a command sent to one member to move its
// playback to the room. Clients apply these without telling the viewer, so the
// log is the only trace of how often the room moved a member.
func memberCommandLogAttrs(roomID string, userID int, dispatch commandDispatch) []any {
	command, _ := dispatch.payload[commandPayloadKey].(TransportCommand)
	return []any{
		"component", "watchtogether", "room_id", roomID, "user_id", userID,
		"session_id", command.SessionID, "command_id", command.CommandID, "action", command.Action,
		"playback_state", command.PlaybackState, "target_position_seconds", command.PositionSeconds,
	}
}

func correctionCommandPending(member *memberState, command TransportCommand, now time.Time) bool {
	if member == nil || member.correctionCommand == nil {
		return false
	}
	pending := member.correctionCommand
	if pending.SessionID != command.SessionID ||
		pending.SelectionRevision != command.SelectionRevision ||
		pending.Action != command.Action ||
		pending.PlaybackState != command.PlaybackState {
		return false
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, pending.IssuedAt)
	if err != nil {
		return false
	}
	age := now.Sub(issuedAt)
	if age < -guestCorrectionRetryInterval {
		return false
	}
	if age < 0 {
		age = 0
	}
	return age < guestCorrectionRetryInterval
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
	if live.room.PlaybackState == RoomPlaybackStateWaiting && live.command != nil && live.command.SelectionRevision == live.room.SelectionRevision {
		return s.targetedCommandDispatchesLocked(live, sessionID, *live.command)
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

func validPosition(position float64) bool {
	return !math.IsNaN(position) && !math.IsInf(position, 0) && position >= 0 && position <= maxPositionSeconds
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

	suggestions, err := s.suggestions.ListSuggestions(ctx, roomID, userID, profileID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	live = s.rooms[roomID]
	if live != nil {
		dispatches := s.prepareSuggestionDispatchesLocked(live, suggestions)
		s.mu.Unlock()
		s.sendDispatches(ctx, dispatches)
	} else {
		s.mu.Unlock()
	}
	s.publishSuggestionUpdate(roomID)

	return suggestions, nil
}

func (s *Service) ListSuggestions(
	ctx context.Context,
	roomID string,
	userID int,
	profileID string,
) ([]Suggestion, error) {
	if s == nil || s.suggestions == nil {
		return nil, fmt.Errorf("watch together suggestions unavailable")
	}
	if _, _, err := s.getOrLoadLiveRoom(ctx, roomID); err != nil {
		return nil, err
	}
	return s.suggestions.ListSuggestions(ctx, roomID, userID, profileID)
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

	suggestions, err := s.suggestions.ListSuggestions(ctx, roomID, userID, profileID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	live = s.rooms[roomID]
	if live != nil {
		dispatches := s.prepareSuggestionDispatchesLocked(live, suggestions)
		s.mu.Unlock()
		s.sendDispatches(ctx, dispatches)
	} else {
		s.mu.Unlock()
	}
	s.publishSuggestionUpdate(roomID)

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

	if err := s.suggestions.AddVote(ctx, suggestionID, userID, profileID); err != nil {
		return nil, err
	}

	suggestions, err := s.suggestions.ListSuggestions(ctx, roomID, userID, profileID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	live = s.rooms[roomID]
	if live != nil {
		dispatches := s.prepareSuggestionDispatchesLocked(live, suggestions)
		s.mu.Unlock()
		s.sendDispatches(ctx, dispatches)
	} else {
		s.mu.Unlock()
	}
	s.publishSuggestionUpdate(roomID)

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

	if err := s.suggestions.RemoveVote(ctx, suggestionID, userID, profileID); err != nil {
		return nil, err
	}

	suggestions, err := s.suggestions.ListSuggestions(ctx, roomID, userID, profileID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	live = s.rooms[roomID]
	if live != nil {
		dispatches := s.prepareSuggestionDispatchesLocked(live, suggestions)
		s.mu.Unlock()
		s.sendDispatches(ctx, dispatches)
	} else {
		s.mu.Unlock()
	}
	s.publishSuggestionUpdate(roomID)

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

	// V1 keeps its frozen winner-only behavior. V2 permits the host override
	// through PromoteSuggestionOnce.
	s.mu.Lock()
	isVoteRoom := live.room.SelectionMode == RoomSelectionModeVote
	s.mu.Unlock()
	if isVoteRoom {
		winner, err := s.VoteWinner(ctx, roomID)
		if err != nil {
			return Snapshot{}, err
		}
		if winner.ID != suggestion.ID {
			return Snapshot{}, ErrNotVoteWinner
		}
	}

	return s.selectItem(ctx, roomID, userID, profileID, SelectItemInput{
		ContentID: suggestion.ContentID,
	}, isVoteRoom)
}

// VoteWinner returns the suggestion a vote-mode room has settled on.
//
// The repository already orders by vote_count DESC, created_at ASC, so the
// winner is the head of the list and ties resolve to whoever suggested first —
// deterministic, and it does not reward re-suggesting the same title.
//
// A room where nobody has voted has no winner. Returning the oldest suggestion
// there would let a host "start the vote winner" for a vote that never
// happened, which is exactly the confusion this mode exists to avoid.
func (s *Service) VoteWinner(ctx context.Context, roomID string) (Suggestion, error) {
	if s == nil || s.suggestions == nil {
		return Suggestion{}, fmt.Errorf("watch together suggestions unavailable")
	}
	if _, _, err := s.getOrLoadLiveRoom(ctx, roomID); err != nil {
		return Suggestion{}, err
	}
	suggestions, err := s.suggestions.ListSuggestions(ctx, roomID, 0, "")
	if err != nil {
		return Suggestion{}, err
	}
	return winnerFrom(suggestions)
}

// winnerFrom picks the winner out of an already-ordered suggestion list. Split
// out so the rule can be tested without a database.
func winnerFrom(ordered []Suggestion) (Suggestion, error) {
	if len(ordered) == 0 || ordered[0].VoteCount <= 0 {
		return Suggestion{}, ErrNoVotesCast
	}
	return ordered[0], nil
}

func (s *Service) prepareSuggestionDispatchesLocked(live *liveRoom, suggestions []Suggestion) []snapshotDispatch {
	if live == nil || live.room.Phase == RoomPhaseEnded {
		return nil
	}
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
				clusterMessageTypeKey: suggestionsUpdateType,
				"suggestions":         broadcast,
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

// The sorted roster and common room fields are built once for each broadcast.
func personalizeSnapshot(base Snapshot, room Room, member *memberState) Snapshot {
	base.SelfRole = roleFor(room, member.userID, member.profileID)
	base.SelfCanManageRoom = base.SelfRole == MemberRoleHost
	base.SelfCanControlTransport = base.SelfCanManageRoom || (room.Phase == RoomPhasePlaying && room.GuestControlPolicy == GuestControlPolicyGuestPlayPause)
	base.SelfIgnoreWait, base.AttachedSessionID = member.ignoreWait, member.sessionID
	if base.SelfCanManageRoom {
		base.InvitePath = fmt.Sprintf("/rooms/join?token=%s", room.JoinToken)
	}
	base.Members = slices.Clone(base.Members)
	for i := range base.Members {
		base.Members[i].IsSelf = base.Members[i].UserID == member.userID && base.Members[i].ProfileID == member.profileID
	}
	return base
}

// SetPlaybackAttemptStore supplies durable session ownership when a room socket
// lands on a different API server from the playback start request.
func (s *Service) SetPlaybackAttemptStore(store interface {
	GetAttempt(context.Context, string) (*playback.AttemptRecordV3, error)
}) {
	s.attempts = store
}

func (s *Service) lookupSession(ctx context.Context, sessionID string) (*playback.Session, error) {
	if s.attempts != nil {
		record, err := s.attempts.GetAttempt(ctx, sessionID)
		if err == nil && record != nil {
			if record.StoppedAt != nil {
				return nil, playback.ErrSessionNotFound
			}
			return &playback.Session{ID: record.SessionID, UserID: record.UserID, ProfileID: record.ProfileID, MediaFileID: record.EffectiveMediaFileID}, nil
		}
		if !errors.Is(err, playback.ErrSessionNotFound) {
			return nil, err
		}
	}
	if s.sessions == nil {
		return nil, playback.ErrSessionNotFound
	}
	session, err := s.sessions.GetSession(sessionID)
	if err == nil && session != nil && s.attempts != nil && session.RequireMediaAuthorization {
		return nil, playback.ErrSessionNotFound
	}
	return session, err
}

func hasLocalRoomWork(live *liveRoom) bool {
	if len(live.pendingDisconnects) > 0 {
		return true
	}
	for _, member := range live.members {
		if member != nil && member.connection != nil {
			return true
		}
	}
	return false
}
