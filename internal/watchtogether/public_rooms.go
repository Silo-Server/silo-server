package watchtogether

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/cache"
)

var (
	// ErrPublicRoomsUnavailable means the room store cannot list rooms or keep
	// join requests (an in-memory store in tests).
	ErrPublicRoomsUnavailable = errors.New("watch together public rooms unavailable")
	// ErrRoomNotPublic refuses a join request to a room its host has not listed.
	ErrRoomNotPublic       = errors.New("watch together room is not public")
	ErrJoinRequestNotFound = errors.New("watch together join request not found")
	// ErrJoinRequestApproved refuses turning an approval into a denial: the
	// requester already holds room proof and the room code, so a denial
	// would not take them back out.
	ErrJoinRequestApproved = errors.New("watch together join request is already approved")
	ErrInvalidVisibility   = errors.New("watch together room visibility is invalid")
)

// maxPublicRooms bounds the open-party list. It is a household-scale list of
// rooms currently open on the server, not a catalog.
const maxPublicRooms = 100

// maxPendingJoinRequests bounds what one room's host is sent. Each profile
// holds at most one request per room, so this only clips a very large server.
const maxPendingJoinRequests = 100

const (
	joinRequestsUpdateType       = "join_requests_update"
	clusterJoinRequestsEventType = "watch_together_join_requests"
)

type JoinRequestStatus string

const (
	JoinRequestStatusPending  JoinRequestStatus = "pending"
	JoinRequestStatusApproved JoinRequestStatus = "approved"
	JoinRequestStatusDenied   JoinRequestStatus = "denied"
)

// JoinRequest is a profile asking the host of a public room to let it in.
type JoinRequest struct {
	ID          string            `json:"id"`
	RoomID      string            `json:"room_id"`
	UserID      int               `json:"user_id"`
	ProfileID   string            `json:"profile_id"`
	DisplayName string            `json:"display_name"`
	Status      JoinRequestStatus `json:"status"`
	CreatedAt   time.Time         `json:"created_at"`
	DecidedAt   *time.Time        `json:"decided_at,omitempty"`
}

// PublicRoom is one open public room as another profile on the server sees
// it. It never carries the room code or invite token: a viewer gets in only
// through a request the host approves.
type PublicRoom struct {
	RoomID            string
	HostUserID        int
	HostProfileID     string
	HostDisplayName   string
	HostConnected     bool
	Phase             RoomPhase
	PlaybackState     RoomPlaybackState
	SelectionMode     RoomSelectionMode
	SelectedContentID *string
	MemberCount       int
	CreatedAt         time.Time
	// IsHost marks the viewer's own room.
	IsHost bool
	// Request is the viewer's own join request, when it has made one.
	Request *JoinRequest
}

// publicRoomRow is a listed room with its raw runtime, which holds presence.
type publicRoomRow struct {
	Room    Room
	Runtime []byte
	Request *JoinRequest
}

// JoinRequestStore lists public rooms and keeps their join requests.
type JoinRequestStore interface {
	ListPublicRooms(ctx context.Context, viewerUserID int, viewerProfileID string, limit int) ([]publicRoomRow, error)
	// CreateJoinRequest records a pending request for an open public room. A
	// denied request becomes pending again; a pending or approved one is
	// returned unchanged. changed reports whether the host has something new
	// to decide.
	CreateJoinRequest(ctx context.Context, request JoinRequest) (row *JoinRequest, changed bool, err error)
	// DecideJoinRequest records the host's answer. It refuses to deny an
	// approved request with ErrJoinRequestApproved.
	GetJoinRequest(ctx context.Context, requestID string) (*JoinRequest, error)
	ListPendingJoinRequests(ctx context.Context, roomID string, limit int) ([]JoinRequest, error)
	DecideJoinRequest(ctx context.Context, roomID, requestID string, status JoinRequestStatus, decidedAt time.Time) (*JoinRequest, error)
}

type roomVisibilityStore interface {
	UpdateVisibility(ctx context.Context, roomID string, visibility RoomVisibility, generation int64, expectedGeneration int64) (*Room, error)
}

