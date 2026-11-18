package abs

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakePlaybackSessionStore is an in-memory ABSPlaybackSessionStore for the
// public-track tests. Only Get is exercised; the other methods are no-ops.
type fakePlaybackSessionStore struct {
	sessions map[string]ABSPlaybackSession
	reported map[string]string // client session id → open reported play id
}

func (f *fakePlaybackSessionStore) InsertPlaybackSession(_ context.Context, s ABSPlaybackSession) error {
	if f.sessions == nil {
		f.sessions = map[string]ABSPlaybackSession{}
	}
	f.sessions[s.ID] = s
	return nil
}
func (f *fakePlaybackSessionStore) GetPlaybackSession(_ context.Context, id string) (ABSPlaybackSession, error) {
	s, ok := f.sessions[id]
	if !ok {
		return ABSPlaybackSession{}, ErrNotFound
	}
	return s, nil
}
func (f *fakePlaybackSessionStore) SyncPlaybackSession(context.Context, string, float64, int) error {
	return nil
}
func (f *fakePlaybackSessionStore) ClosePlaybackSession(_ context.Context, id string, _ *FinalSync) (bool, error) {
	s, ok := f.sessions[id]
	if !ok || s.ClosedAt != nil {
		return false, nil
	}
	now := time.Now()
	s.ClosedAt = &now
	f.sessions[id] = s
	return true, nil
}
func (f *fakePlaybackSessionStore) CloseOpenSessionsForPrincipal(context.Context, string, string) error {
	return nil
}

func (f *fakePlaybackSessionStore) AggregateStats(_ context.Context, userID, profileID string) (Stats, error) {
	return Stats{Days: []DayStat{}, Monthly: []MonthStat{}}, nil
}

func (f *fakePlaybackSessionStore) ListClosedSessions(_ context.Context, userID, profileID string, limit, offset int) ([]ABSPlaybackSession, int, error) {
	return nil, 0, nil
}

// UpdateReportedPlay and StartReportedPlay keep one open reported play per
// client session; a play counts as announced from its second report.
func (f *fakePlaybackSessionStore) UpdateReportedPlay(_ context.Context, p ReportedPlay) (ReportedPlayState, bool, error) {
	id, ok := f.reported[p.ClientSessionID]
	if !ok || f.sessions[id].ClosedAt != nil {
		return ReportedPlayState{}, false, nil
	}
	s := f.sessions[id]
	s.CurrentPositionSeconds = p.PositionSeconds
	f.sessions[id] = s
	return ReportedPlayState{ID: id, StartedAt: s.StartedAt, Announced: true}, true, nil
}

func (f *fakePlaybackSessionStore) StartReportedPlay(_ context.Context, p ReportedPlay, newID string, _ int) (ReportedPlayState, error) {
	if f.sessions == nil {
		f.sessions = map[string]ABSPlaybackSession{}
	}
	if f.reported == nil {
		f.reported = map[string]string{}
	}
	f.reported[p.ClientSessionID] = newID
	f.sessions[newID] = ABSPlaybackSession{ID: newID, UserID: p.UserID, ProfileID: p.ProfileID, ContentID: p.ContentID,
		MediaFileID: p.MediaFileID, CurrentPositionSeconds: p.PositionSeconds, StartedAt: time.Now(), ReportOnly: true}
	return ReportedPlayState{ID: newID, StartedAt: f.sessions[newID].StartedAt}, nil
}

// filesMediaStore returns a fixed slice of MediaFile entries for the
// configured contentID, satisfying the MediaStore interface for the
// public-track tests. Unconfigured methods inherit no-op behavior from
// noopMediaStore via embedding.
type filesMediaStore struct {
	noopMediaStore
	contentID string
	files     []*models.MediaFile
}

func (f *filesMediaStore) GetMediaFiles(_ context.Context, contentID string, _ catalog.AccessFilter) ([]*models.MediaFile, error) {
	if contentID != f.contentID {
		return nil, nil
	}
	return f.files, nil
}
