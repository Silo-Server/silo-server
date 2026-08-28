package playback

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/Silo-Server/silo-server/internal/models"
)

type markerUpdateSessionLookup interface {
	GetSessionsByMediaFileID(fileID int) []*Session
}

type markerFileState struct {
	token chan struct{}
	epoch atomic.Uint64
	refs  int
}

func newMarkerFileState() *markerFileState {
	state := &markerFileState{token: make(chan struct{}, 1)}
	state.token <- struct{}{}
	return state
}

func (s *markerFileState) lock() {
	<-s.token
}

func (s *markerFileState) lockContext(ctx context.Context, registrationDone <-chan struct{}) bool {
	select {
	case <-ctx.Done():
		return false
	case <-registrationDone:
		return false
	case <-s.token:
		return true
	}
}

func (s *markerFileState) unlock() {
	s.token <- struct{}{}
}

type markerSnapshotPayload struct {
	fileID  int
	intro   *TimeRangePayload
	credits *TimeRangePayload
	recap   *TimeRangePayload
	preview *TimeRangePayload
}

// MarkerUpdateNotifier publishes live marker updates to active playback sessions.
type MarkerUpdateNotifier struct {
	sessions markerUpdateSessionLookup
	hub      *RealtimeHub
	stateMu  sync.Mutex
	states   map[int]*markerFileState
}

// MarkerSnapshotFileLoader loads the persisted media-file marker row used for
// a reconnect snapshot.
type MarkerSnapshotFileLoader interface {
	GetByID(ctx context.Context, fileID int) (*models.MediaFile, error)
}

func NewMarkerUpdateNotifier(sessions markerUpdateSessionLookup, hub *RealtimeHub) *MarkerUpdateNotifier {
	if sessions == nil || hub == nil {
		return nil
	}
	return &MarkerUpdateNotifier{
		sessions: sessions,
		hub:      hub,
		states:   make(map[int]*markerFileState),
	}
}

func (n *MarkerUpdateNotifier) MarkersUpdated(ctx context.Context, file *models.MediaFile) {
	if n == nil || file == nil || file.ID <= 0 {
		return
	}

	state := n.acquireFileState(file.ID)
	state.lock()
	epoch := state.epoch.Add(1)
	payload := markerSnapshotPayloadFromFile(file)
	state.unlock()

	sessionIDs := make([]string, 0)
	for _, session := range n.sessions.GetSessionsByMediaFileID(file.ID) {
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		sessionIDs = append(sessionIDs, session.ID)
	}
	if len(sessionIDs) == 0 {
		n.releaseFileState(file.ID, state)
		return
	}

	logCtx := context.Background()
	if ctx != nil {
		logCtx = context.WithoutCancel(ctx)
	}
	go func() {
		defer n.releaseFileState(file.ID, state)
		for _, sessionID := range sessionIDs {
			n.sendCurrentEpochToSession(logCtx, state, epoch, sessionID, payload)
		}
	}()
}

// SendSessionSnapshotFromLoader reads a persisted snapshot while holding the
// file state, then sends it only if no newer MarkersUpdated call won the race.
// WebSocket I/O happens after the state lock is released.
func (n *MarkerUpdateNotifier) SendSessionSnapshotFromLoader(
	ctx context.Context,
	registration *RealtimeRegistration,
	fileID int,
	loader MarkerSnapshotFileLoader,
) (bool, error) {
	if n == nil || n.hub == nil || registration == nil || registration.SessionID() == "" || fileID <= 0 || loader == nil {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !n.hub.HasRegistration(registration) {
		return false, nil
	}

	state := n.acquireFileState(fileID)
	defer n.releaseFileState(fileID, state)
	if !state.lockContext(ctx, registration.Done()) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		state.unlock()
		return false, err
	}
	if !n.hub.HasRegistration(registration) {
		state.unlock()
		return false, nil
	}
	file, err := loader.GetByID(ctx, fileID)
	if err != nil {
		state.unlock()
		return false, err
	}
	epoch := state.epoch.Load()
	payload := markerSnapshotPayloadFromFile(file)
	state.unlock()
	if file == nil || !payload.hasAnyMarker() {
		return false, nil
	}
	event, err := payload.event(registration.SessionID())
	if err != nil {
		return false, err
	}
	sent, err := n.hub.sendRegisteredIf(registration, event, func() bool {
		return state.epoch.Load() == epoch
	})
	if errors.Is(err, ErrRealtimeConnectionNotFound) {
		return false, nil
	}
	return sent, err
}

func (n *MarkerUpdateNotifier) acquireFileState(fileID int) *markerFileState {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	state := n.states[fileID]
	if state == nil {
		state = newMarkerFileState()
		n.states[fileID] = state
	}
	state.refs++
	return state
}

func (n *MarkerUpdateNotifier) releaseFileState(fileID int, state *markerFileState) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if current := n.states[fileID]; current != state {
		return
	}
	state.refs--
	if state.refs == 0 {
		delete(n.states, fileID)
	}
}

func (n *MarkerUpdateNotifier) sendCurrentEpochToSession(
	ctx context.Context,
	state *markerFileState,
	epoch uint64,
	sessionID string,
	payload markerSnapshotPayload,
) {
	event, err := payload.event(sessionID)
	if err != nil {
		slog.WarnContext(ctx,
			"failed to encode markers updated realtime event", "component", "playback",
			"session_id", sessionID,
			"file_id", payload.fileID,
			"error", err,
		)
		return
	}
	_, err = n.hub.sendIf(sessionID, event, func() bool {
		return state.epoch.Load() == epoch
	})
	if err != nil && !errors.Is(err, ErrRealtimeConnectionNotFound) {
		slog.WarnContext(ctx,
			"failed to deliver markers updated realtime event", "component", "playback",
			"session_id", sessionID,
			"file_id", payload.fileID,
			"error", err,
		)
	}
}

func markerSnapshotPayloadFromFile(file *models.MediaFile) markerSnapshotPayload {
	if file == nil {
		return markerSnapshotPayload{}
	}
	rangePayload := func(start, end *float64) *TimeRangePayload {
		if start == nil || end == nil {
			return nil
		}
		return &TimeRangePayload{Start: *start, End: *end}
	}
	return markerSnapshotPayload{
		fileID:  file.ID,
		intro:   rangePayload(file.IntroStart, file.IntroEnd),
		credits: rangePayload(file.CreditsStart, file.CreditsEnd),
		recap:   rangePayload(file.RecapStart, file.RecapEnd),
		preview: rangePayload(file.PreviewStart, file.PreviewEnd),
	}
}

func (p markerSnapshotPayload) hasAnyMarker() bool {
	return p.intro != nil || p.credits != nil || p.recap != nil || p.preview != nil
}

func (p markerSnapshotPayload) event(sessionID string) (EventEnvelope, error) {
	return NewMarkersUpdatedEvent(sessionID, p.fileID, p.intro, p.credits, p.recap, p.preview)
}