// SetJoinRequestStore wires public-room listing and join requests. Without it
// those operations return ErrPublicRoomsUnavailable.
func (s *Service) SetJoinRequestStore(store JoinRequestStore) {
	s.joinRequests = store
}

// PublicRoomsAvailable reports whether rooms can be listed and requested.
func (s *Service) PublicRoomsAvailable() bool {
	if s == nil || s.joinRequests == nil {
		return false
	}
	_, ok := s.repo.(roomVisibilityStore)
	return ok
}

// visibility treats a room row from before the column existed as private.
func (r Room) visibility() RoomVisibility {
	if r.Visibility == RoomVisibilityPublic {
		return RoomVisibilityPublic
	}
	return RoomVisibilityPrivate
}

// SetRoomVisibility lists or unlists a room. Host only. Pending requests stay
// with the room either way, so a host who unlists it can still answer them.
func (s *Service) SetRoomVisibility(ctx context.Context, roomID string, user int, profile string, visibility RoomVisibility) (Snapshot, error) {
	return withRoomOperation(ctx, s, roomID, func(ctx context.Context) (Snapshot, error) {
		return s.setRoomVisibility(ctx, roomID, user, profile, visibility)
	})
}

// setRoomVisibility runs inside the room operation; see SetRoomVisibility.
func (s *Service) setRoomVisibility(ctx context.Context, roomID string, userID int, profileID string, visibility RoomVisibility) (Snapshot, error) {
	if visibility != RoomVisibilityPrivate && visibility != RoomVisibilityPublic {
		return Snapshot{}, ErrInvalidVisibility
	}
	store, ok := s.repo.(roomVisibilityStore)
	if !ok {
		return Snapshot{}, ErrPublicRoomsUnavailable
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
	if live.room.visibility() == visibility {
		snapshot := s.buildSnapshotLocked(live, userID, profileID)
		s.mu.Unlock()
		return snapshot, nil
	}
	previous := live.room.Visibility
	live.room.Visibility = visibility
	conflict, updateErr := s.persistRoomChangeLocked(ctx, live, func(room Room, expectedGeneration int64) (*Room, error) {
		return store.UpdateVisibility(ctx, roomID, room.Visibility, room.Generation, expectedGeneration)
	})
	if updateErr != nil {
		// The write did not land; keep the live room matching the database so
		// a retry is not taken for a no-op.
		live.room.Visibility = previous
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

// ListPublicRooms returns the open public rooms on the server, newest first.
// Presence comes from the shared room runtime, so rooms served by other API
// servers count their members too.
func (s *Service) ListPublicRooms(ctx context.Context, userID int, profileID string) ([]PublicRoom, error) {
	if !s.PublicRoomsAvailable() {
		return nil, ErrPublicRoomsUnavailable
	}
	rows, err := s.joinRequests.ListPublicRooms(ctx, userID, profileID, maxPublicRooms)
	if err != nil {
		return nil, err
	}
	now := s.now()
	rooms := make([]PublicRoom, 0, len(rows))
	// A host missing from the runtime has never connected. Look each such
	// host up once per list, not once per room.
	names := make(map[string]string)
	for _, row := range rows {
		room := row.Room
		var runtime roomRuntime
		if len(row.Runtime) > 0 {
			if err := json.Unmarshal(row.Runtime, &runtime); err != nil {
				slog.WarnContext(ctx, "watch together public room runtime unreadable",
					"component", "watchtogether", "room_id", room.ID, "error", err)
			}
		}
		hostKey := buildMemberKey(room.HostUserID, room.HostProfileID)
		listed := PublicRoom{
			RoomID:            room.ID,
			HostUserID:        room.HostUserID,
			HostProfileID:     room.HostProfileID,
			Phase:             room.Phase,
			PlaybackState:     room.PlaybackState,
			SelectionMode:     room.SelectionMode,
			SelectedContentID: room.SelectedContentID,
			CreatedAt:         room.CreatedAt,
			IsHost:            room.HostUserID == userID && room.HostProfileID == profileID,
			Request:           row.Request,
		}
		for key, member := range runtime.Members {
			present := member.Connected && member.LeaseUntil.After(now)
			if present {
				listed.MemberCount++
			}
			if key == hostKey {
				listed.HostConnected = present
				listed.HostDisplayName = member.DisplayName
			}
		}
		if listed.HostDisplayName == "" {
			name, ok := names[hostKey]
			if !ok {
				name = s.displayName(ctx, room.HostUserID, room.HostProfileID)
				names[hostKey] = name
			}
			listed.HostDisplayName = name
		}
		rooms = append(rooms, listed)
	}
	return rooms, nil
}

// RequestToJoin asks the host of a public room to let the profile in. Asking
// again while pending or approved returns the same request without notifying
// the host again.
func (s *Service) RequestToJoin(ctx context.Context, roomID string, userID int, profileID string) (JoinRequest, error) {
	if !s.PublicRoomsAvailable() {
		return JoinRequest{}, ErrPublicRoomsUnavailable
	}
	room, err := s.GetRoom(ctx, roomID)
	if err != nil {
		return JoinRequest{}, err
	}
	if room.HostUserID == userID && room.HostProfileID == profileID {
		return JoinRequest{}, ErrInvalidJoinRequest
	}
	if room.visibility() != RoomVisibilityPublic {
		return JoinRequest{}, ErrRoomNotPublic
	}
	row, changed, err := s.joinRequests.CreateJoinRequest(ctx, JoinRequest{
		ID:          uuid.NewString(),
		RoomID:      roomID,
		UserID:      userID,
		ProfileID:   profileID,
		DisplayName: s.displayName(ctx, userID, profileID),
		Status:      JoinRequestStatusPending,
		CreatedAt:   s.now(),
	})
	if err != nil {
		return JoinRequest{}, err
	}
	if changed {
		s.notifyJoinRequests(ctx, roomID)
	}
	return *row, nil
}

// ListJoinRequests returns a room's pending requests, oldest first. Host only.
func (s *Service) ListJoinRequests(ctx context.Context, roomID string, userID int, profileID string) ([]JoinRequest, error) {
	if !s.PublicRoomsAvailable() {
		return nil, ErrPublicRoomsUnavailable
	}
	if err := s.requireHost(ctx, roomID, userID, profileID); err != nil {
		return nil, err
	}
	return s.joinRequests.ListPendingJoinRequests(ctx, roomID, maxPendingJoinRequests)
}

// DecideJoinRequest approves or denies a request. Host only. A denied
// request may still be approved; an approved one is final.
func (s *Service) DecideJoinRequest(ctx context.Context, roomID, requestID string, userID int, profileID string, status JoinRequestStatus) (JoinRequest, error) {
	if !s.PublicRoomsAvailable() {
		return JoinRequest{}, ErrPublicRoomsUnavailable
	}
	if status != JoinRequestStatusApproved && status != JoinRequestStatusDenied {
		return JoinRequest{}, ErrInvalidJoinRequest
	}
	if err := s.requireHost(ctx, roomID, userID, profileID); err != nil {
		return JoinRequest{}, err
	}
	row, err := s.joinRequests.DecideJoinRequest(ctx, roomID, requestID, status, s.now())
	if err != nil {
		return JoinRequest{}, err
	}
	s.notifyJoinRequests(ctx, roomID)
	return *row, nil
}

// ReadOwnJoinRequest returns the requester's own request in an open room.
// Another profile's request reads as not found.
func (s *Service) ReadOwnJoinRequest(ctx context.Context, roomID, requestID string, userID int, profileID string) (JoinRequest, error) {
	if !s.PublicRoomsAvailable() {
		return JoinRequest{}, ErrPublicRoomsUnavailable
	}
	row, err := s.joinRequests.GetJoinRequest(ctx, requestID)
	if err != nil {
		return JoinRequest{}, err
	}
	if row.RoomID != roomID || row.UserID != userID || row.ProfileID != profileID {
		return JoinRequest{}, ErrJoinRequestNotFound
	}
	if _, err := s.GetRoom(ctx, roomID); err != nil {
		return JoinRequest{}, err
	}
	return *row, nil
}

// requireHost returns ErrRoomForbidden unless the profile hosts the open room.
func (s *Service) requireHost(ctx context.Context, roomID string, userID int, profileID string) error {
	room, err := s.GetRoom(ctx, roomID)
	if err != nil {
		return err
	}
	if room.HostUserID != userID || room.HostProfileID != profileID {
		return ErrRoomForbidden
	}
	return nil
}

// displayName is the profile's name, or the generic member name without a resolver.
func (s *Service) displayName(ctx context.Context, userID int, profileID string) string {
	if s.profileNames == nil {
		return fallbackMemberName
	}
	return s.profileNames.ProfileDisplayName(ctx, userID, profileID)
}

// notifyJoinRequests sends the room's pending requests to its host on this
// server and asks the other servers to do the same for theirs.
func (s *Service) notifyJoinRequests(ctx context.Context, roomID string) {
	s.sendJoinRequestsToLocalHost(ctx, roomID)
	s.publishJoinRequestsUpdate(roomID)
}

// sendJoinRequestsToLocalHost pushes the pending list to the host's socket
// when this server holds it. Only the v2 room socket carries the message.
func (s *Service) sendJoinRequestsToLocalHost(ctx context.Context, roomID string) {
	if s.joinRequests == nil {
		return
	}
	s.mu.Lock()
	var conn RoomConnection
	if live := s.rooms[roomID]; live != nil && live.room.Phase != RoomPhaseEnded {
		host := live.members[buildMemberKey(live.room.HostUserID, live.room.HostProfileID)]
		if host != nil && host.connection != nil {
			if legacy, ok := host.connection.(interface{ LegacyRoomProtocol() bool }); !ok || !legacy.LegacyRoomProtocol() {
				conn = host.connection
			}
		}
	}
	s.mu.Unlock()
	if conn == nil {
		return
	}
	// Each send reads the list after its own change committed. Holding one
	// lock across the read and the write keeps the host's frames in read
	// order, so an older list can never land after a newer one.
	s.joinRequestSendMu.Lock()
	defer s.joinRequestSendMu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	rows, err := s.joinRequests.ListPendingJoinRequests(ctx, roomID, maxPendingJoinRequests)
	if err != nil {
		slog.WarnContext(ctx, "watch together join requests not sent to host",
			"component", "watchtogether", "room_id", roomID, "error", err)
		return
	}
	if rows == nil {
		rows = []JoinRequest{}
	}
	// Match the HTTP list's instants (UTC, millisecond precision).
	for i := range rows {
		rows[i].CreatedAt = rows[i].CreatedAt.UTC().Truncate(time.Millisecond)
		if rows[i].DecidedAt != nil {
			decided := rows[i].DecidedAt.UTC().Truncate(time.Millisecond)
			rows[i].DecidedAt = &decided
		}
	}
	_ = conn.WriteJSON(map[string]any{
		clusterMessageTypeKey: joinRequestsUpdateType,
		"join_requests":       rows,
	})
}

// publishJoinRequestsUpdate asks the other API servers to resend the
// room's pending list to a host socket they hold.
func (s *Service) publishJoinRequestsUpdate(roomID string) {
	s.clusterMu.Lock()
	bus := s.clusterBus
	s.clusterMu.Unlock()
	if bus == nil {
		return
	}
	payload, _ := json.Marshal(clusterRoomEvent{Source: s.instanceID, RoomID: roomID})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = bus.Publish(ctx, cache.ChannelPlayback, cache.Event{Type: clusterJoinRequestsEventType, Payload: string(payload)})
	}()
}

// handleClusterJoinRequests keeps the pending-list query off the PubSub reader.
func (s *Service) handleClusterJoinRequests(payload string) {
	var incoming clusterRoomEvent
	if json.Unmarshal([]byte(payload), &incoming) != nil || incoming.Source == s.instanceID || incoming.RoomID == "" {
		return
	}
	go s.sendJoinRequestsToLocalHost(context.Background(), incoming.RoomID)
}
