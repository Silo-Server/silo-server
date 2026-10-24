package watchsync

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

const (
	testProviderAccountID    = "account-1"
	testAccessToken          = "access"
	testBearerTokenType      = "Bearer"
	testCursorOne            = "cursor-1"
	testDPoPTokenType        = "DPoP"
	testHistoryExportID      = "export-1"
	testHistoryScope         = "history"
	testMovieMediaID         = "movie-1"
	testMovieProviderItemKey = "movie:tmdb:603"
	testOldAccessToken       = "old-access"
	testOldRefreshToken      = "old-refresh"
	testOneValue             = "one"
	testPluginUsername       = "alice"
	testRefreshToken         = "refresh"
	testReconnectRequired    = "reconnect required"
	testRotatedAccessToken   = "rotated-access"
	testSecretValue          = "secret"
	testValidatedToken       = "validated-token"
)

type serviceFakeRepo struct {
	connections            map[string]Connection
	dueConnections         []Connection
	sessions               map[string]DeviceAuthSession
	settings               map[string]string
	syncRuns               []SyncRun
	historyExports         []HistoryExport
	historyLookupIDs       []string
	historyLookupLimit     int
	historyPageSizes       []int
	listItemStates         []ListItemState
	ratingStates           []RatingSyncState
	droppedStates          []DroppedSyncState
	listMedia              map[string]LocalFavorite
	mediaTitles            map[string]MediaTitles
	mediaTitlesRelease     chan struct{} // when set, title lookups stall until it closes
	mediaTitleLookups      [][]string
	scrobbleConnections    []Connection
	scrobbleSessions       []ScrobbleSession
	pendingReconciliations []ScrobbleSession
	reconciledScrobbles    map[string]time.Time
	scrobbleUpdates        []scrobbleUpdate
	reopenedScrobbles      []scrobbleUpdate
	confirmedScrobbles     map[string]bool
	confirmingScrobbles    map[string]time.Time
	markSatisfiedErr       error
	markLocalRemovedErr    error
	markHistoryStatusErr   error
	syncRunMu              sync.Mutex
	scrobbleMu             sync.Mutex
	// ratingLockBusy simulates another node holding a connection's rating
	// sync lock; ratingLocks records each lock taken, with whether it waited.
	ratingLockBusy map[string]bool
	ratingLocks    []string
	// tokenRefreshMu gives the token refresh lock its one-holder-at-a-time
	// behavior. onTokenRefreshWait runs as a caller starts waiting for the
	// lock; beforeTokenRefresh stands in for another holder that finishes just
	// before this caller acquires it.
	tokenRefreshMu     sync.Mutex
	onTokenRefreshWait func()
	beforeTokenRefresh func()
	// upsertRatingErr fails UpsertRatingSyncStates when set.
	upsertRatingErr error
}

type scrobbleUpdate struct {
	playbackSessionID string
	connectionID      string
	action            string
	positionSeconds   float64
	historyID         string
	lastError         string
	stopSentAt        *time.Time
}

func newServiceFakeRepo() *serviceFakeRepo {
	return &serviceFakeRepo{
		connections:         make(map[string]Connection),
		sessions:            make(map[string]DeviceAuthSession),
		confirmedScrobbles:  make(map[string]bool),
		confirmingScrobbles: make(map[string]time.Time),
		reconciledScrobbles: make(map[string]time.Time),
		settings: map[string]string{
			"watchsync.trakt.client_id":     "client-id",
			"watchsync.trakt.client_secret": "client-secret",
			"watchsync.simkl.client_id":     "client-id",
			"watchsync.simkl.client_secret": "client-secret",
		},
	}
}

func (r *serviceFakeRepo) GetServerSetting(_ context.Context, key string) (string, error) {
	return r.settings[key], nil
}

func (r *serviceFakeRepo) UpsertAuthSession(
	_ context.Context,
	session DeviceAuthSession,
) (DeviceAuthSession, error) {
	if session.ID == "" {
		session.ID = "auth-1"
	}
	r.sessions[session.ID] = session
	return session, nil
}

func (r *serviceFakeRepo) GetAuthSession(_ context.Context, id string) (DeviceAuthSession, error) {
	session, ok := r.sessions[id]
	if !ok {
		return DeviceAuthSession{}, errors.New("missing auth session")
	}
	return session, nil
}

func (r *serviceFakeRepo) UpsertConnection(
	_ context.Context,
	conn Connection,
) (Connection, error) {
	if conn.ID == "" {
		conn.ID = "conn-1"
	}
	conn = cloneConnectionForTest(conn)
	r.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)] = conn
	return cloneConnectionForTest(conn), nil
}

func (r *serviceFakeRepo) GetConnection(
	_ context.Context,
	provider string,
	userID int,
	profileID string,
) (Connection, bool, error) {
	conn, ok := r.connections[connectionKey(provider, userID, profileID)]
	return cloneConnectionForTest(conn), ok, nil
}

func (r *serviceFakeRepo) UpdateConnectionTokens(ctx context.Context, expected, updated Connection) (Connection, error) {
	current, ok, err := r.GetConnectionByID(ctx, expected.ID)
	if err != nil {
		return Connection{}, err
	}
	if !ok {
		return Connection{}, ErrConnectionNotFound
	}
	if !connectionCredentialsMatch(current, expected) {
		return Connection{}, ErrStaleConnection
	}
	current = connectionWithTokens(current, storedTokens(updated))
	if updated.LastError != expected.LastError {
		current.LastError = updated.LastError
	}
	return r.UpsertConnection(ctx, current)
}

func (r *serviceFakeRepo) UpdateConnectionSyncState(_ context.Context, id string, update ConnectionSyncUpdate) error {
	if id == "" {
		return nil
	}
	for key, conn := range r.connections {
		if conn.ID == id {
			r.connections[key] = applyConnectionSyncUpdateForTest(conn, update)
		}
	}
	for i, conn := range r.scrobbleConnections {
		if conn.ID == id {
			r.scrobbleConnections[i] = applyConnectionSyncUpdateForTest(conn, update)
		}
	}
	return nil
}

// applyConnectionSyncUpdateForTest mirrors UpdateConnectionSyncState's SQL.
func applyConnectionSyncUpdateForTest(conn Connection, update ConnectionSyncUpdate) Connection {
	conn = cloneConnectionForTest(conn)
	for _, field := range []struct {
		dst **time.Time
		src *time.Time
	}{
		{&conn.LastInboundSyncAt, update.LastInboundSyncAt},
		{&conn.LastProgressSyncAt, update.LastProgressSyncAt},
		{&conn.LastOutboundSyncAt, update.LastOutboundSyncAt},
		{&conn.LastFavoritesSyncAt, update.LastFavoritesSyncAt},
		{&conn.LastWatchlistSyncAt, update.LastWatchlistSyncAt},
	} {
		if field.src != nil {
			at := *field.src
			*field.dst = &at
		}
	}
	deferred := update.ClearRateLimitBy != nil && conn.RateLimitedUntil != nil &&
		conn.RateLimitedUntil.After(*update.ClearRateLimitBy)
	if update.LastError != nil && (*update.LastError != "" || !deferred) {
		conn.LastError = *update.LastError
	}
	switch {
	case update.RateLimitedUntil != nil:
		until := *update.RateLimitedUntil
		conn.RateLimitedUntil = &until
	case update.ClearRateLimitBy != nil && conn.RateLimitedUntil != nil &&
		!conn.RateLimitedUntil.After(*update.ClearRateLimitBy):
		conn.RateLimitedUntil = nil
	}
	conn.SyncCursors = mergeSyncCursors(conn.SyncCursors, update.SyncCursors)
	return conn
}

func (r *serviceFakeRepo) DeferConnectionsForAccount(
	_ context.Context,
	provider string,
	providerAccountID string,
	until time.Time,
	lastError string,
) (int, error) {
	if providerAccountID == "" {
		return 0, nil
	}
	deferred := 0
	for key, conn := range r.connections {
		if conn.Provider != provider || conn.ProviderAccountID != providerAccountID {
			continue
		}
		untilCopy := until
		conn.RateLimitedUntil = &untilCopy
		conn.LastError = lastError
		r.connections[key] = conn
		deferred++
	}
	return deferred, nil
}

// withConnection stores conn the way a connected profile's row exists before
// a sync, and returns it.
func (r *serviceFakeRepo) withConnection(conn Connection) Connection {
	r.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)] = conn
	return conn
}

func (r *serviceFakeRepo) GetConnectionByID(_ context.Context, id string) (Connection, bool, error) {
	for _, conn := range r.connections {
		if conn.ID == id {
			return cloneConnectionForTest(conn), true, nil
		}
	}
	for _, conn := range r.scrobbleConnections {
		if conn.ID == id {
			return cloneConnectionForTest(conn), true, nil
		}
	}
	return Connection{}, false, nil
}

func (r *serviceFakeRepo) DeleteConnection(
	_ context.Context,
	provider string,
	userID int,
	profileID string,
) error {
	delete(r.connections, connectionKey(provider, userID, profileID))
	return nil
}

func (r *serviceFakeRepo) ListConnectionsDueForSync(
	_ context.Context,
	_ time.Time,
) ([]Connection, error) {
	out := make([]Connection, 0, len(r.dueConnections))
	for _, conn := range r.dueConnections {
		out = append(out, cloneConnectionForTest(conn))
	}
	return out, nil
}

func (r *serviceFakeRepo) CreateSyncRun(_ context.Context, run SyncRun) (SyncRun, error) {
	r.syncRunMu.Lock()
	defer r.syncRunMu.Unlock()
	if run.ID == "" {
		run.ID = "run-" + strconv.Itoa(len(r.syncRuns)+1)
	}
	if run.Status == "" {
		run.Status = string(SyncRunStatusRunning)
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = run.StartedAt
	}
	r.syncRuns = append(r.syncRuns, run)
	return run, nil
}

func (r *serviceFakeRepo) CompleteSyncRun(_ context.Context, run SyncRun) (SyncRun, error) {
	r.syncRunMu.Lock()
	defer r.syncRunMu.Unlock()
	for i := range r.syncRuns {
		if r.syncRuns[i].ID == run.ID {
			if run.CreatedAt.IsZero() {
				run.CreatedAt = r.syncRuns[i].CreatedAt
			}
			if run.StartedAt.IsZero() {
				run.StartedAt = r.syncRuns[i].StartedAt
			}
			r.syncRuns[i] = run
			return run, nil
		}
	}
	return SyncRun{}, errors.New("missing sync run")
}

func (r *serviceFakeRepo) GetLatestSyncRun(_ context.Context, connectionID string) (SyncRun, bool, error) {
	r.syncRunMu.Lock()
	defer r.syncRunMu.Unlock()
	for i := len(r.syncRuns) - 1; i >= 0; i-- {
		if r.syncRuns[i].ConnectionID == connectionID {
			return r.syncRuns[i], true, nil
		}
	}
	return SyncRun{}, false, nil
}

func (r *serviceFakeRepo) GetActiveSyncRun(_ context.Context, connectionID string) (SyncRun, bool, error) {
	r.syncRunMu.Lock()
	defer r.syncRunMu.Unlock()
	for i := len(r.syncRuns) - 1; i >= 0; i-- {
		run := r.syncRuns[i]
		if run.ConnectionID == connectionID &&
			(run.Status == string(SyncRunStatusQueued) || run.Status == string(SyncRunStatusRunning)) {
			return run, true, nil
		}
	}
	return SyncRun{}, false, nil
}

func (r *serviceFakeRepo) ListSyncRuns(_ context.Context, connectionID string, limit int) ([]SyncRun, error) {
	r.syncRunMu.Lock()
	defer r.syncRunMu.Unlock()
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	var runs []SyncRun
	for i := len(r.syncRuns) - 1; i >= 0 && len(runs) < limit; i-- {
		if r.syncRuns[i].ConnectionID == connectionID {
			runs = append(runs, r.syncRuns[i])
		}
	}
	return runs, nil
}

func (r *serviceFakeRepo) ListLocalWatchEventConnections(_ context.Context, userID int, profileID string, kind LocalWatchEventKind) ([]Connection, error) {
	var conns []Connection
	for _, conn := range r.connections {
		if conn.UserID != userID || conn.ProfileID != profileID {
			continue
		}
		if conn.RateLimitedUntil != nil && conn.RateLimitedUntil.After(time.Now()) {
			continue
		}
		if kind == LocalWatchEventMarkedWatched && conn.ExportWatchedEnabled {
			conns = append(conns, cloneConnectionForTest(conn))
		}
	}
	return conns, nil
}

func (r *serviceFakeRepo) ListListEventConnections(_ context.Context, userID int, profileID string, list ListKind) ([]Connection, error) {
	var conns []Connection
	for _, conn := range r.connections {
		if conn.UserID != userID || conn.ProfileID != profileID {
			continue
		}
		enabled := conn.ExportFavoritesEnabled
		if list == ListKindWatchlist {
			enabled = conn.ExportWatchlistEnabled
		}
		if enabled {
			conns = append(conns, cloneConnectionForTest(conn))
		}
	}
	return conns, nil
}

func (r *serviceFakeRepo) UpsertHistoryExports(_ context.Context, exports []HistoryExport) error {
	for _, export := range exports {
		if export.ID == "" {
			export.ID = "export-" + strconv.Itoa(len(r.historyExports)+1)
		}
		replaced := false
		for i := range r.historyExports {
			if r.historyExports[i].HistoryID == export.HistoryID && r.historyExports[i].ConnectionID == export.ConnectionID {
				existing := r.historyExports[i]
				export.ID = existing.ID
				export.AttemptCount = existing.AttemptCount
				if existing.Status == historyExportStatusSent || existing.Status == historyExportStatusSatisfiedByScrobble || existing.Status == historyExportStatusNotFound || existing.AttemptCount >= 5 {
					export.Status = existing.Status
				}
				r.historyExports[i] = export
				replaced = true
				break
			}
		}
		if !replaced {
			r.historyExports = append(r.historyExports, export)
		}
	}
	return nil
}

func (r *serviceFakeRepo) ListPendingHistoryExports(_ context.Context, connectionID string, after HistoryExportCursor, limit int) ([]HistoryExport, error) {
	r.historyPageSizes = append(r.historyPageSizes, limit)
	var exports []HistoryExport
	for _, export := range r.historyExports {
		if export.ConnectionID == connectionID &&
			(export.Status == historyExportStatusPending || export.Status == historyExportStatusFailed) && export.AttemptCount < 5 &&
			(after.ID == "" || export.WatchedAt.After(after.WatchedAt) ||
				(export.WatchedAt.Equal(after.WatchedAt) && export.ID > after.ID)) {
			exports = append(exports, export)
		}
	}
	slices.SortStableFunc(exports, func(a, b HistoryExport) int {
		if c := a.WatchedAt.Compare(b.WatchedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	if limit > 0 && len(exports) > limit {
		exports = exports[:limit]
	}
	return exports, nil
}

func (r *serviceFakeRepo) ListPendingHistoryExportsByHistoryIDs(_ context.Context, connectionID string, historyIDs []string, limit int) ([]HistoryExport, error) {
	r.historyLookupIDs = append([]string(nil), historyIDs...)
	r.historyLookupLimit = limit
	wanted := make(map[string]struct{}, len(historyIDs))
	for _, historyID := range historyIDs {
		wanted[historyID] = struct{}{}
	}
	var exports []HistoryExport
	for _, export := range r.historyExports {
		_, matches := wanted[export.HistoryID]
		if export.ConnectionID == connectionID && matches &&
			(export.Status == historyExportStatusPending || export.Status == historyExportStatusFailed) && export.AttemptCount < 5 {
			exports = append(exports, export)
			if limit > 0 && len(exports) >= limit {
				break
			}
		}
	}
	return exports, nil
}

func (r *serviceFakeRepo) MarkHistoryExportStatus(_ context.Context, id string, status string, lastError string) error {
	if r.markHistoryStatusErr != nil {
		return r.markHistoryStatusErr
	}
	for i := range r.historyExports {
		if r.historyExports[i].ID == id {
			if r.historyExports[i].Status == historyExportStatusSent || r.historyExports[i].Status == historyExportStatusSatisfiedByScrobble || r.historyExports[i].Status == historyExportStatusNotFound {
				return nil
			}
			r.historyExports[i].Status = status
			r.historyExports[i].AttemptCount++
			r.historyExports[i].LastError = lastError
			return nil
		}
	}
	return nil
}

func (r *serviceFakeRepo) MarkHistoryExportSatisfiedByScrobble(_ context.Context, connectionID string, historyID string) error {
	if r.markSatisfiedErr != nil {
		return r.markSatisfiedErr
	}
	for i := range r.historyExports {
		if r.historyExports[i].ConnectionID == connectionID && r.historyExports[i].HistoryID == historyID {
			if r.historyExports[i].Status == historyExportStatusSent || r.historyExports[i].Status == historyExportStatusNotFound {
				return nil
			}
			r.historyExports[i].Status = historyExportStatusSatisfiedByScrobble
			return nil
		}
	}
	return nil
}

func (r *serviceFakeRepo) UpsertListItemStates(_ context.Context, states []ListItemState) error {
	for _, state := range states {
		if state.ListKind == "" {
			state.ListKind = ListKindFavorites
		}
		replaced := false
		for i := range r.listItemStates {
			if r.listItemStates[i].ConnectionID == state.ConnectionID &&
				r.listItemStates[i].ListKind == state.ListKind &&
				r.listItemStates[i].MediaItemID == state.MediaItemID {
				if state.ID == "" {
					state.ID = r.listItemStates[i].ID
				}
				r.listItemStates[i] = state
				replaced = true
				break
			}
		}
		if !replaced {
			if state.ID == "" {
				state.ID = "list-item-" + strconv.Itoa(len(r.listItemStates)+1)
			}
			r.listItemStates = append(r.listItemStates, state)
		}
	}
	return nil
}

func (r *serviceFakeRepo) ListListItemStates(_ context.Context, connectionID string, kind ListKind) ([]ListItemState, error) {
	var states []ListItemState
	for _, state := range r.listItemStates {
		if state.ConnectionID == connectionID && state.ListKind == kind {
			states = append(states, state)
		}
	}
	return states, nil
}

func (r *serviceFakeRepo) ListPendingListItemExports(_ context.Context, connectionID string, kind ListKind, limit int) ([]ListItemState, error) {
	var states []ListItemState
	for _, state := range r.listItemStates {
		if state.ConnectionID == connectionID && state.ListKind == kind && state.LocalPresent && !state.RemotePresent && state.LastError == "" {
			states = append(states, state)
			if limit > 0 && len(states) >= limit {
				break
			}
		}
	}
	return states, nil
}

func (r *serviceFakeRepo) ListPendingListItemRemovals(_ context.Context, connectionID string, kind ListKind, limit int) ([]ListItemState, error) {
	var states []ListItemState
	for _, state := range r.listItemStates {
		if state.ConnectionID == connectionID && state.ListKind == kind && !state.LocalPresent && state.RemotePresent && state.LastError == "" &&
			!r.listItemKeyHeldElsewhere(state) {
			states = append(states, state)
			if limit > 0 && len(states) >= limit {
				break
			}
		}
	}
	return states, nil
}

func (r *serviceFakeRepo) ListListItemsWithSharedKeyHeld(_ context.Context, connectionID string, kind ListKind, mediaItemIDs []string) (map[string]bool, error) {
	held := make(map[string]bool)
	for _, state := range r.listItemStates {
		if state.ConnectionID == connectionID && state.ListKind == kind && slices.Contains(mediaItemIDs, state.MediaItemID) && r.listItemKeyHeldElsewhere(state) {
			held[state.MediaItemID] = true
		}
	}
	return held, nil
}

func (r *serviceFakeRepo) listItemKeyHeldElsewhere(state ListItemState) bool {
	if state.ProviderItemKey == "" {
		return false
	}
	for _, other := range r.listItemStates {
		if other.ConnectionID == state.ConnectionID && other.ListKind == state.ListKind &&
			other.ProviderItemKey == state.ProviderItemKey && other.Kind == state.Kind &&
			other.MediaItemID != state.MediaItemID && other.LocalPresent {
			return true
		}
	}
	return false
}

func (r *serviceFakeRepo) markListItem(connectionID string, kind ListKind, mediaItemID string, apply func(*ListItemState)) {
	for i := range r.listItemStates {
		if r.listItemStates[i].ConnectionID == connectionID && r.listItemStates[i].ListKind == kind && r.listItemStates[i].MediaItemID == mediaItemID {
			apply(&r.listItemStates[i])
		}
	}
}

func (r *serviceFakeRepo) MarkListItemExported(_ context.Context, connectionID string, kind ListKind, mediaItemID string, exportedAt time.Time) error {
	// Mirror Postgres: successful transitions clear last_error.
	r.markListItem(connectionID, kind, mediaItemID, func(s *ListItemState) {
		s.RemotePresent = true
		s.LocalPresent = true
		s.LastExportedAt = &exportedAt
		s.LastError = ""
	})
	return nil
}

func (r *serviceFakeRepo) MarkListItemRemoteRemoved(_ context.Context, connectionID string, kind ListKind, mediaItemID string, removedAt time.Time) error {
	r.markListItem(connectionID, kind, mediaItemID, func(s *ListItemState) {
		s.RemotePresent = false
		s.LastRemovedRemoteAt = &removedAt
		s.LastError = ""
	})
	return nil
}

func (r *serviceFakeRepo) MarkListItemLocalRemoved(_ context.Context, connectionID string, kind ListKind, mediaItemID string, removedAt time.Time) error {
	if r.markLocalRemovedErr != nil {
		return r.markLocalRemovedErr
	}
	r.markListItem(connectionID, kind, mediaItemID, func(s *ListItemState) {
		s.LocalPresent = false
		s.LastRemovedLocalAt = &removedAt
		s.LastError = ""
	})
	return nil
}

func (r *serviceFakeRepo) MarkListItemError(_ context.Context, connectionID string, kind ListKind, mediaItemID, lastError string) error {
	r.markListItem(connectionID, kind, mediaItemID, func(s *ListItemState) {
		s.LastError = lastError
	})
	return nil
}

func (r *serviceFakeRepo) ListRatingEventConnections(_ context.Context, userID int, profileID string) ([]Connection, error) {
	var conns []Connection
	for _, conn := range r.connections {
		if conn.UserID == userID && conn.ProfileID == profileID && conn.ExportRatingsEnabled {
			conns = append(conns, cloneConnectionForTest(conn))
		}
	}
	return conns, nil
}

func (r *serviceFakeRepo) ListRatingSyncStates(_ context.Context, connectionID, providerAccountID string, mediaItemIDs []string) ([]RatingSyncState, error) {
	var states []RatingSyncState
	for _, state := range r.ratingStates {
		if state.ConnectionID != connectionID || state.ProviderAccountID != providerAccountID {
			continue
		}
		if mediaItemIDs != nil && !containsString(mediaItemIDs, state.MediaItemID) {
			continue
		}
		states = append(states, state)
	}
	return states, nil
}

func (r *serviceFakeRepo) UpsertRatingSyncStates(_ context.Context, states []RatingSyncState) error {
	if r.upsertRatingErr != nil && len(states) > 0 {
		return r.upsertRatingErr
	}
	for _, state := range states {
		replaced := false
		for i := range r.ratingStates {
			existing := &r.ratingStates[i]
			if existing.ConnectionID == state.ConnectionID && existing.MediaItemID == state.MediaItemID {
				if state.Kind == "" {
					state.Kind = existing.Kind
				}
				if state.ProviderItemKey == "" {
					state.ProviderItemKey = existing.ProviderItemKey
				}
				*existing = state
				replaced = true
				break
			}
		}
		if !replaced {
			r.ratingStates = append(r.ratingStates, state)
		}
	}
	return nil
}

func (r *serviceFakeRepo) WithRatingSyncLock(ctx context.Context, connectionID string, wait bool, fn func(context.Context) error) (bool, error) {
	if r.ratingLockBusy[connectionID] {
		if wait {
			return false, context.DeadlineExceeded
		}
		return false, nil
	}
	mode := "try"
	if wait {
		mode = "wait"
	}
	r.ratingLocks = append(r.ratingLocks, mode+":"+connectionID)
	return true, fn(ctx)
}

func (r *serviceFakeRepo) WithTokenRefreshLock(ctx context.Context, _ string, fn func(context.Context) error) error {
	if r.onTokenRefreshWait != nil {
		r.onTokenRefreshWait()
	}
	r.tokenRefreshMu.Lock()
	defer r.tokenRefreshMu.Unlock()
	if r.beforeTokenRefresh != nil {
		r.beforeTokenRefresh()
	}
	return fn(ctx)
}

func (r *serviceFakeRepo) DeleteRatingSyncStates(_ context.Context, connectionID, providerAccountID string, mediaItemIDs []string) error {
	kept := r.ratingStates[:0]
	for _, state := range r.ratingStates {
		if state.ConnectionID == connectionID && state.ProviderAccountID == providerAccountID && containsString(mediaItemIDs, state.MediaItemID) {
			continue
		}
		kept = append(kept, state)
	}
	r.ratingStates = kept
	return nil
}

func (r *serviceFakeRepo) ClearRatingSyncStates(_ context.Context, connectionID, keepAccountID string) error {
	kept := r.ratingStates[:0]
	for _, state := range r.ratingStates {
		if state.ConnectionID != connectionID || state.ProviderAccountID == keepAccountID {
			kept = append(kept, state)
		}
	}
	r.ratingStates = kept
	return nil
}

func (r *serviceFakeRepo) ListDroppedEventConnections(_ context.Context, userID int, profileID string) ([]Connection, error) {
	var conns []Connection
	for _, conn := range r.connections {
		if conn.UserID == userID && conn.ProfileID == profileID && conn.SyncDroppedEnabled {
			conns = append(conns, cloneConnectionForTest(conn))
		}
	}
	return conns, nil
}

func (r *serviceFakeRepo) ListDroppedSyncStates(_ context.Context, connectionID, providerAccountID string, seriesIDs []string) ([]DroppedSyncState, error) {
	var states []DroppedSyncState
	for _, state := range r.droppedStates {
		if state.ConnectionID != connectionID || state.ProviderAccountID != providerAccountID {
			continue
		}
		if seriesIDs != nil && !containsString(seriesIDs, state.SeriesID) {
			continue
		}
		states = append(states, state)
	}
	return states, nil
}

func (r *serviceFakeRepo) UpsertDroppedSyncStates(_ context.Context, states []DroppedSyncState) error {
	for _, state := range states {
		state.UpdatedAt = time.Now()
		replaced := false
		for i := range r.droppedStates {
			existing := &r.droppedStates[i]
			if existing.ConnectionID == state.ConnectionID && existing.SeriesID == state.SeriesID {
				if state.ProviderItemKey == "" {
					state.ProviderItemKey = existing.ProviderItemKey
				}
				*existing = state
				replaced = true
				break
			}
		}
		if !replaced {
			r.droppedStates = append(r.droppedStates, state)
		}
	}
	return nil
}

func (r *serviceFakeRepo) DeleteDroppedSyncStates(_ context.Context, connectionID, providerAccountID string, seriesIDs []string) error {
	kept := r.droppedStates[:0]
	for _, state := range r.droppedStates {
		if state.ConnectionID == connectionID && state.ProviderAccountID == providerAccountID && containsString(seriesIDs, state.SeriesID) {
			continue
		}
		kept = append(kept, state)
	}
	r.droppedStates = kept
	return nil
}

func (r *serviceFakeRepo) ClearDroppedSyncStates(_ context.Context, connectionID, keepAccountID string) error {
	kept := r.droppedStates[:0]
	for _, state := range r.droppedStates {
		if state.ConnectionID != connectionID || state.ProviderAccountID == keepAccountID {
			kept = append(kept, state)
		}
	}
	r.droppedStates = kept
	return nil
}

func (r *serviceFakeRepo) UpdateRatingCursors(_ context.Context, connectionID, providerAccountID string, remove []string, set map[string]string) error {
	for key, conn := range r.connections {
		if conn.ID != connectionID || conn.ProviderAccountID != providerAccountID {
			continue
		}
		cursors := cloneStringMapForTest(conn.SyncCursors)
		for _, k := range remove {
			delete(cursors, k)
		}
		for k, v := range set {
			cursors[k] = v
		}
		conn.SyncCursors = cursors
		r.connections[key] = conn
	}
	return nil
}

func (r *serviceFakeRepo) GetListMediaItems(_ context.Context, mediaItemIDs []string) (map[string]LocalFavorite, error) {
	result := make(map[string]LocalFavorite, len(mediaItemIDs))
	for _, id := range mediaItemIDs {
		if item, ok := r.listMedia[id]; ok {
			result[id] = item
		}
	}
	return result, nil
}

func (r *serviceFakeRepo) GetMediaTitles(ctx context.Context, mediaItemIDs []string) (map[string]MediaTitles, error) {
	r.scrobbleMu.Lock()
	r.mediaTitleLookups = append(r.mediaTitleLookups, append([]string(nil), mediaItemIDs...))
	r.scrobbleMu.Unlock()
	if r.mediaTitlesRelease != nil {
		select {
		case <-r.mediaTitlesRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	result := make(map[string]MediaTitles, len(mediaItemIDs))
	for _, id := range mediaItemIDs {
		if titles, ok := r.mediaTitles[id]; ok {
			result[id] = titles
		}
	}
	return result, nil
}

func (r *serviceFakeRepo) ListScrobbleConnections(_ context.Context, _ int, _ string) ([]Connection, error) {
	conns := make([]Connection, 0, len(r.scrobbleConnections))
	for _, conn := range r.scrobbleConnections {
		if conn.RateLimitedUntil != nil && conn.RateLimitedUntil.After(time.Now()) {
			continue
		}
		conns = append(conns, cloneConnectionForTest(conn))
	}
	return conns, nil
}

func (r *serviceFakeRepo) UpsertScrobbleSession(_ context.Context, _ ScrobbleEvent, _ string, _ string) error {
	return nil
}

func (r *serviceFakeRepo) PrepareConfirmedScrobbleStop(_ context.Context, event ScrobbleEvent, connectionID string, _ time.Time) (confirmedStopPreparation, time.Time, error) {
	r.scrobbleMu.Lock()
	defer r.scrobbleMu.Unlock()
	key := event.PlaybackSessionID + "|" + connectionID
	if r.confirmedScrobbles[key] {
		return confirmedStopAlreadySent, time.Time{}, nil
	}
	if !r.confirmingScrobbles[key].IsZero() {
		return confirmedStopInProgress, time.Time{}, nil
	}
	claimVersion := time.Now()
	r.confirmingScrobbles[key] = claimVersion
	r.reopenedScrobbles = append(r.reopenedScrobbles, scrobbleUpdate{
		playbackSessionID: event.PlaybackSessionID,
		connectionID:      connectionID,
		action:            "stop",
		positionSeconds:   event.PositionSeconds,
		historyID:         event.HistoryID,
	})
	return confirmedStopPrepared, claimVersion, nil
}

func (r *serviceFakeRepo) CompleteConfirmedScrobbleStop(_ context.Context, playbackSessionID string, connectionID string, positionSeconds float64, historyID string, claimVersion time.Time, stopSentAt time.Time) error {
	r.scrobbleMu.Lock()
	defer r.scrobbleMu.Unlock()
	key := playbackSessionID + "|" + connectionID
	if !r.confirmingScrobbles[key].Equal(claimVersion) {
		return errConfirmedStopClaimLost
	}
	delete(r.confirmingScrobbles, key)
	r.confirmedScrobbles[key] = true
	r.scrobbleUpdates = append(r.scrobbleUpdates, scrobbleUpdate{
		playbackSessionID: playbackSessionID,
		connectionID:      connectionID,
		action:            "stop_confirmed",
		positionSeconds:   positionSeconds,
		historyID:         historyID,
		stopSentAt:        &stopSentAt,
	})
	if historyID != "" {
		r.pendingReconciliations = append(r.pendingReconciliations, ScrobbleSession{
			PlaybackSessionID: playbackSessionID, ConnectionID: connectionID, HistoryID: historyID,
		})
	}
	return nil
}

func (r *serviceFakeRepo) FailConfirmedScrobbleStop(_ context.Context, playbackSessionID string, connectionID string, positionSeconds float64, historyID string, claimVersion time.Time, lastError string) error {
	r.scrobbleMu.Lock()
	defer r.scrobbleMu.Unlock()
	key := playbackSessionID + "|" + connectionID
	if !r.confirmingScrobbles[key].Equal(claimVersion) {
		return errConfirmedStopClaimLost
	}
	delete(r.confirmingScrobbles, key)
	r.scrobbleUpdates = append(r.scrobbleUpdates, scrobbleUpdate{
		playbackSessionID: playbackSessionID,
		connectionID:      connectionID,
		action:            "stop_confirming",
		positionSeconds:   positionSeconds,
		historyID:         historyID,
		lastError:         lastError,
	})
	return nil
}

func (r *serviceFakeRepo) UpdateScrobbleSession(ctx context.Context, playbackSessionID string, connectionID string, action string, positionSeconds float64, historyID string, lastError string, stopSentAt *time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.scrobbleMu.Lock()
	defer r.scrobbleMu.Unlock()
	r.scrobbleUpdates = append(r.scrobbleUpdates, scrobbleUpdate{
		playbackSessionID: playbackSessionID,
		connectionID:      connectionID,
		action:            action,
		positionSeconds:   positionSeconds,
		historyID:         historyID,
		lastError:         lastError,
		stopSentAt:        stopSentAt,
	})
	if stopSentAt != nil && historyID != "" {
		r.pendingReconciliations = append(r.pendingReconciliations, ScrobbleSession{
			PlaybackSessionID: playbackSessionID, ConnectionID: connectionID, HistoryID: historyID,
		})
	}
	return nil
}

func (r *serviceFakeRepo) ListOpenScrobbleSessions(_ context.Context) ([]ScrobbleSession, error) {
	r.scrobbleMu.Lock()
	defer r.scrobbleMu.Unlock()
	return append([]ScrobbleSession(nil), r.scrobbleSessions...), nil
}

func (r *serviceFakeRepo) ListPendingScrobbleReconciliations(_ context.Context) ([]ScrobbleSession, error) {
	r.scrobbleMu.Lock()
	defer r.scrobbleMu.Unlock()
	return append([]ScrobbleSession(nil), r.pendingReconciliations...), nil
}

func (r *serviceFakeRepo) MarkScrobbleHistoryReconciled(_ context.Context, playbackSessionID, connectionID string, reconciledAt time.Time) error {
	r.scrobbleMu.Lock()
	defer r.scrobbleMu.Unlock()
	key := playbackSessionID + "|" + connectionID
	r.reconciledScrobbles[key] = reconciledAt
	remaining := r.pendingReconciliations[:0]
	for _, session := range r.pendingReconciliations {
		if session.PlaybackSessionID == playbackSessionID && session.ConnectionID == connectionID {
			continue
		}
		remaining = append(remaining, session)
	}
	r.pendingReconciliations = remaining
	return nil
}

func (r *serviceFakeRepo) scrobbleUpdatesSnapshot() []scrobbleUpdate {
	r.scrobbleMu.Lock()
	defer r.scrobbleMu.Unlock()
	return append([]scrobbleUpdate(nil), r.scrobbleUpdates...)
}

func connectionKey(provider string, userID int, profileID string) string {
	return provider + "|" + strconv.Itoa(userID) + "|" + profileID
}

func cloneConnectionForTest(conn Connection) Connection {
	conn.SyncCursors = cloneStringMapForTest(conn.SyncCursors)
	conn.ConnectionSettings = cloneStringMapForTest(conn.ConnectionSettings)
	return conn
}

func cloneStringMapForTest(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

type authProviderStub struct {
	started       bool
	polled        bool
	pollErr       error
	refreshed     bool
	refreshTokens TokenSet
	refreshErr    error
	beforeRefresh func(context.Context, Connection) error
	// refreshEntered, when set, receives once RefreshToken is called, which
	// then waits for refreshRelease: a refresh in flight at the provider.
	refreshEntered chan<- struct{}
	refreshRelease <-chan struct{}
}

func (p *authProviderStub) Key() string {
	return "trakt"
}

func (p *authProviderStub) DisplayName() string {
	return "Trakt"
}

func (p *authProviderStub) Capabilities() Capabilities {
	return Capabilities{}
}

func (p *authProviderStub) StartDeviceAuth(
	context.Context,
	ServerConfig,
) (DeviceAuthSession, error) {
	p.started = true
	return DeviceAuthSession{
		Provider:        "trakt",
		DeviceCode:      "device",
		UserCode:        "CODE",
		VerificationURL: "https://trakt.tv/activate",
		IntervalSeconds: 5,
		ExpiresAt:       time.Now().Add(time.Minute),
	}, nil
}

func (p *authProviderStub) PollDeviceAuth(
	context.Context,
	ServerConfig,
	DeviceAuthSession,
) (TokenSet, error) {
	p.polled = true
	if p.pollErr != nil {
		return TokenSet{}, p.pollErr
	}
	expires := time.Now().Add(time.Hour)
	return TokenSet{AccessToken: testAccessToken, RefreshToken: testRefreshToken, TokenExpiresAt: &expires}, nil
}

func (p *authProviderStub) RefreshToken(ctx context.Context, _ ServerConfig, conn Connection) (TokenSet, error) {
	if p.refreshEntered != nil {
		p.refreshEntered <- struct{}{}
		<-p.refreshRelease
	}
	p.refreshed = true
	if p.beforeRefresh != nil {
		if err := p.beforeRefresh(ctx, conn); err != nil {
			return TokenSet{}, err
		}
	}
	if p.refreshErr != nil {
		return TokenSet{}, p.refreshErr
	}
	return p.refreshTokens, nil
}

func (p *authProviderStub) LookupAccount(
	context.Context,
	ServerConfig,
	Connection,
) (ProviderAccount, error) {
	return ProviderAccount{ID: "trakt-user-1", Username: "alex"}, nil
}

type emptyPluginAPIKeyProvider struct{}

func (emptyPluginAPIKeyProvider) Key() string { return "plugin:1:tracker" }

func (emptyPluginAPIKeyProvider) DisplayName() string { return "Tracker" }

func (emptyPluginAPIKeyProvider) Capabilities() Capabilities { return Capabilities{} }

func (emptyPluginAPIKeyProvider) ProviderSource() string { return providerSourcePlugin }

func (emptyPluginAPIKeyProvider) ConnectWithAPIKey(context.Context, string) (TokenSet, ProviderAccount, error) {
	return TokenSet{}, ProviderAccount{ID: testProviderAccountID}, nil
}

// historyStubBase is the identity the watched-history stubs share: Trakt
// unless key and source say otherwise.
type historyStubBase struct {
	key    string
	source userstore.WatchHistorySource
}

func (p historyStubBase) Key() string {
	if p.key != "" {
		return p.key
	}
	return "trakt"
}

func (historyStubBase) DisplayName() string {
	return "Trakt"
}

func (p historyStubBase) HistorySource() userstore.WatchHistorySource {
	if p.source == "" {
		return userstore.WatchHistorySourceTrakt
	}
	return p.source
}

type watchedImporterStub struct {
	historyStubBase
	rows []RemoteWatch
}

func (p watchedImporterStub) Capabilities() Capabilities {
	return Capabilities{ImportWatched: true}
}

func (p watchedImporterStub) FetchWatched(context.Context, ServerConfig, Connection) ([]RemoteWatch, error) {
	return p.rows, nil
}

type watchedExporterStub struct {
	exportErr    error
	exportResult ExportResult
	exported     *[]LocalPlay
	remote       []RemotePlay
	precision    time.Duration
	onExport     func()
	historyStubBase
}

func (p watchedExporterStub) Capabilities() Capabilities {
	return Capabilities{ExportWatched: true}
}

func (p watchedExporterStub) FetchHistory(context.Context, ServerConfig, Connection) ([]RemotePlay, error) {
	return p.remote, nil
}

func (p watchedExporterStub) HistoryTimePrecision() time.Duration {
	return p.precision
}

func (p watchedExporterStub) ExportHistory(_ context.Context, _ ServerConfig, _ Connection, plays []LocalPlay) (ExportResult, error) {
	if p.exported != nil {
		*p.exported = append(*p.exported, plays...)
	}
	if p.onExport != nil {
		p.onExport()
	}
	return p.exportResult, p.exportErr
}

type watchedImportExportStub struct {
	historyStubBase
	rows      []RemoteWatch
	exportErr error
}

func (p watchedImportExportStub) Capabilities() Capabilities {
	return Capabilities{ImportWatched: true, ExportWatched: true}
}

func (p watchedImportExportStub) FetchWatched(context.Context, ServerConfig, Connection) ([]RemoteWatch, error) {
	return p.rows, nil
}

func (p watchedImportExportStub) FetchHistory(context.Context, ServerConfig, Connection) ([]RemotePlay, error) {
	return nil, nil
}

func (p watchedImportExportStub) ExportHistory(_ context.Context, _ ServerConfig, _ Connection, plays []LocalPlay) (ExportResult, error) {
	if p.exportErr != nil {
		return ExportResult{}, p.exportErr
	}
	result := ExportResult{Sent: make([]string, 0, len(plays))}
	for _, play := range plays {
		result.Sent = append(result.Sent, play.HistoryID)
	}
	return result, nil
}

type scrobblerStub struct {
	stopErr       error
	refreshed     bool
	refreshTokens TokenSet
	refreshErr    error
	stopConns     chan Connection
	stopEvents    chan ScrobbleEvent
	stopStarted   chan struct{}
	stopRelease   chan struct{}
	stopFailures  *atomic.Int32
}

type keyedScrobblerStub struct {
	scrobblerStub
	key string
}

func (p keyedScrobblerStub) Key() string {
	return p.key
}

func (p keyedScrobblerStub) DisplayName() string {
	return p.key
}

func (p scrobblerStub) Key() string {
	return "trakt"
}

func (p scrobblerStub) DisplayName() string {
	return "Trakt"
}

func (p scrobblerStub) Capabilities() Capabilities {
	return Capabilities{ScrobblePlayback: true}
}

type watchedScrobblerStub struct{ scrobblerStub }

func (watchedScrobblerStub) Capabilities() Capabilities {
	return Capabilities{ScrobblePlayback: true, ExportWatched: true}
}

func (p scrobblerStub) Start(context.Context, ServerConfig, Connection, ScrobbleEvent) error {
	return nil
}

func (p scrobblerStub) Pause(context.Context, ServerConfig, Connection, ScrobbleEvent) error {
	return nil
}

func (p scrobblerStub) Stop(_ context.Context, _ ServerConfig, conn Connection, event ScrobbleEvent) error {
	if p.stopStarted != nil {
		p.stopStarted <- struct{}{}
	}
	if p.stopRelease != nil {
		<-p.stopRelease
	}
	if p.stopConns != nil {
		p.stopConns <- conn
	}
	if p.stopEvents != nil {
		p.stopEvents <- event
	}
	if p.stopFailures != nil && p.stopFailures.Add(-1) >= 0 {
		return errors.New("stop failed")
	}
	return p.stopErr
}

func (p *scrobblerStub) RefreshToken(context.Context, ServerConfig, Connection) (TokenSet, error) {
	p.refreshed = true
	if p.refreshErr != nil {
		return TokenSet{}, p.refreshErr
	}
	return p.refreshTokens, nil
}

func (p *scrobblerStub) StartDeviceAuth(context.Context, ServerConfig) (DeviceAuthSession, error) {
	return DeviceAuthSession{}, nil
}

func (p *scrobblerStub) PollDeviceAuth(context.Context, ServerConfig, DeviceAuthSession) (TokenSet, error) {
	return TokenSet{}, nil
}

func (p *scrobblerStub) LookupAccount(context.Context, ServerConfig, Connection) (ProviderAccount, error) {
	return ProviderAccount{}, nil
}

type orderedScrobblerStub struct {
	mu      sync.Mutex
	calls   []string
	started chan string
	release chan struct{}
}

func newOrderedScrobblerStub() *orderedScrobblerStub {
	stub := &orderedScrobblerStub{
		started: make(chan string, 3),
		release: make(chan struct{}),
	}
	return stub
}

func (p *orderedScrobblerStub) Key() string {
	return "simkl"
}

func (p *orderedScrobblerStub) DisplayName() string {
	return "Simkl"
}

func (p *orderedScrobblerStub) Capabilities() Capabilities {
	return Capabilities{ScrobblePlayback: true}
}

func (p *orderedScrobblerStub) ScrobbleOrderingKey(conn Connection, _ ScrobbleEvent) string {
	return "simkl:" + conn.ID
}

func (p *orderedScrobblerStub) Start(context.Context, ServerConfig, Connection, ScrobbleEvent) error {
	return p.record("start")
}

func (p *orderedScrobblerStub) Pause(context.Context, ServerConfig, Connection, ScrobbleEvent) error {
	return p.record("pause")
}

func (p *orderedScrobblerStub) Stop(context.Context, ServerConfig, Connection, ScrobbleEvent) error {
	return p.record("stop")
}

func (p *orderedScrobblerStub) record(action string) error {
	p.started <- action
	<-p.release
	p.mu.Lock()
	p.calls = append(p.calls, action)
	p.mu.Unlock()
	return nil
}

func (p *orderedScrobblerStub) waitCalls(t *testing.T, count int) []string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		if len(p.calls) >= count {
			calls := append([]string{}, p.calls...)
			p.mu.Unlock()
			return calls
		}
		calls := append([]string{}, p.calls...)
		p.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("calls = %+v, want %d", calls, count)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// registryWith returns a registry holding provider.
func registryWith(t *testing.T, provider Provider) *Registry {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg
}

// openUserDB opens an in-memory SQLite user database with the user schema,
// closed when the test ends.
func openUserDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

type staticStoreProvider struct {
	store userstore.UserStore
}

func (p staticStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p staticStoreProvider) Close() error {
	return nil
}

type unmatchedMatcherStub struct {
	reason string
}

func (m unmatchedMatcherStub) Match(context.Context, historyimport.Record) (*historyimport.Match, string, error) {
	return nil, m.reason, nil
}

type matchedMatcherStub struct {
	mediaItemID string
}

func (m matchedMatcherStub) Match(context.Context, historyimport.Record) (*historyimport.Match, string, error) {
	return &historyimport.Match{MediaItemID: m.mediaItemID}, "", nil
}

type watchedLeafMatcherStub struct {
	matches []historyimport.Match
}

func (m watchedLeafMatcherStub) Match(context.Context, historyimport.Record) (*historyimport.Match, string, error) {
	return nil, "unexpected single-item match", nil
}

func (m watchedLeafMatcherStub) MatchLeaves(context.Context, historyimport.Record) ([]historyimport.Match, string, error) {
	return m.matches, "", nil
}

type recordingWatchState struct {
	sources   []userstore.WatchHistorySource
	updatedAt []time.Time
	watchedAt []*time.Time
	completed []bool
	positions []float64
	durations []float64
	targetIDs []string
}

func (s *recordingWatchState) RecordImportedWatchIfNewerWithSource(
	_ context.Context,
	_ int,
	_ string,
	targetID string,
	duration float64,
	position float64,
	completed bool,
	updatedAt time.Time,
	watchedAt *time.Time,
	source userstore.WatchHistorySource,
) (bool, error) {
	s.sources = append(s.sources, source)
	s.updatedAt = append(s.updatedAt, updatedAt)
	s.watchedAt = append(s.watchedAt, watchedAt)
	s.completed = append(s.completed, completed)
	s.positions = append(s.positions, position)
	s.durations = append(s.durations, duration)
	s.targetIDs = append(s.targetIDs, targetID)
	return true, nil
}

func TestServiceStartsAndPollsDeviceAuth(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := &authProviderStub{}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)

	session, err := service.StartDeviceAuth(context.Background(), 7, "profile-1", "trakt")
	if err != nil {
		t.Fatalf("StartDeviceAuth: %v", err)
	}
	if !provider.started || session.ID != "auth-1" {
		t.Fatalf("session = %+v started=%v", session, provider.started)
	}

	conn, err := service.PollDeviceAuth(context.Background(), 7, "profile-1", "trakt", session.ID)
	if err != nil {
		t.Fatalf("PollDeviceAuth: %v", err)
	}
	if conn.ProviderUsername != "alex" || conn.AccessToken != testAccessToken {
		t.Fatalf("connection = %+v", conn)
	}
	if !conn.ImportWatchedEnabled || !conn.ImportProgressEnabled ||
		!conn.ExportWatchedEnabled || !conn.ScrobbleEnabled {
		t.Fatalf("default toggles were not enabled: %+v", conn)
	}
	storedSession := repo.sessions[session.ID]
	if storedSession.CompletedAt == nil {
		t.Fatalf("auth session was not marked completed: %+v", storedSession)
	}
}

func TestServicePersistsRotatedPendingDeviceAuthorizationState(t *testing.T) {
	repo := newServiceFakeRepo()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	original := DeviceAuthSession{
		ID: "auth-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1",
		DeviceCode: "original", UserCode: "CODE", VerificationURL: "https://trakt.tv/activate",
		IntervalSeconds: 5, ExpiresAt: now.Add(10 * time.Minute),
	}
	repo.sessions[original.ID] = original
	provider := &authProviderStub{pollErr: deviceAuthorizationPendingError{session: DeviceAuthSession{
		// These host-owned fields are intentionally wrong; only the rotated
		// challenge state, interval, and expiry may be accepted from the provider.
		ID: "wrong", Provider: "wrong", UserID: 99, ProfileID: "wrong",
		DeviceCode: "rotated", UserCode: "WRONG", VerificationURL: "https://evil.example",
		IntervalSeconds: 11, ExpiresAt: now.Add(20 * time.Minute),
	}}}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)
	service.now = func() time.Time { return now }
	_, err := service.PollDeviceAuth(context.Background(), 7, "profile-1", "trakt", original.ID)
	var pending deviceAuthorizationPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("error = %#v, want pending", err)
	}
	stored := repo.sessions[original.ID]
	if stored.ID != original.ID || stored.Provider != original.Provider || stored.UserID != original.UserID ||
		stored.ProfileID != original.ProfileID || stored.UserCode != original.UserCode ||
		stored.VerificationURL != original.VerificationURL || stored.DeviceCode != "rotated" ||
		stored.IntervalSeconds != 11 || !stored.ExpiresAt.Equal(now.Add(20*time.Minute)) {
		t.Fatalf("stored session = %#v", stored)
	}
}

func TestServiceStartsAndPollsDeviceAuthRejectsMismatchedSession(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := &authProviderStub{}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)

	session, err := service.StartDeviceAuth(context.Background(), 7, "profile-1", "trakt")
	if err != nil {
		t.Fatalf("StartDeviceAuth: %v", err)
	}
	_, err = service.PollDeviceAuth(context.Background(), 7, "profile-2", "trakt", session.ID)
	if err == nil {
		t.Fatal("expected mismatched profile to be rejected")
	}
	if provider.polled {
		t.Fatal("provider was polled before session ownership was verified")
	}
}

func TestServiceRejectsMissingProfileScope(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := &authProviderStub{}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)

	if _, err := service.StartDeviceAuth(context.Background(), 0, "profile-1", "trakt"); err == nil {
		t.Fatal("expected missing user id to be rejected")
	}
	if _, err := service.StartDeviceAuth(context.Background(), 7, "", "trakt"); err == nil {
		t.Fatal("expected missing profile id to be rejected")
	}
	if _, err := service.PollDeviceAuth(context.Background(), 7, "profile-1", "trakt", ""); err == nil {
		t.Fatal("expected missing auth session id to be rejected")
	}
	if provider.started || provider.polled {
		t.Fatal("provider was called for invalid profile scope")
	}
}

func TestServicePollDeviceAuthRejectsExpiredOrCompletedSession(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := &authProviderStub{}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	repo.sessions["expired"] = DeviceAuthSession{
		ID:        "expired",
		Provider:  "trakt",
		UserID:    7,
		ProfileID: "profile-1",
		ExpiresAt: now.Add(-time.Second),
	}
	if _, err := service.PollDeviceAuth(context.Background(), 7, "profile-1", "trakt", "expired"); err == nil {
		t.Fatal("expected expired session to be rejected")
	}

	completedAt := now.Add(-time.Minute)
	repo.sessions["completed"] = DeviceAuthSession{
		ID:          "completed",
		Provider:    "trakt",
		UserID:      7,
		ProfileID:   "profile-1",
		ExpiresAt:   now.Add(time.Minute),
		CompletedAt: &completedAt,
	}
	if _, err := service.PollDeviceAuth(context.Background(), 7, "profile-1", "trakt", "completed"); err == nil {
		t.Fatal("expected completed session to be rejected")
	}
	if provider.polled {
		t.Fatal("provider was polled for expired or completed session")
	}
}

func TestServiceRequestManualSyncReturnsActiveRun(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := &authProviderStub{}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)
	repo.connections[connectionKey("trakt", 7, "profile-1")] = Connection{
		ID:        "conn-1",
		Provider:  "trakt",
		UserID:    7,
		ProfileID: "profile-1",
	}
	repo.syncRuns = append(repo.syncRuns, SyncRun{
		ID:           "active-run",
		ConnectionID: "conn-1",
		Provider:     "trakt",
		Trigger:      "manual",
		Status:       string(SyncRunStatusRunning),
		StartedAt:    time.Now(),
		CreatedAt:    time.Now(),
	})

	result, err := service.RequestManualSync(context.Background(), 7, "profile-1", "trakt")
	if err != nil {
		t.Fatalf("RequestManualSync: %v", err)
	}
	if result.Run.ID != "active-run" {
		t.Fatalf("run ID = %q, want active-run", result.Run.ID)
	}
	if len(repo.syncRuns) != 1 {
		t.Fatalf("sync runs = %d, want 1", len(repo.syncRuns))
	}
}

func TestServiceRequestManualSyncCooldown(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := &authProviderStub{}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	completedAt := now.Add(-30 * time.Minute)
	repo.connections[connectionKey("trakt", 7, "profile-1")] = Connection{
		ID:        "conn-1",
		Provider:  "trakt",
		UserID:    7,
		ProfileID: "profile-1",
	}
	repo.syncRuns = append(repo.syncRuns, SyncRun{
		ID:           "recent-run",
		ConnectionID: "conn-1",
		Provider:     "trakt",
		Trigger:      "scheduled",
		Status:       string(SyncRunStatusSuccess),
		StartedAt:    completedAt.Add(-time.Minute),
		CompletedAt:  &completedAt,
		CreatedAt:    completedAt.Add(-time.Minute),
	})

	_, err := service.RequestManualSync(context.Background(), 7, "profile-1", "trakt")
	var cooldown SyncCooldownError
	if !errors.As(err, &cooldown) {
		t.Fatalf("error = %v, want SyncCooldownError", err)
	}
	if cooldown.RetryAfterSeconds != 30*60 {
		t.Fatalf("retry after = %d, want %d", cooldown.RetryAfterSeconds, 30*60)
	}
}

func TestServiceSyncConnectionRejectsBlankAccessToken(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := watchedExporterStub{}
	reg := registryWith(t, provider)
	conn := Connection{
		ID:                   "conn-1",
		Provider:             "trakt",
		UserID:               7,
		ProfileID:            "profile-1",
		AccessToken:          "",
		ExportWatchedEnabled: true,
	}
	repo.connections[connectionKey("trakt", 7, "profile-1")] = conn

	err := NewService(repo, reg).SyncConnection(context.Background(), conn, "scheduled")
	if err == nil {
		t.Fatal("SyncConnection error = nil, want blank token error")
	}
	latest, ok, err := repo.GetLatestSyncRun(context.Background(), "conn-1")
	if err != nil {
		t.Fatalf("GetLatestSyncRun: %v", err)
	}
	if !ok || latest.Status != string(SyncRunStatusFailed) || latest.Error == "" {
		t.Fatalf("latest run = %+v, want failed blank token run", latest)
	}
}

func TestServiceSyncConnectionRefreshesExpiredToken(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(-time.Minute)
	refreshedExpiresAt := now.Add(time.Hour)
	key := connectionKey("trakt", 7, "profile-1")
	conn := Connection{
		ID:             "conn-1",
		Provider:       "trakt",
		UserID:         7,
		ProfileID:      "profile-1",
		AccessToken:    testOldAccessToken,
		RefreshToken:   testOldRefreshToken,
		TokenExpiresAt: &expiresAt,
	}
	tests := []struct {
		name string
		// otherRefresher stores rotated tokens while the sync waits for the
		// refresh lock. Trakt refresh tokens are single-use, so spending the
		// old one again would be refused.
		otherRefresher bool
		wantRefresh    bool
	}{
		{name: "refreshes the expired token", wantRefresh: true},
		{name: "uses tokens another refresher stored", otherRefresher: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newServiceFakeRepo()
			provider := &authProviderStub{
				refreshTokens: TokenSet{
					AccessToken:    "new-access",
					RefreshToken:   "new-refresh",
					TokenExpiresAt: &refreshedExpiresAt,
				},
			}
			if tt.otherRefresher {
				provider.refreshErr = errors.New("refresh token already spent")
				repo.beforeTokenRefresh = func() {
					stored := repo.connections[key]
					stored.AccessToken, stored.RefreshToken, stored.TokenExpiresAt = "new-access", "new-refresh", &refreshedExpiresAt
					repo.connections[key] = stored
				}
			}
			reg := registryWith(t, provider)
			service := NewService(repo, reg)
			service.now = func() time.Time { return now }
			repo.connections[key] = conn

			if err := service.SyncConnection(context.Background(), conn, "scheduled"); err != nil {
				t.Fatalf("SyncConnection: %v", err)
			}
			if provider.refreshed != tt.wantRefresh {
				t.Fatalf("provider refreshed = %v, want %v", provider.refreshed, tt.wantRefresh)
			}
			updated := repo.connections[key]
			if updated.AccessToken != "new-access" || updated.RefreshToken != "new-refresh" {
				t.Fatalf("connection tokens = %q/%q, want refreshed tokens", updated.AccessToken, updated.RefreshToken)
			}
			if updated.TokenExpiresAt == nil || !updated.TokenExpiresAt.Equal(refreshedExpiresAt) {
				t.Fatalf("token expiry = %v, want %v", updated.TokenExpiresAt, refreshedExpiresAt)
			}
		})
	}
}

// refreshedAccessToken returns a saved connection's access token through the
// serialized refresh path, so tests can drive refreshConnectionIfNeeded the
// way a sync does.
func refreshedAccessToken(ctx context.Context, s *Service, connectionID string) (string, error) {
	conn, ok, err := s.repo.GetConnectionByID(ctx, connectionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrConnectionNotFound
	}
	provider, ok := s.registry.Get(conn.Provider)
	if !ok {
		return "", UnknownProviderError{Key: conn.Provider}
	}
	cfg, err := s.serverConfig(ctx, conn.Provider)
	if err != nil {
		return "", err
	}
	conn, err = s.refreshConnectionIfNeeded(ctx, provider, cfg, conn)
	if err != nil {
		return "", err
	}
	return conn.AccessToken, nil
}

func TestServiceTokenRefreshPreservesConcurrentConnectionChanges(t *testing.T) {
	for _, change := range []string{"disconnect", "reconnect", "account switch", "sync state"} {
		t.Run(change, func(t *testing.T) {
			repo := newServiceFakeRepo()
			now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
			conn := Connection{
				ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1",
				ProviderAccountID: "old-account", AccessToken: testOldAccessToken,
				RefreshToken: testOldRefreshToken, TokenExpiresAt: new(now.Add(-time.Minute)),
				ExportWatchedEnabled: true, ScrobbleEnabled: true,
			}
			key := connectionKey(conn.Provider, conn.UserID, conn.ProfileID)
			repo.connections[key] = conn
			provider := &authProviderStub{refreshTokens: TokenSet{
				AccessToken: "new-access", RefreshToken: "new-refresh", TokenExpiresAt: new(now.Add(time.Hour)),
			}}
			registry := NewRegistry()
			if err := registry.Register(provider); err != nil {
				t.Fatal(err)
			}
			service := NewService(repo, registry)
			service.now = func() time.Time { return now }
			provider.beforeRefresh = func(ctx context.Context, _ Connection) error {
				if change == "disconnect" {
					return repo.DeleteConnection(ctx, conn.Provider, conn.UserID, conn.ProfileID)
				}
				current := repo.connections[key]
				switch change {
				case "reconnect", "account switch":
					current.AccessToken, current.RefreshToken = "reconnected-access", "reconnected-refresh"
					if change == "account switch" {
						current.ProviderAccountID = "new-account"
					}
				case "sync state":
					current.SyncCursors = map[string]string{"trakt.watched": "new-cursor"}
					current.LastOutboundSyncAt = new(now)
					current.ScrobbleEnabled = false
				}
				repo.connections[key] = current
				return nil
			}
			_, err := refreshedAccessToken(t.Context(), service, conn.ID)
			current, exists := repo.connections[key]
			switch change {
			case "disconnect":
				if exists || !errors.Is(err, ErrConnectionNotFound) {
					t.Fatalf("disconnected connection restored: exists=%v err=%v", exists, err)
				}
			case "reconnect", "account switch":
				if !errors.Is(err, ErrStaleConnection) || current.AccessToken != "reconnected-access" {
					t.Fatalf("reconnected credentials overwritten: access=%q err=%v", current.AccessToken, err)
				}
				if change == "account switch" && current.ProviderAccountID != "new-account" {
					t.Fatal("account switch overwritten")
				}
			case "sync state":
				if err != nil || current.AccessToken != "new-access" || current.SyncCursors["trakt.watched"] != "new-cursor" || current.LastOutboundSyncAt == nil || current.ScrobbleEnabled {
					t.Fatalf("sync state lost during refresh: connection=%+v err=%v", current, err)
				}
			}
		})
	}
}

func TestServiceTokenRefreshReloadsAccountBinding(t *testing.T) {
	repo := newServiceFakeRepo()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	conn := Connection{
		ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1",
		ProviderAccountID: "old-account", AccessToken: testOldAccessToken,
		RefreshToken: testOldRefreshToken, TokenExpiresAt: new(now.Add(-time.Minute)),
	}
	key := connectionKey(conn.Provider, conn.UserID, conn.ProfileID)
	repo.connections[key] = conn
	repo.beforeTokenRefresh = func() {
		current := repo.connections[key]
		current.ProviderAccountID = "new-account"
		repo.connections[key] = current
	}
	provider := &authProviderStub{
		refreshTokens: TokenSet{AccessToken: "new-access", RefreshToken: "new-refresh", TokenExpiresAt: new(now.Add(time.Hour))},
		beforeRefresh: func(_ context.Context, current Connection) error {
			if current.ProviderAccountID != "new-account" {
				t.Fatalf("refresh received old account binding: %q", current.ProviderAccountID)
			}
			return nil
		},
	}
	registry := NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, registry)
	service.now = func() time.Time { return now }
	if _, err := refreshedAccessToken(t.Context(), service, conn.ID); err != nil {
		t.Fatal(err)
	}
}

// A disconnect waits for the refresh to finish before deleting its connection.
func TestServiceDisconnectWaitsForAnInFlightTokenRefresh(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(-time.Minute)
	refreshedExpiresAt := now.Add(time.Hour)
	entered, release := make(chan struct{}), make(chan struct{})
	provider := &authProviderStub{
		refreshTokens:  TokenSet{AccessToken: "new-access", RefreshToken: "new-refresh", TokenExpiresAt: &refreshedExpiresAt},
		refreshEntered: entered,
		refreshRelease: release,
	}
	reg := registryWith(t, provider)
	repo := newServiceFakeRepo()
	service := NewService(repo, reg)
	service.now = func() time.Time { return now }
	key := connectionKey("trakt", 7, "profile-1")
	repo.connections[key] = Connection{
		ID:             "conn-1",
		Provider:       "trakt",
		UserID:         7,
		ProfileID:      "profile-1",
		AccessToken:    testOldAccessToken,
		RefreshToken:   testOldRefreshToken,
		TokenExpiresAt: &expiresAt,
	}
	ctx := context.Background()

	refreshDone := make(chan error, 1)
	go func() {
		_, err := refreshedAccessToken(ctx, service, "conn-1")
		refreshDone <- err
	}()
	<-entered

	deleteWaiting := make(chan struct{}, 1)
	repo.onTokenRefreshWait = func() { deleteWaiting <- struct{}{} }
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- service.DeleteConnection(ctx, 7, "profile-1", "trakt") }()
	var deleteErr error
	deleted := false
	select {
	case <-deleteWaiting:
	case deleteErr = <-deleteDone:
		deleted = true
	}
	close(release)
	if err := <-refreshDone; err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if !deleted {
		deleteErr = <-deleteDone
	}
	if deleteErr != nil {
		t.Fatalf("DeleteConnection: %v", deleteErr)
	}
	if _, ok := repo.connections[key]; ok {
		t.Fatal("the refresh recreated the disconnected connection")
	}
}

func TestServicePluginRefreshPersistsAuthoritativeCredentialsBeforeFault(t *testing.T) {
	repo := newServiceFakeRepo()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(-time.Minute)
	client := &fakeWatchSyncPluginClient{refreshResponse: &pluginv1.WatchSyncCredentialResponse{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: testRotatedAccessToken, TokenType: testBearerTokenType},
		Fault: &pluginv1.WatchSyncFault{
			Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL,
			SafeMessage: testReconnectRequired,
		},
	}}
	provider := testPluginProvider(t, client)
	service := NewService(repo, NewRegistry())
	service.now = func() time.Time { return now }
	conn := Connection{
		ID:             "conn-1",
		Provider:       provider.Key(),
		UserID:         7,
		ProfileID:      "profile-1",
		AccessToken:    testOldAccessToken,
		RefreshToken:   testOldRefreshToken,
		TokenExpiresAt: &expiresAt,
	}
	repo.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)] = conn

	if _, err := service.refreshConnectionIfNeeded(context.Background(), provider, ServerConfig{}, conn); !isWatchSyncInvalidCredentialError(err) {
		t.Fatalf("error = %#v", err)
	}
	updated := repo.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)]
	if updated.AccessToken != testRotatedAccessToken || updated.RefreshToken != "" || updated.TokenExpiresAt != nil {
		t.Fatalf("connection credentials = %#v", updated)
	}
	if updated.LastError != testReconnectRequired {
		t.Fatalf("LastError = %q", updated.LastError)
	}
}

func TestServiceImportWatchedExpandsAggregateMarkerToEpisodeLeaves(t *testing.T) {
	repo := newServiceFakeRepo()
	watchedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	provider := watchedImporterStub{
		historyStubBase: historyStubBase{key: "mdblist", source: userstore.WatchHistorySourceMDBList},
		rows: []RemoteWatch{{
			Provider: "mdblist", Kind: historyimport.KindSeries, Title: "Breaking Bad", LastWatchedAt: &watchedAt,
		}},
	}
	watchState := &recordingWatchState{}
	service := NewService(repo, NewRegistry()).
		WithMatcher(watchedLeafMatcherStub{matches: []historyimport.Match{
			{MediaItemID: "episode-1", Kind: historyimport.KindEpisode},
			{MediaItemID: "episode-2", Kind: historyimport.KindEpisode},
		}}).
		WithWatchState(watchState)

	result, err := service.ImportWatched(context.Background(), Connection{
		ID: "conn-1", Provider: "mdblist", UserID: 7, ProfileID: "profile-1",
	}, ServerConfig{}, provider)
	if err != nil {
		t.Fatalf("ImportWatched: %v", err)
	}
	if result.Found != 1 || result.Imported != 2 || result.Unmatched != 0 {
		t.Fatalf("result = %+v, want one aggregate found and two leaves imported", result)
	}
	if !reflect.DeepEqual(watchState.targetIDs, []string{"episode-1", "episode-2"}) {
		t.Fatalf("recorded target ids = %+v", watchState.targetIDs)
	}
	if !reflect.DeepEqual(watchState.sources, []userstore.WatchHistorySource{
		userstore.WatchHistorySourceMDBList,
		userstore.WatchHistorySourceMDBList,
	}) {
		t.Fatalf("recorded sources = %+v", watchState.sources)
	}
}

func TestServiceImportWatchedDeduplicatesResolvedLeavesAtNewestTimestamp(t *testing.T) {
	repo := newServiceFakeRepo()
	olderWatchedAt := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	newerWatchedAt := olderWatchedAt.Add(24 * time.Hour)
	provider := watchedImporterStub{
		historyStubBase: historyStubBase{key: "mdblist", source: userstore.WatchHistorySourceMDBList},
		rows: []RemoteWatch{
			{Provider: "mdblist", Kind: historyimport.KindMovie, Title: "Inception", LastWatchedAt: &olderWatchedAt},
			{Provider: "mdblist", Kind: historyimport.KindMovie, Title: "Inception", LastWatchedAt: &newerWatchedAt},
		},
	}
	watchState := &recordingWatchState{}
	service := NewService(repo, NewRegistry()).
		WithMatcher(matchedMatcherStub{mediaItemID: testMovieMediaID}).
		WithWatchState(watchState)

	result, err := service.ImportWatched(context.Background(), Connection{
		ID: "conn-1", Provider: "mdblist", UserID: 7, ProfileID: "profile-1",
	}, ServerConfig{}, provider)
	if err != nil {
		t.Fatalf("ImportWatched: %v", err)
	}
	if result.Found != 2 || result.Imported != 1 || result.Unmatched != 0 {
		t.Fatalf("result = %+v, want two provider markers and one imported leaf", result)
	}
	if !reflect.DeepEqual(watchState.targetIDs, []string{testMovieMediaID}) {
		t.Fatalf("recorded target ids = %+v", watchState.targetIDs)
	}
	if len(watchState.watchedAt) != 1 || watchState.watchedAt[0] == nil || !watchState.watchedAt[0].Equal(newerWatchedAt) {
		t.Fatalf("recorded watched_at = %+v, want %v", watchState.watchedAt, newerWatchedAt)
	}
}

func TestServiceSyncConnectionPreservesConnectionUpdatesAcrossFlows(t *testing.T) {
	db := openUserDB(t)
	if err := userdb.AddHistory(db, userstore.WatchHistoryEntry{
		ID:              "history-1",
		ProfileID:       "profile-1",
		MediaItemID:     testMovieMediaID,
		WatchedAt:       "2026-05-04T12:00:00Z",
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourcePlayback,
		Identity: userstore.WatchIdentity{
			StableType:  "movie",
			ProviderIDs: map[string]string{"tmdb": "603"},
		},
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}

	watchedAt := time.Date(2026, 5, 4, 13, 0, 0, 0, time.UTC)
	provider := watchedImportExportStub{
		historyStubBase: historyStubBase{key: "simkl", source: userstore.WatchHistorySourceSimkl},
		rows: []RemoteWatch{{
			Provider:      "simkl",
			Kind:          historyimport.KindMovie,
			Title:         "Inception",
			Year:          2010,
			LastWatchedAt: &watchedAt,
		}},
	}
	reg := registryWith(t, provider)
	repo := newServiceFakeRepo()
	service := NewService(repo, reg).
		WithMatcher(matchedMatcherStub{mediaItemID: testMovieMediaID}).
		WithWatchState(&recordingWatchState{}).
		WithUserStoreProvider(staticStoreProvider{store: userdb.NewSQLiteUserStore(db)})
	now := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	conn := Connection{
		ID:                   "conn-1",
		Provider:             "simkl",
		UserID:               7,
		ProfileID:            "profile-1",
		AccessToken:          testAccessToken,
		ImportWatchedEnabled: true,
		ExportWatchedEnabled: true,
	}
	repo.connections[connectionKey("simkl", 7, "profile-1")] = conn

	if err := service.SyncConnection(context.Background(), conn, "scheduled"); err != nil {
		t.Fatalf("SyncConnection: %v", err)
	}
	updated := repo.connections[connectionKey("simkl", 7, "profile-1")]
	if updated.LastInboundSyncAt == nil {
		t.Fatalf("LastInboundSyncAt was not preserved across export: %+v", updated)
	}
	if updated.LastOutboundSyncAt == nil {
		t.Fatalf("LastOutboundSyncAt was not recorded: %+v", updated)
	}
}

// A provider that stores watch times to the minute (Trakt) returns :00 for a
// local play that kept its seconds; one that keeps seconds must not match a
// different time in the same minute.
func TestServiceExportWatchedMatchesRemotePlaysAtProviderPrecision(t *testing.T) {
	tests := []struct {
		name         string
		precision    time.Duration
		wantMatched  bool
		wantExported int
	}{
		{name: "provider stores minutes", precision: time.Minute, wantMatched: true},
		{name: "provider stores seconds", wantExported: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openUserDB(t)
			if err := userdb.AddHistory(db, userstore.WatchHistoryEntry{
				ID:              "history-1",
				ProfileID:       "profile-1",
				MediaItemID:     testMovieMediaID,
				WatchedAt:       "2026-05-04T12:00:37Z",
				DurationSeconds: 7200,
				Completed:       true,
				Source:          userstore.WatchHistorySourcePlayback,
				Identity: userstore.WatchIdentity{
					StableType:  "movie",
					ProviderIDs: map[string]string{"tmdb": "603"},
				},
			}); err != nil {
				t.Fatalf("AddHistory: %v", err)
			}

			var exported []LocalPlay
			provider := watchedExporterStub{
				exported:  &exported,
				precision: tt.precision,
				remote: []RemotePlay{{
					ProviderItemKey: "tmdb:603",
					WatchedAt:       time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
				}},
			}
			repo := newServiceFakeRepo()
			service := NewService(repo, NewRegistry()).WithUserStoreProvider(staticStoreProvider{
				store: userdb.NewSQLiteUserStore(db),
			})
			result, err := service.ExportWatched(context.Background(), repo.withConnection(Connection{
				ID:        "conn-1",
				Provider:  "trakt",
				UserID:    7,
				ProfileID: "profile-1",
			}), ServerConfig{}, provider)
			if err != nil {
				t.Fatalf("ExportWatched: %v", err)
			}
			if matched := result.RemotePresent == 1; matched != tt.wantMatched || len(exported) != tt.wantExported {
				t.Fatalf("result = %+v, exported %d plays; want matched=%v and %d exported", result, len(exported), tt.wantMatched, tt.wantExported)
			}
		})
	}
}

func TestServiceSyncConnectionMarksRunFailedWhenExportTransportFails(t *testing.T) {
	db := openUserDB(t)
	if err := userdb.AddHistory(db, userstore.WatchHistoryEntry{
		ID:              "history-1",
		ProfileID:       "profile-1",
		MediaItemID:     testMovieMediaID,
		WatchedAt:       "2026-05-04T12:00:00Z",
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourcePlayback,
		Identity: userstore.WatchIdentity{
			StableType:  "movie",
			ProviderIDs: map[string]string{"tmdb": "603"},
		},
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}

	repo := newServiceFakeRepo()
	provider := watchedExporterStub{exportErr: errors.New("provider offline")}
	reg := registryWith(t, provider)
	service := NewService(repo, reg).WithUserStoreProvider(staticStoreProvider{
		store: userdb.NewSQLiteUserStore(db),
	})
	conn := Connection{
		ID:                   "conn-1",
		Provider:             "trakt",
		UserID:               7,
		ProfileID:            "profile-1",
		AccessToken:          testAccessToken,
		ExportWatchedEnabled: true,
	}

	err := service.SyncConnection(context.Background(), conn, "scheduled")
	if err == nil {
		t.Fatal("SyncConnection error = nil, want export transport failure")
	}
	latest, ok, err := repo.GetLatestSyncRun(context.Background(), "conn-1")
	if err != nil {
		t.Fatalf("GetLatestSyncRun: %v", err)
	}
	if !ok || latest.Status != string(SyncRunStatusFailed) {
		t.Fatalf("latest run = %+v, want failed run", latest)
	}
	if latest.Error == "" {
		t.Fatalf("latest run error is empty: %+v", latest)
	}
	if len(repo.historyExports) != 1 || repo.historyExports[0].Status != historyExportStatusFailed {
		t.Fatalf("history exports = %+v, want one failed export", repo.historyExports)
	}
}

func TestServiceCompletedScrobblePersistsAndSatisfiesHistoryExport(t *testing.T) {
	repo := newServiceFakeRepo()
	service := NewService(repo, NewRegistry())
	conn := Connection{ID: "conn-1"}
	event := ScrobbleEvent{
		PlaybackSessionID: testPlaybackSessionID,
		MediaItemID:       testEpisodeMediaID,
		Kind:              historyimport.KindEpisode,
		SeriesTVDBID:      "123",
		SeasonNumber:      1,
		EpisodeNumber:     2,
		HistoryID:         testWatchHistoryID,
		OccurredAt:        time.Now().UTC(),
		Completed:         true,
	}
	if err := service.persistCompletedScrobbleExport(context.Background(), conn, event); err != nil {
		t.Fatal(err)
	}
	if len(repo.historyExports) != 1 || repo.historyExports[0].Status != historyExportStatusPending {
		t.Fatalf("history exports = %#v", repo.historyExports)
	}
	if repo.historyExports[0].ProviderItemKey != "show:tvdb:123:s1:e2" {
		t.Fatalf("provider item key = %q", repo.historyExports[0].ProviderItemKey)
	}
	if err := service.dispatchScrobble(context.Background(), watchedScrobblerStub{}, ServerConfig{}, conn, event, "stop", nil); err != nil {
		t.Fatal(err)
	}
	if repo.historyExports[0].Status != historyExportStatusSatisfiedByScrobble {
		t.Fatalf("history export status = %q", repo.historyExports[0].Status)
	}
}

// Playback events reach a plugin with the catalog's titles, so a provider can
// create a title it has never seen (#2196). An episode whose identity fell back
// to a movie gets none, or the plugin would see a movie named after it.
func TestServiceSendsCatalogTitlesWithPluginScrobbles(t *testing.T) {
	episode := ScrobbleEvent{
		MediaItemID: testEpisodeMediaID, Kind: historyimport.KindEpisode,
		SeriesTMDBID: "1396", SeasonNumber: 1, EpisodeNumber: 1,
	}
	unresolved := ScrobbleEvent{MediaItemID: testEpisodeMediaID, Kind: historyimport.KindMovie}
	for _, tc := range []struct {
		name  string
		event ScrobbleEvent
		want  []any
	}{
		{"episode", episode, []any{"Pilot", int32(2008), "Breaking Bad", int32(2008)}},
		{"episode resolved as movie", unresolved, []any{"", int32(0), "", int32(0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newServiceFakeRepo()
			repo.mediaTitles = map[string]MediaTitles{testEpisodeMediaID: {
				Kind: historyimport.KindEpisode, Title: "Pilot", Year: 2008, SeriesTitle: "Breaking Bad", SeriesYear: 2008,
			}}
			repo.scrobbleConnections = []Connection{{ID: "conn-1", Provider: testPluginProviderKey, UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true}}
			client := &fakeWatchSyncPluginClient{applyStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED}
			reg := NewRegistry()
			if err := reg.Register(testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
				AuthMethods:      []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
				ScrobblePlayback: true, MaxBatchSize: 25,
			})); err != nil {
				t.Fatal(err)
			}
			event := tc.event
			event.PlaybackSessionID, event.UserID, event.ProfileID, event.OccurredAt = testPlaybackSessionID, 7, "profile-1", time.Now().UTC()
			if err := NewService(repo, reg).ScrobbleStopConfirmed(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			media := client.applyRequest.GetEvents()[0].GetMedia()
			got := []any{media.GetTitle(), media.GetYear(), media.GetSeriesTitle(), media.GetSeriesYear()}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("title, year, series title, series year = %v, want %v", got, tc.want)
			}
		})
	}
}

// A confirmed stop has a deadline of its own; a stalled title lookup gets only
// part of it, and the stop goes out with IDs alone rather than failing.
func TestServiceConfirmedStopSendsIDsWhenTitleLookupStalls(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.mediaTitles = map[string]MediaTitles{testMovieMediaID: {Kind: historyimport.KindMovie, Title: "1917", Year: 2019}}
	release := make(chan struct{})
	defer close(release)
	repo.mediaTitlesRelease = release
	repo.scrobbleConnections = []Connection{{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true}}
	events := make(chan ScrobbleEvent, 1)
	reg := registryWith(t, scrobblerStub{stopEvents: events})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := NewService(repo, reg).ScrobbleStopConfirmed(ctx, ScrobbleEvent{
		PlaybackSessionID: testPlaybackSessionID, UserID: 7, ProfileID: "profile-1",
		MediaItemID: testMovieMediaID, Kind: historyimport.KindMovie,
	}); err != nil {
		t.Fatalf("ScrobbleStopConfirmed = %v", err)
	}
	if event := <-events; event.MediaItemID != testMovieMediaID || event.Title != "" {
		t.Fatalf("stop event = %+v, want IDs without titles", event)
	}
}

func TestServiceCompletedScrobbleDurablyRetriesSatisfiedPersistenceWithoutResendingStop(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.markSatisfiedErr = errors.New("database unavailable")
	service := NewService(repo, NewRegistry())
	conn := Connection{ID: "conn-1", Provider: "trakt"}
	event := ScrobbleEvent{
		PlaybackSessionID: testPlaybackSessionID,
		HistoryID:         testWatchHistoryID,
		Completed:         true,
	}
	repo.historyExports = []HistoryExport{{ID: testHistoryExportID, ConnectionID: conn.ID, HistoryID: event.HistoryID, Status: historyExportStatusPending}}
	if err := service.dispatchScrobble(context.Background(), watchedScrobblerStub{}, ServerConfig{}, conn, event, "stop", nil); err == nil {
		t.Fatal("expected reconciliation persistence failure")
	}
	if repo.historyExports[0].Status != historyExportStatusPending {
		t.Fatalf("history export status = %q", repo.historyExports[0].Status)
	}
	updates := repo.scrobbleUpdatesSnapshot()
	if len(updates) != 1 || updates[0].stopSentAt == nil {
		t.Fatalf("scrobble updates = %#v", updates)
	}
	if len(repo.pendingReconciliations) != 1 {
		t.Fatalf("pending reconciliations = %#v", repo.pendingReconciliations)
	}
	repo.markSatisfiedErr = nil
	if err := service.SweepOpenScrobbles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.historyExports[0].Status != historyExportStatusSatisfiedByScrobble || len(repo.pendingReconciliations) != 0 {
		t.Fatalf("history exports=%#v pending=%#v", repo.historyExports, repo.pendingReconciliations)
	}
	if updates = repo.scrobbleUpdatesSnapshot(); len(updates) != 1 {
		t.Fatalf("sweeper redispatched remote stop: %#v", updates)
	}
}

func TestServiceOrderedScrobblerDispatchesInQueueOrder(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.scrobbleConnections = []Connection{{ID: "conn-1", Provider: "simkl", UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true}}
	provider := newOrderedScrobblerStub()
	reg := registryWith(t, provider)
	service := NewService(repo, reg)
	event := ScrobbleEvent{
		PlaybackSessionID: "session-1",
		UserID:            7,
		ProfileID:         "profile-1",
		Kind:              historyimport.KindMovie,
		MediaItemID:       testMovieMediaID,
		PositionSeconds:   10,
		DurationSeconds:   100,
	}

	if err := service.ScrobbleStart(context.Background(), event); err != nil {
		t.Fatalf("ScrobbleStart: %v", err)
	}
	if action := <-provider.started; action != "start" {
		t.Fatalf("first dispatch = %q, want start", action)
	}
	if err := service.ScrobblePause(context.Background(), event); err != nil {
		t.Fatalf("ScrobblePause: %v", err)
	}
	if err := service.ScrobbleStop(context.Background(), event); err != nil {
		t.Fatalf("ScrobbleStop: %v", err)
	}
	select {
	case action := <-provider.started:
		t.Fatalf("ordered dispatch advanced to %q before start completed", action)
	case <-time.After(25 * time.Millisecond):
	}

	provider.release <- struct{}{}
	if action := <-provider.started; action != "pause" {
		t.Fatalf("second dispatch = %q, want pause", action)
	}
	provider.release <- struct{}{}
	if action := <-provider.started; action != "stop" {
		t.Fatalf("third dispatch = %q, want stop", action)
	}
	provider.release <- struct{}{}
	calls := provider.waitCalls(t, 3)
	if calls[0] != "start" || calls[1] != "pause" || calls[2] != "stop" {
		t.Fatalf("calls = %+v, want start/pause/stop", calls)
	}
}

func TestServiceScrobbleStopKeepsSessionOpenWhenProviderStopFails(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.scrobbleConnections = []Connection{{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true}}
	provider := scrobblerStub{stopErr: errors.New("stop failed")}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)

	err := service.ScrobbleStop(context.Background(), ScrobbleEvent{
		PlaybackSessionID: "playback-1",
		UserID:            7,
		ProfileID:         "profile-1",
		MediaItemID:       testMovieMediaID,
		HistoryID:         "history-1",
		PositionSeconds:   120,
	})
	if err != nil {
		t.Fatalf("ScrobbleStop: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		updates := repo.scrobbleUpdatesSnapshot()
		for _, update := range updates {
			if update.lastError == "stop failed" {
				for _, seen := range updates {
					if seen.stopSentAt != nil {
						t.Fatalf("stop_sent_at was set despite provider failure: %+v", updates)
					}
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for failed stop update: %+v", updates)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type endedSessionStopperStub struct{ keyedScrobblerStub }

func (endedSessionStopperStub) stopsEndedSessions() {}

// A session that ended without a user stop reaches a provider that mirrors
// live sessions as a recorded stop, so the startup sweep does not stop it
// again, while other providers keep the pause that saves progress.
func TestServiceEndedSessionPauseStopsOnlyEndedSessionStoppers(t *testing.T) {
	for _, tt := range []struct {
		name         string
		sessionEnded bool
		wantAction   string
	}{
		{"pause stays a pause", false, "pause"},
		{"ended session stops", true, scrobbleActionStop},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newServiceFakeRepo()
			for _, key := range []string{"trakt", "plugin"} {
				repo.settings["watchsync."+key+".client_id"] = "client-id"
				repo.settings["watchsync."+key+".client_secret"] = "client-secret"
			}
			repo.scrobbleConnections = []Connection{
				{ID: "conn-trakt", Provider: "trakt", UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true},
				{ID: "conn-plugin", Provider: "plugin", UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true},
			}
			traktStops := make(chan ScrobbleEvent, 1)
			pluginStops := make(chan ScrobbleEvent, 1)
			reg := NewRegistry()
			if err := reg.Register(scrobblerStub{stopEvents: traktStops}); err != nil {
				t.Fatalf("Register trakt: %v", err)
			}
			if err := reg.Register(endedSessionStopperStub{keyedScrobblerStub{
				scrobblerStub: scrobblerStub{stopEvents: pluginStops}, key: "plugin",
			}}); err != nil {
				t.Fatalf("Register plugin: %v", err)
			}
			service := NewService(repo, reg)

			if err := service.ScrobblePause(context.Background(), ScrobbleEvent{
				PlaybackSessionID: "playback-1",
				UserID:            7,
				ProfileID:         "profile-1",
				MediaItemID:       testMovieMediaID,
				PositionSeconds:   120,
				SessionEnded:      tt.sessionEnded,
			}); err != nil {
				t.Fatalf("ScrobblePause: %v", err)
			}
			waitForScrobbleQueues(t, service)

			if got := len(traktStops); got != 0 {
				t.Fatalf("trakt stops = %d, want 0", got)
			}
			wantPluginStops := 0
			if tt.wantAction == scrobbleActionStop {
				wantPluginStops = 1
			}
			if got := len(pluginStops); got != wantPluginStops {
				t.Fatalf("plugin stops = %d, want %d", got, wantPluginStops)
			}
			pluginStopSent := false
			for _, update := range repo.scrobbleUpdatesSnapshot() {
				want := "pause"
				if update.connectionID == "conn-plugin" {
					want = tt.wantAction
					pluginStopSent = pluginStopSent || update.stopSentAt != nil
				}
				if update.action != want {
					t.Fatalf("%s action = %q, want %q", update.connectionID, update.action, want)
				}
				if update.connectionID == "conn-trakt" && update.stopSentAt != nil {
					t.Fatalf("trakt pause recorded a sent stop: %+v", update)
				}
			}
			if pluginStopSent != (wantPluginStops == 1) {
				t.Fatalf("plugin stop recorded as sent = %v, want %v", pluginStopSent, wantPluginStops == 1)
			}
		})
	}
}

// waitForScrobbleQueues waits until every queued provider dispatch finished.
func waitForScrobbleQueues(t *testing.T, service *Service) {
	t.Helper()
	service.scrobbleQueues.Range(func(_, value any) bool {
		queue := value.(*scrobbleQueue)
		queue.mu.Lock()
		tail := queue.tail
		queue.mu.Unlock()
		if tail == nil {
			return true
		}
		select {
		case <-tail:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for queued scrobbles")
		}
		return true
	})
}

func TestServiceConfirmedStopWaitsForProviderAndReopensFailedReplacement(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.scrobbleConnections = []Connection{{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true}}
	provider := scrobblerStub{stopErr: errors.New("stop failed")}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)

	err := service.ScrobbleStopConfirmed(context.Background(), ScrobbleEvent{
		PlaybackSessionID: "playback-1",
		UserID:            7,
		ProfileID:         "profile-1",
		MediaItemID:       testMovieMediaID,
		HistoryID:         "history-1",
		PositionSeconds:   120,
	})
	if err == nil || err.Error() != "stop failed" {
		t.Fatalf("ScrobbleStopConfirmed error = %v, want stop failed", err)
	}
	if len(repo.reopenedScrobbles) != 1 {
		t.Fatalf("reopened scrobbles = %+v, want one durable reopen", repo.reopenedScrobbles)
	}
	reopened := repo.reopenedScrobbles[0]
	if reopened.positionSeconds != 120 || reopened.historyID != "history-1" {
		t.Fatalf("reopened scrobble = %+v, want authoritative progress", reopened)
	}
	updates := repo.scrobbleUpdatesSnapshot()
	for _, update := range updates {
		if update.stopSentAt != nil {
			t.Fatalf("failed confirmed stop marked session closed: %+v", updates)
		}
	}
}

func TestServiceConfirmedStopWaitsInProviderOrder(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.scrobbleConnections = []Connection{{ID: "conn-1", Provider: "simkl", UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true}}
	provider := newOrderedScrobblerStub()
	reg := registryWith(t, provider)
	service := NewService(repo, reg)
	event := ScrobbleEvent{
		PlaybackSessionID: "session-1",
		UserID:            7,
		ProfileID:         "profile-1",
		MediaItemID:       testMovieMediaID,
	}
	if err := service.ScrobbleStart(context.Background(), event); err != nil {
		t.Fatalf("ScrobbleStart: %v", err)
	}
	if action := <-provider.started; action != "start" {
		t.Fatalf("first dispatch = %q, want start", action)
	}

	result := make(chan error, 1)
	go func() {
		result <- service.ScrobbleStopConfirmed(context.Background(), event)
	}()
	select {
	case err := <-result:
		t.Fatalf("confirmed stop returned before queued start completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	provider.release <- struct{}{}
	if action := <-provider.started; action != "stop" {
		t.Fatalf("second dispatch = %q, want stop", action)
	}
	select {
	case err := <-result:
		t.Fatalf("confirmed stop returned before provider completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	provider.release <- struct{}{}
	if err := <-result; err != nil {
		t.Fatalf("ScrobbleStopConfirmed: %v", err)
	}
}

func TestServiceConfirmedStopDispatchesProvidersIndependently(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.settings["watchsync.slow.client_id"] = "client-id"
	repo.settings["watchsync.slow.client_secret"] = "client-secret"
	repo.settings["watchsync.healthy.client_id"] = "client-id"
	repo.settings["watchsync.healthy.client_secret"] = "client-secret"
	repo.scrobbleConnections = []Connection{
		{
			ID:              "conn-slow",
			Provider:        "slow",
			UserID:          7,
			ProfileID:       "profile-1",
			ScrobbleEnabled: true,
		},
		{
			ID:              "conn-healthy",
			Provider:        "healthy",
			UserID:          7,
			ProfileID:       "profile-1",
			ScrobbleEnabled: true,
		},
	}
	slow := keyedScrobblerStub{
		key: "slow",
		scrobblerStub: scrobblerStub{
			stopStarted: make(chan struct{}, 1),
			stopRelease: make(chan struct{}),
		},
	}
	healthy := keyedScrobblerStub{
		key: "healthy",
		scrobblerStub: scrobblerStub{
			stopEvents: make(chan ScrobbleEvent, 1),
		},
	}
	reg := registryWith(t, slow)
	if err := reg.Register(healthy); err != nil {
		t.Fatalf("Register healthy provider: %v", err)
	}
	service := NewService(repo, reg)
	event := ScrobbleEvent{
		PlaybackSessionID: "session-1",
		UserID:            7,
		ProfileID:         "profile-1",
		MediaItemID:       testMovieMediaID,
	}

	result := make(chan error, 1)
	go func() {
		result <- service.ScrobbleStopConfirmed(context.Background(), event)
	}()
	select {
	case <-slow.stopStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for slow provider")
	}
	select {
	case <-healthy.stopEvents:
	case <-time.After(time.Second):
		t.Fatal("healthy provider was starved by slow provider")
	}
	close(slow.stopRelease)
	if err := <-result; err != nil {
		t.Fatalf("ScrobbleStopConfirmed: %v", err)
	}
}

func TestServiceConfirmedStopDoesNotResendSuccessfulProvider(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.scrobbleConnections = []Connection{{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true}}
	provider := scrobblerStub{stopEvents: make(chan ScrobbleEvent, 2)}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)
	event := ScrobbleEvent{
		PlaybackSessionID: "session-1",
		UserID:            7,
		ProfileID:         "profile-1",
		MediaItemID:       testMovieMediaID,
	}

	if err := service.ScrobbleStopConfirmed(context.Background(), event); err != nil {
		t.Fatalf("first ScrobbleStopConfirmed: %v", err)
	}
	if err := service.ScrobbleStopConfirmed(context.Background(), event); err != nil {
		t.Fatalf("retry ScrobbleStopConfirmed: %v", err)
	}
	select {
	case <-provider.stopEvents:
	default:
		t.Fatal("successful confirmed stop was not dispatched")
	}
	select {
	case duplicate := <-provider.stopEvents:
		t.Fatalf("successful provider received duplicate confirmed stop: %+v", duplicate)
	default:
	}
	if len(repo.reopenedScrobbles) != 1 {
		t.Fatalf("confirmed stop preparations = %+v, want one", repo.reopenedScrobbles)
	}
}

func TestServiceConfirmedStopSerializesConcurrentConfirmation(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.scrobbleConnections = []Connection{{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true}}
	provider := scrobblerStub{
		stopStarted: make(chan struct{}, 1),
		stopRelease: make(chan struct{}),
	}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)
	event := ScrobbleEvent{
		PlaybackSessionID: "session-1",
		UserID:            7,
		ProfileID:         "profile-1",
		MediaItemID:       testMovieMediaID,
	}

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- service.ScrobbleStopConfirmed(context.Background(), event)
	}()
	select {
	case <-provider.stopStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first provider stop")
	}

	secondResult := make(chan error, 1)
	go func() {
		secondResult <- service.ScrobbleStopConfirmed(context.Background(), event)
	}()
	select {
	case err := <-secondResult:
		t.Fatalf("concurrent confirmation returned before the first completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(provider.stopRelease)
	if err := <-firstResult; err != nil {
		t.Fatalf("first ScrobbleStopConfirmed: %v", err)
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second ScrobbleStopConfirmed: %v", err)
	}
	select {
	case <-provider.stopStarted:
		t.Fatal("concurrent confirmation dispatched a duplicate provider stop")
	default:
	}
}

func TestConfirmedStopLeaseExceedsDispatchTimeout(t *testing.T) {
	if confirmedStopLease <= confirmedStopDispatchTimeout {
		t.Fatalf("confirmed stop lease %s must exceed dispatch timeout %s", confirmedStopLease, confirmedStopDispatchTimeout)
	}
}

func TestServiceConfirmedStopCannotCompleteReclaimedLease(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.scrobbleConnections = []Connection{{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true}}
	provider := scrobblerStub{
		stopStarted: make(chan struct{}, 1),
		stopRelease: make(chan struct{}),
	}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)
	event := ScrobbleEvent{
		PlaybackSessionID: "session-1",
		UserID:            7,
		ProfileID:         "profile-1",
		MediaItemID:       testMovieMediaID,
	}

	result := make(chan error, 1)
	go func() {
		result <- service.ScrobbleStopConfirmed(context.Background(), event)
	}()
	select {
	case <-provider.stopStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for provider stop")
	}

	key := event.PlaybackSessionID + "|conn-1"
	repo.confirmingScrobbles[key] = time.Now().Add(time.Second)
	close(provider.stopRelease)
	if err := <-result; !errors.Is(err, errConfirmedStopClaimLost) {
		t.Fatalf("stale ScrobbleStopConfirmed error = %v, want claim lost", err)
	}
	if repo.confirmedScrobbles[key] {
		t.Fatal("stale provider worker marked the reclaimed stop confirmed")
	}
}

func TestServiceSweepOpenScrobblesRetriesProviderStop(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.connections[connectionKey("trakt", 7, "profile-1")] = Connection{
		ID:              "conn-1",
		Provider:        "trakt",
		UserID:          7,
		ProfileID:       "profile-1",
		AccessToken:     testAccessToken,
		ScrobbleEnabled: true,
	}
	repo.scrobbleSessions = []ScrobbleSession{{
		PlaybackSessionID: "playback-1",
		ConnectionID:      "conn-1",
		MediaItemID:       testMovieMediaID,
		Kind:              "movie",
		TMDBID:            "603",
		HistoryID:         "history-1",
		LastProgress:      5400,
		DurationSeconds:   7200,
		Completed:         true,
	}}
	provider := scrobblerStub{stopEvents: make(chan ScrobbleEvent, 1)}
	reg := registryWith(t, provider)
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	service := NewService(repo, reg)
	service.now = func() time.Time { return now }

	if err := service.SweepOpenScrobbles(context.Background()); err != nil {
		t.Fatalf("SweepOpenScrobbles: %v", err)
	}

	select {
	case event := <-provider.stopEvents:
		if event.PlaybackSessionID != "playback-1" || event.UserID != 7 || event.ProfileID != "profile-1" {
			t.Fatalf("stop event ownership = %+v, want playback/profile context", event)
		}
		if event.TMDBID != "603" || event.DurationSeconds != 7200 || !event.Completed {
			t.Fatalf("stop event media fields = %+v, want persisted event metadata", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for provider stop retry")
	}
	foundClosed := false
	updates := repo.scrobbleUpdatesSnapshot()
	for _, update := range updates {
		if update.action == "stop" && update.stopSentAt != nil {
			foundClosed = true
		}
	}
	if !foundClosed {
		t.Fatalf("scrobble updates = %+v, want successful stop to mark session closed", updates)
	}
}

// A sync near its deadline gives the title lookup only part of the time left,
// so the provider call that follows still has a live context.
type recordingWatchedExporter struct {
	watchedImportExportStub
	exported *[]LocalPlay
	ctxErrs  *[]error
}

func (p recordingWatchedExporter) ExportHistory(ctx context.Context, cfg ServerConfig, conn Connection, plays []LocalPlay) (ExportResult, error) {
	*p.exported = append(*p.exported, plays...)
	if p.ctxErrs != nil {
		*p.ctxErrs = append(*p.ctxErrs, ctx.Err())
	}
	return p.watchedImportExportStub.ExportHistory(ctx, cfg, conn, plays)
}

func TestServiceExportWatchedKeepsDeadlineForProviderWhenTitlesStall(t *testing.T) {
	db := openUserDB(t)
	if err := userdb.AddHistory(db, userstore.WatchHistoryEntry{
		ID: testWatchHistoryID, ProfileID: "profile-1", MediaItemID: testMovieMediaID,
		WatchedAt: "2026-05-04T12:00:00Z", DurationSeconds: 7200, Completed: true,
		Source:   userstore.WatchHistorySourcePlayback,
		Identity: userstore.WatchIdentity{StableType: "movie", ProviderIDs: map[string]string{"tmdb": "16996"}},
	}); err != nil {
		t.Fatal(err)
	}
	repo := newServiceFakeRepo()
	repo.mediaTitles = map[string]MediaTitles{testMovieMediaID: {Kind: historyimport.KindMovie, Title: "17 Again", Year: 2009}}
	release := make(chan struct{})
	defer close(release)
	repo.mediaTitlesRelease = release
	service := NewService(repo, NewRegistry()).WithUserStoreProvider(staticStoreProvider{store: userdb.NewSQLiteUserStore(db)})
	var exported []LocalPlay
	var ctxErrs []error
	exporter := recordingWatchedExporter{watchedImportExportStub{historyStubBase: historyStubBase{key: "simkl", source: userstore.WatchHistorySourceSimkl}}, &exported, &ctxErrs}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, err := service.ExportWatched(ctx, repo.withConnection(Connection{ID: "conn-1", Provider: "simkl", UserID: 7, ProfileID: "profile-1"}), ServerConfig{}, exporter); err != nil {
		t.Fatal(err)
	}
	if len(ctxErrs) != 1 || ctxErrs[0] != nil || len(exported) != 1 || exported[0].Title != "" {
		t.Fatalf("export context errors = %v, plays = %+v; want one live call with IDs alone", ctxErrs, exported)
	}
}

type completedHistoryListerStub struct {
	rows    []userstore.WatchHistoryEntry
	queries []userstore.CompletedHistoryQuery
}

func (s *completedHistoryListerStub) ListCompletedHistory(_ context.Context, query userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error) {
	s.queries = append(s.queries, query)
	start := query.Offset
	if start >= len(s.rows) {
		return nil, nil
	}
	limit := query.Limit
	if limit <= 0 {
		limit = len(s.rows)
	}
	end := start + limit
	if end > len(s.rows) {
		end = len(s.rows)
	}
	return s.rows[start:end], nil
}

func TestHasVisibleCompletedHistoryAtOrAfterScopesTargetAndPaginates(t *testing.T) {
	at := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	rows := make([]userstore.WatchHistoryEntry, 501)
	for i := 0; i < 500; i++ {
		rows[i] = userstore.WatchHistoryEntry{
			ProfileID:   "profile-1",
			MediaItemID: testMovieMediaID,
			WatchedAt:   at.Add(-time.Duration(500-i) * time.Hour).Format(time.RFC3339),
			Completed:   true,
		}
	}
	rows[500] = userstore.WatchHistoryEntry{
		ProfileID:   "profile-1",
		MediaItemID: testMovieMediaID,
		WatchedAt:   at.Add(time.Minute).Format(time.RFC3339),
		Completed:   true,
	}
	store := &completedHistoryListerStub{rows: rows}

	found, err := hasVisibleCompletedHistoryAtOrAfter(context.Background(), store, "profile-1", testMovieMediaID, at)
	if err != nil {
		t.Fatalf("hasVisibleCompletedHistoryAtOrAfter: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if len(store.queries) != 2 {
		t.Fatalf("query count = %d, want 2", len(store.queries))
	}
	for _, query := range store.queries {
		if query.ProfileID != "profile-1" || len(query.MediaItemIDs) != 1 || query.MediaItemIDs[0] != testMovieMediaID {
			t.Fatalf("query was not scoped to target media item: %+v", query)
		}
	}
}

func TestListAllCompletedHistoryPaginatesUntilExhausted(t *testing.T) {
	rows := make([]userstore.WatchHistoryEntry, 501)
	for i := range rows {
		rows[i] = userstore.WatchHistoryEntry{
			ID:          "history-" + strconv.Itoa(i),
			ProfileID:   "profile-1",
			MediaItemID: "movie-" + strconv.Itoa(i),
			Completed:   true,
		}
	}
	store := &completedHistoryListerStub{rows: rows}

	got, err := listAllCompletedHistory(context.Background(), store, userstore.CompletedHistoryQuery{
		ProfileID:      "profile-1",
		ExcludeSources: []userstore.WatchHistorySource{userstore.WatchHistorySourceTrakt},
	})
	if err != nil {
		t.Fatalf("listAllCompletedHistory: %v", err)
	}
	if len(got) != len(rows) {
		t.Fatalf("history len = %d, want %d", len(got), len(rows))
	}
	if len(store.queries) != 2 {
		t.Fatalf("query count = %d, want 2", len(store.queries))
	}
	if store.queries[0].Limit != completedHistoryPageSize || store.queries[0].Offset != 0 {
		t.Fatalf("first query = %+v, want first page", store.queries[0])
	}
	if store.queries[1].Limit != completedHistoryPageSize || store.queries[1].Offset != completedHistoryPageSize {
		t.Fatalf("second query = %+v, want second page", store.queries[1])
	}
	for _, query := range store.queries {
		if query.ProfileID != "profile-1" || len(query.ExcludeSources) != 1 ||
			query.ExcludeSources[0] != userstore.WatchHistorySourceTrakt {
			t.Fatalf("query did not preserve export filters: %+v", query)
		}
	}
}

// rateLimitedImporterStub rate-limits watched import and records whether the
// progress flow was still attempted afterwards.
type rateLimitedImporterStub struct {
	progressCalled *bool
	fetchCalls     *int
}

func (p rateLimitedImporterStub) Key() string         { return "trakt" }
func (p rateLimitedImporterStub) DisplayName() string { return "Trakt" }
func (p rateLimitedImporterStub) Capabilities() Capabilities {
	return Capabilities{ImportWatched: true, ImportProgress: true}
}

func (p rateLimitedImporterStub) FetchWatched(context.Context, ServerConfig, Connection) ([]RemoteWatch, error) {
	if p.fetchCalls != nil {
		*p.fetchCalls++
	}
	return nil, RateLimitedError{Provider: "trakt", RetryAfter: 30 * time.Minute}
}

func (p rateLimitedImporterStub) FetchProgress(context.Context, ServerConfig, Connection) ([]RemoteProgress, error) {
	*p.progressCalled = true
	return nil, nil
}

func (p rateLimitedImporterStub) HistorySource() userstore.WatchHistorySource {
	return userstore.WatchHistorySourceTrakt
}

func TestServicePluginAPIKeyConnectRejectsEmptyReturnedCredential(t *testing.T) {
	repo := newServiceFakeRepo()
	registry := NewRegistry()
	provider := emptyPluginAPIKeyProvider{}
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, registry)
	if _, err := service.ConnectAPIKey(context.Background(), 7, "profile-1", provider.Key(), "input-secret"); err == nil {
		t.Fatal("ConnectAPIKey error = nil")
	}
	if len(repo.connections) != 0 {
		t.Fatalf("connections = %#v", repo.connections)
	}
}

func TestServiceSyncConnectionDefersOnRateLimit(t *testing.T) {
	db := openUserDB(t)

	repo := newServiceFakeRepo()
	progressCalled := false
	provider := rateLimitedImporterStub{progressCalled: &progressCalled}
	reg := registryWith(t, provider)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	service := NewService(repo, reg).
		WithMatcher(matchedMatcherStub{mediaItemID: testMovieMediaID}).
		WithWatchState(noopWatchState{}).
		WithUserStoreProvider(staticStoreProvider{store: userdb.NewSQLiteUserStore(db)})
	service.now = func() time.Time { return now }
	conn := Connection{
		ID:                    "conn-1",
		Provider:              "trakt",
		UserID:                7,
		ProfileID:             "profile-1",
		AccessToken:           testAccessToken,
		ImportWatchedEnabled:  true,
		ImportProgressEnabled: true,
	}
	repo.connections[connectionKey("trakt", 7, "profile-1")] = conn

	err := service.SyncConnection(context.Background(), conn, "scheduled")
	if err == nil {
		t.Fatal("SyncConnection error = nil, want rate limit failure")
	}
	if progressCalled {
		t.Fatal("progress import ran after the watched import was rate limited")
	}
	updated := repo.connections[connectionKey("trakt", 7, "profile-1")]
	wantUntil := now.Add(30 * time.Minute)
	if updated.RateLimitedUntil == nil || !updated.RateLimitedUntil.Equal(wantUntil) {
		t.Fatalf("RateLimitedUntil = %v, want %v", updated.RateLimitedUntil, wantUntil)
	}
	if !strings.Contains(updated.LastError, "rate limit") {
		t.Fatalf("LastError = %q, want rate limit message", updated.LastError)
	}

	// A manual sync during the deferral must be refused with the remaining wait.
	_, err = service.RequestManualSync(context.Background(), 7, "profile-1", "trakt")
	var cooldown SyncCooldownError
	if !errors.As(err, &cooldown) {
		t.Fatalf("RequestManualSync error = %v, want SyncCooldownError", err)
	}
	if cooldown.RetryAfterSeconds != 30*60 {
		t.Fatalf("RetryAfterSeconds = %d, want %d", cooldown.RetryAfterSeconds, 30*60)
	}
}

func TestServiceLocalWatchEventCommitsPartialResultAndDefersRateLimit(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := watchedExporterStub{
		exportErr:    RateLimitedError{Provider: "trakt", RetryAfter: 72 * time.Hour},
		exportResult: ExportResult{Sent: []string{"history-1"}},
	}
	registry := NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	service := NewService(repo, registry)
	service.now = func() time.Time { return now }
	conn := Connection{
		ID:                   "conn-1",
		Provider:             "trakt",
		UserID:               7,
		ProfileID:            "profile-1",
		ProviderAccountID:    testProviderAccountID,
		ExportWatchedEnabled: true,
	}
	repo.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)] = conn

	if err := service.processLocalWatchEvent(context.Background(), LocalWatchEvent{
		Kind:      LocalWatchEventMarkedWatched,
		UserID:    conn.UserID,
		ProfileID: conn.ProfileID,
		Plays: []LocalPlay{{
			HistoryID:       "history-1",
			MediaItemID:     testMovieMediaID,
			ProviderItemKey: testMovieProviderItemKey,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(repo.historyExports) != 1 || repo.historyExports[0].Status != historyExportStatusSent {
		t.Fatalf("history exports = %#v", repo.historyExports)
	}
	updated := repo.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)]
	wantUntil := now.Add(24 * time.Hour)
	if updated.RateLimitedUntil == nil || !updated.RateLimitedUntil.Equal(wantUntil) {
		t.Fatalf("RateLimitedUntil = %v, want %v", updated.RateLimitedUntil, wantUntil)
	}
}

// A marked-watched event queued before the profile marked the item unwatched
// must not send the play: the unmark keeps it in history but stops it counting
// as watched, and no removal follows it to the provider.
func TestServiceLocalWatchEventSkipsPlaysUnmarkedSinceQueued(t *testing.T) {
	db := openUserDB(t)
	store := userdb.NewSQLiteUserStore(db)
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-1", Name: "Viewer"}); err != nil {
		t.Fatal(err)
	}
	watchedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{ID: "history-1", ProfileID: "profile-1", MediaItemID: testMovieMediaID, WatchedAt: watchedAt.Format(time.RFC3339), Completed: true, Source: userstore.WatchHistorySourceManual}); err != nil {
		t.Fatal(err)
	}
	if err := watchstate.NewService(staticStoreProvider{store: store}).RecordManualMarkUnwatched(ctx, 7, "profile-1", []string{testMovieMediaID}); err != nil {
		t.Fatal(err)
	}

	repo := newServiceFakeRepo()
	var exported []LocalPlay
	registry := NewRegistry()
	if err := registry.Register(watchedExporterStub{exported: &exported}); err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, registry).WithUserStoreProvider(staticStoreProvider{store: store})
	conn := Connection{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1", ProviderAccountID: testProviderAccountID, ExportWatchedEnabled: true}
	repo.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)] = conn

	if err := service.processLocalWatchEvent(ctx, LocalWatchEvent{
		Kind:      LocalWatchEventMarkedWatched,
		UserID:    conn.UserID,
		ProfileID: conn.ProfileID,
		Plays:     []LocalPlay{{HistoryID: "history-1", MediaItemID: testMovieMediaID, ProviderItemKey: testMovieProviderItemKey, WatchedAt: watchedAt}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(exported) != 0 {
		t.Fatalf("exported %+v, want nothing for a play unmarked since the event was queued", exported)
	}
}

// A mark unwatched that lands while one provider exports must stop the play
// going to the next provider too.
func TestServiceLocalWatchEventRechecksPlaysPerConnection(t *testing.T) {
	db := openUserDB(t)
	store := userdb.NewSQLiteUserStore(db)
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-1", Name: "Viewer"}); err != nil {
		t.Fatal(err)
	}
	watchedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{ID: "history-1", ProfileID: "profile-1", MediaItemID: testMovieMediaID, WatchedAt: watchedAt.Format(time.RFC3339), Completed: true, Source: userstore.WatchHistorySourceManual}); err != nil {
		t.Fatal(err)
	}
	unmark := func() {
		if err := watchstate.NewService(staticStoreProvider{store: store}).RecordManualMarkUnwatched(ctx, 7, "profile-1", []string{testMovieMediaID}); err != nil {
			t.Error(err)
		}
	}

	repo := newServiceFakeRepo()
	var exported []LocalPlay
	registry := NewRegistry()
	for _, key := range []string{"trakt", "simkl"} {
		if err := registry.Register(watchedExporterStub{exported: &exported, onExport: unmark, historyStubBase: historyStubBase{key: key}}); err != nil {
			t.Fatal(err)
		}
		conn := Connection{ID: "conn-" + key, Provider: key, UserID: 7, ProfileID: "profile-1", ProviderAccountID: testProviderAccountID, ExportWatchedEnabled: true}
		repo.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)] = conn
	}
	service := NewService(repo, registry).WithUserStoreProvider(staticStoreProvider{store: store})

	if err := service.processLocalWatchEvent(ctx, LocalWatchEvent{
		Kind:      LocalWatchEventMarkedWatched,
		UserID:    7,
		ProfileID: "profile-1",
		Plays:     []LocalPlay{{HistoryID: "history-1", MediaItemID: testMovieMediaID, ProviderItemKey: testMovieProviderItemKey, WatchedAt: watchedAt}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(exported) != 1 {
		t.Fatalf("exported %d plays, want 1: the provider after the unmark must get nothing", len(exported))
	}
}

func TestServicePluginTransportFailureLeavesExportPending(t *testing.T) {
	repo := newServiceFakeRepo()
	service := NewService(repo, NewRegistry())
	conn := Connection{ID: "conn-1"}
	err := service.exportLocalPlays(context.Background(), conn, ServerConfig{}, watchedExporterStub{
		exportErr: retryableProviderError{message: "watch sync plugin is unavailable"},
	}, []LocalPlay{{
		HistoryID:       "history-1",
		MediaItemID:     testMovieMediaID,
		ProviderItemKey: testMovieProviderItemKey,
	}})
	if !isRetryableProviderError(err) {
		t.Fatalf("error = %#v", err)
	}
	if len(repo.historyExports) != 1 || repo.historyExports[0].Status != historyExportStatusPending || repo.historyExports[0].AttemptCount != 0 {
		t.Fatalf("history exports = %#v", repo.historyExports)
	}
}

func TestServicePluginInvalidCredentialLeavesExportPendingAndRecordsConnectionError(t *testing.T) {
	repo := newServiceFakeRepo()
	client := &fakeWatchSyncPluginClient{applyResponse: &pluginv1.WatchSyncApplyEventsResponse{
		Fault: &pluginv1.WatchSyncFault{
			Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL,
			SafeMessage: "credential revoked",
		},
	}}
	provider := testPluginProvider(t, client)
	registry := NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, registry)
	conn := Connection{
		ID:                   "conn-1",
		Provider:             provider.Key(),
		UserID:               7,
		ProfileID:            "profile-1",
		AccessToken:          testSecretValue,
		ExportWatchedEnabled: true,
	}
	repo.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)] = conn
	err := service.processLocalWatchEvent(context.Background(), LocalWatchEvent{
		Kind:      LocalWatchEventMarkedWatched,
		UserID:    conn.UserID,
		ProfileID: conn.ProfileID,
		Plays: []LocalPlay{{
			HistoryID:       "history-1",
			MediaItemID:     testMovieMediaID,
			ProviderItemKey: testMovieProviderItemKey,
			Kind:            historyimport.KindMovie,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.historyExports) != 1 || repo.historyExports[0].Status != historyExportStatusPending || repo.historyExports[0].AttemptCount != 0 {
		t.Fatalf("history exports = %#v", repo.historyExports)
	}
	updated := repo.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)]
	if updated.LastError != "credential revoked" {
		t.Fatalf("LastError = %q", updated.LastError)
	}
}

func TestSyncDueConnectionsSkipsSiblingsOfRateLimitedAccount(t *testing.T) {
	db := openUserDB(t)

	repo := newServiceFakeRepo()
	fetchCalls := 0
	progressCalled := false
	provider := rateLimitedImporterStub{progressCalled: &progressCalled, fetchCalls: &fetchCalls}
	reg := registryWith(t, provider)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	service := NewService(repo, reg).
		WithMatcher(matchedMatcherStub{mediaItemID: testMovieMediaID}).
		WithWatchState(noopWatchState{}).
		WithUserStoreProvider(staticStoreProvider{store: userdb.NewSQLiteUserStore(db)})
	service.now = func() time.Time { return now }

	// Two household profiles share one MDBList-style API key (same provider
	// account); the second must not sync after the first exhausts the quota.
	first := Connection{
		ID:                   "conn-1",
		Provider:             "trakt",
		UserID:               7,
		ProfileID:            "profile-1",
		ProviderAccountID:    "acct-1",
		AccessToken:          testAccessToken,
		ImportWatchedEnabled: true,
	}
	second := Connection{
		ID:                   "conn-2",
		Provider:             "trakt",
		UserID:               7,
		ProfileID:            "profile-2",
		ProviderAccountID:    "acct-1",
		AccessToken:          testAccessToken,
		ImportWatchedEnabled: true,
	}
	repo.connections[connectionKey("trakt", 7, "profile-1")] = first
	repo.connections[connectionKey("trakt", 7, "profile-2")] = second
	repo.dueConnections = []Connection{first, second}

	if err := service.SyncDueConnections(context.Background()); err != nil {
		t.Fatalf("SyncDueConnections: %v", err)
	}
	if fetchCalls != 1 {
		t.Fatalf("fetch calls = %d, want 1 (sibling connection must be skipped)", fetchCalls)
	}
	sibling := repo.connections[connectionKey("trakt", 7, "profile-2")]
	wantUntil := now.Add(30 * time.Minute)
	if sibling.RateLimitedUntil == nil || !sibling.RateLimitedUntil.Equal(wantUntil) {
		t.Fatalf("sibling RateLimitedUntil = %v, want %v", sibling.RateLimitedUntil, wantUntil)
	}
}

type favoriteBatchProviderStub struct {
	batch FavoriteImportBatch
}

func (favoriteBatchProviderStub) Key() string         { return "plugin:4:list" }
func (favoriteBatchProviderStub) DisplayName() string { return "List" }
func (favoriteBatchProviderStub) Capabilities() Capabilities {
	return Capabilities{ImportFavorites: true}
}
func (p favoriteBatchProviderStub) FetchFavorites(context.Context, ServerConfig, Connection) ([]RemoteFavorite, error) {
	return p.batch.Rows, nil
}
func (p favoriteBatchProviderStub) FetchFavoritesBatch(context.Context, ServerConfig, Connection) (FavoriteImportBatch, error) {
	return p.batch, nil
}

// favoriteImportFixture returns a SQLite user store, a fake repo, and a plugin
// list connection that imports favorites and syncs their removals.
func favoriteImportFixture(t *testing.T) (*userdb.SQLiteUserStore, *serviceFakeRepo, Connection) {
	t.Helper()
	conn := Connection{
		ID: "conn-1", Provider: "plugin:4:list", UserID: 7, ProfileID: "profile-1",
		ImportFavoritesEnabled: true, SyncFavoriteRemovalsEnabled: true,
	}
	repo := newServiceFakeRepo()
	repo.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)] = conn
	return userdb.NewSQLiteUserStore(openUserDB(t)), repo, conn
}

func TestServiceAppliesIncrementalFavoriteTombstone(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	ctx := context.Background()
	if _, err := store.AddFavoriteAt(ctx, "profile-1", testMovieMediaID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	repo.listItemStates = []ListItemState{{
		ConnectionID: conn.ID, ListKind: ListKindFavorites, MediaItemID: testMovieMediaID,
		ProviderItemKey: testMovieProviderItemKey, RemotePresent: true, LocalPresent: true,
	}}
	service := NewService(repo, NewRegistry()).
		WithMatcher(matchedMatcherStub{mediaItemID: testMovieMediaID}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	provider := favoriteBatchProviderStub{batch: FavoriteImportBatch{
		Rows:           []RemoteFavorite{{ProviderItemKey: testMovieProviderItemKey, Removed: true}},
		UpdatedCursors: map[string]string{pluginFavoritesCursorKey: "cursor-2"},
		Incremental:    true,
	}}
	result, err := service.importList(ctx, conn, ServerConfig{}, provider, service.favoritesBinding())
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 1 || !result.changedLocalList() {
		t.Fatalf("result = %#v", result)
	}
	favorites, err := store.ListFavorites(ctx, conn.ProfileID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(favorites) != 0 || repo.listItemStates[0].RemotePresent || repo.listItemStates[0].LocalPresent {
		t.Fatalf("favorites=%#v state=%#v", favorites, repo.listItemStates[0])
	}
	updated := repo.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)]
	if updated.SyncCursors[pluginFavoritesCursorKey] != "cursor-2" {
		t.Fatalf("connection cursors = %#v", updated.SyncCursors)
	}
}

func TestServiceIncrementalFavoriteAbsenceIsNotRemoval(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	ctx := context.Background()
	if _, err := store.AddFavoriteAt(ctx, "profile-1", testMovieMediaID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	repo.listItemStates = []ListItemState{{
		ConnectionID: conn.ID, ListKind: ListKindFavorites, MediaItemID: testMovieMediaID,
		ProviderItemKey: testMovieProviderItemKey, RemotePresent: true, LocalPresent: true,
	}}
	service := NewService(repo, NewRegistry()).
		WithMatcher(matchedMatcherStub{mediaItemID: testMovieMediaID}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	provider := favoriteBatchProviderStub{batch: FavoriteImportBatch{Incremental: true}}
	result, err := service.importList(ctx, conn, ServerConfig{}, provider, service.favoritesBinding())
	if err != nil {
		t.Fatal(err)
	}
	favorites, err := store.ListFavorites(ctx, conn.ProfileID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 0 || len(favorites) != 1 || !repo.listItemStates[0].RemotePresent || !repo.listItemStates[0].LocalPresent {
		t.Fatalf("result=%#v favorites=%#v state=%#v", result, favorites, repo.listItemStates[0])
	}
}

func TestServiceFavoriteTombstoneForSharedKey(t *testing.T) {
	tests := []struct {
		name          string
		items         map[string]string // media item id -> kind
		tombstoneKind string
		wantRemoved   []string
	}{
		{
			// Two library items for one title carry the same IMDb id.
			name:        "copies of one title all leave",
			items:       map[string]string{"movie:tmdb:550": "movie", "movie:imdb:tt0137523": "movie"},
			wantRemoved: []string{"movie:imdb:tt0137523", "movie:tmdb:550"},
		},
		{
			name:        "a row without a kind is a copy",
			items:       map[string]string{"movie:tmdb:550": "movie", "movie:imdb:tt0137523": ""},
			wantRemoved: []string{"movie:imdb:tt0137523", "movie:tmdb:550"},
		},
		{
			// A movie and a series with the same TMDB id and no IMDb id are
			// different titles; a key-only tombstone cannot say which left.
			name:  "different titles stay",
			items: map[string]string{"movie:tmdb:550": "movie", "series:tmdb:550": "series"},
		},
		{
			name:          "a tombstone with a kind removes that title",
			items:         map[string]string{"movie:tmdb:550": "movie", "series:tmdb:550": "series"},
			tombstoneKind: "movie",
			wantRemoved:   []string{"movie:tmdb:550"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, repo, conn := favoriteImportFixture(t)
			ctx := context.Background()
			for id, kind := range tt.items {
				if _, err := store.AddFavoriteAt(ctx, conn.ProfileID, id, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				repo.listItemStates = append(repo.listItemStates, ListItemState{
					ConnectionID: conn.ID, ListKind: ListKindFavorites, MediaItemID: id, Kind: kind,
					ProviderItemKey: "shared-key", RemotePresent: true, LocalPresent: true,
				})
			}
			service := NewService(repo, NewRegistry()).
				WithMatcher(matchedMatcherStub{mediaItemID: testMovieMediaID}).
				WithUserStoreProvider(staticStoreProvider{store: store})
			provider := favoriteBatchProviderStub{batch: FavoriteImportBatch{
				Rows:        []RemoteFavorite{{ProviderItemKey: "shared-key", Kind: tt.tombstoneKind, Removed: true}},
				Incremental: true,
			}}
			result, err := service.importList(ctx, conn, ServerConfig{}, provider, service.favoritesBinding())
			if err != nil {
				t.Fatal(err)
			}
			favorites, err := store.ListFavorites(ctx, conn.ProfileID, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			if result.Removed != len(tt.wantRemoved) || len(favorites) != len(tt.items)-len(tt.wantRemoved) {
				t.Fatalf("result=%#v favorites=%#v", result, favorites)
			}
			var removed []string
			for _, state := range repo.listItemStates {
				switch {
				case !state.RemotePresent && !state.LocalPresent:
					removed = append(removed, state.MediaItemID)
				case !state.RemotePresent || !state.LocalPresent:
					t.Fatalf("state = %#v, want unchanged or removed on both sides", state)
				}
			}
			slices.Sort(removed)
			if !slices.Equal(removed, tt.wantRemoved) {
				t.Fatalf("removed = %v, want %v", removed, tt.wantRemoved)
			}
		})
	}
}

// The matcher finds no single item for an id two library items carry, but the
// provider still lists it, so a complete import must not read either copy as
// removed remotely.
func TestServiceFullFavoriteImportKeepsAmbiguousKey(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	ctx := context.Background()
	// Export stored both copies under the IMDb key; Trakt keys shows by TVDB id.
	for _, id := range []string{"series:tvdb:81189", "series:imdb:tt0903747"} {
		if _, err := store.AddFavoriteAt(ctx, conn.ProfileID, id, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		repo.listItemStates = append(repo.listItemStates, ListItemState{
			ConnectionID: conn.ID, ListKind: ListKindFavorites, MediaItemID: id, Kind: "series",
			ProviderItemKey: "imdb:tt0903747", RemotePresent: true, LocalPresent: true,
		})
	}
	service := NewService(repo, NewRegistry()).
		WithMatcher(unmatchedMatcherStub{reason: "ambiguous tvdb_id match"}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	provider := favoriteBatchProviderStub{batch: FavoriteImportBatch{
		Rows: []RemoteFavorite{{ProviderItemKey: "tvdb:81189", Kind: "series", IMDbID: "tt0903747", TVDBID: "81189"}},
	}}
	result, err := service.importList(ctx, conn, ServerConfig{}, provider, service.favoritesBinding())
	if err != nil {
		t.Fatal(err)
	}
	favorites, err := store.ListFavorites(ctx, conn.ProfileID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 0 || result.Unmatched != 1 || len(favorites) != 2 {
		t.Fatalf("result=%#v favorites=%#v", result, favorites)
	}
	for _, state := range repo.listItemStates {
		if !state.RemotePresent || !state.LocalPresent {
			t.Fatalf("state = %#v, want unchanged", state)
		}
	}
}

// Unfavoriting one copy of a title while another copy holds the key leaves
// the key on the provider. An import must not favorite that copy again from
// the listing the removal is waiting on, but a newer listing is a new add.
func TestServiceFavoriteImportAndPendingLocalRemoval(t *testing.T) {
	removedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		listedAt  time.Time
		guessed   bool
		wantAdded bool
	}{
		{name: "listing older than the removal", listedAt: removedAt.Add(-24 * time.Hour)},
		{name: "listing newer than the removal", listedAt: removedAt.Add(time.Hour), wantAdded: true},
		// A provider that sends no listing time gets the import time.
		{name: "undated listing", listedAt: removedAt.Add(time.Hour), guessed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, repo, conn := favoriteImportFixture(t)
			ctx := context.Background()
			if _, err := store.AddFavoriteAt(ctx, conn.ProfileID, "movie-b", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			repo.listItemStates = []ListItemState{
				{
					ConnectionID: conn.ID, ListKind: ListKindFavorites, MediaItemID: "movie-a", Kind: "movie",
					ProviderItemKey: "imdb:tt0137523", RemotePresent: true, LastRemovedLocalAt: &removedAt,
				},
				{
					ConnectionID: conn.ID, ListKind: ListKindFavorites, MediaItemID: "movie-b", Kind: "movie",
					ProviderItemKey: "imdb:tt0137523", RemotePresent: true, LocalPresent: true,
				},
			}
			// The provider's row carries a TMDB id only movie-a has, so it matches a.
			service := NewService(repo, NewRegistry()).
				WithMatcher(matchedMatcherStub{mediaItemID: "movie-a"}).
				WithUserStoreProvider(staticStoreProvider{store: store})
			provider := favoriteBatchProviderStub{batch: FavoriteImportBatch{
				Rows: []RemoteFavorite{{
					ProviderItemKey: "imdb:tt0137523", Kind: "movie", IMDbID: "tt0137523", TMDBID: "550",
					FavoritedAt: tt.listedAt, FavoritedAtGuessed: tt.guessed,
				}},
			}}
			result, err := service.importList(ctx, conn, ServerConfig{}, provider, service.favoritesBinding())
			if err != nil {
				t.Fatal(err)
			}
			favorites, err := store.ListFavorites(ctx, conn.ProfileID, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			wantFavorites := 1
			if tt.wantAdded {
				wantFavorites = 2
			}
			if result.Removed != 0 || len(favorites) != wantFavorites {
				t.Fatalf("result=%#v favorites=%#v", result, favorites)
			}
			if a := repo.listItemStates[0]; a.LocalPresent != tt.wantAdded || !a.RemotePresent {
				t.Fatalf("movie-a state = %#v, want local %v", a, tt.wantAdded)
			}
		})
	}
}

// A complete read that lists only a movie does not keep a series with the
// same TMDB id; a stored row without a kind is kept by any listing.
func TestServiceFullFavoriteImportScopesPresenceByKind(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	ctx := context.Background()
	for id, kind := range map[string]string{"series:tmdb:550": "series", "unknown:tmdb:550": ""} {
		if _, err := store.AddFavoriteAt(ctx, conn.ProfileID, id, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		repo.listItemStates = append(repo.listItemStates, ListItemState{
			ConnectionID: conn.ID, ListKind: ListKindFavorites, MediaItemID: id, Kind: kind,
			ProviderItemKey: "tmdb:550", RemotePresent: true, LocalPresent: true,
		})
	}
	// The movie is not in the library, so it matches nothing.
	service := NewService(repo, NewRegistry()).
		WithMatcher(unmatchedMatcherStub{reason: "no tmdb_id match"}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	provider := favoriteBatchProviderStub{batch: FavoriteImportBatch{
		Rows: []RemoteFavorite{{ProviderItemKey: "tmdb:550", Kind: "movie", TMDBID: "550"}},
	}}
	result, err := service.importList(ctx, conn, ServerConfig{}, provider, service.favoritesBinding())
	if err != nil {
		t.Fatal(err)
	}
	favorites, err := store.ListFavorites(ctx, conn.ProfileID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 1 || len(favorites) != 1 || favorites[0].MediaItemID != "unknown:tmdb:550" {
		t.Fatalf("result=%#v favorites=%#v", result, favorites)
	}
}

// A pending local removal waits on the provider's listing. When an incremental
// read removes that listing and then lists the item again, the second row is a
// new add, even without a listing time.
func TestServiceIncrementalFavoriteRemoveThenAddReplacesPendingRemoval(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	ctx := context.Background()
	removedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	repo.listItemStates = []ListItemState{{
		ConnectionID: conn.ID, ListKind: ListKindFavorites, MediaItemID: testMovieMediaID, Kind: "movie",
		ProviderItemKey: testMovieProviderItemKey, RemotePresent: true, LastRemovedLocalAt: &removedAt,
	}}
	service := NewService(repo, NewRegistry()).
		WithMatcher(matchedMatcherStub{mediaItemID: testMovieMediaID}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	provider := favoriteBatchProviderStub{batch: FavoriteImportBatch{
		Rows: []RemoteFavorite{
			{ProviderItemKey: testMovieProviderItemKey, Removed: true},
			{ProviderItemKey: testMovieProviderItemKey, Kind: "movie", IMDbID: "tt0133093", FavoritedAt: time.Now().UTC(), FavoritedAtGuessed: true},
		},
		Incremental: true,
	}}
	result, err := service.importList(ctx, conn, ServerConfig{}, provider, service.favoritesBinding())
	if err != nil {
		t.Fatal(err)
	}
	favorites, err := store.ListFavorites(ctx, conn.ProfileID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || len(favorites) != 1 {
		t.Fatalf("result=%#v favorites=%#v", result, favorites)
	}
	if state := repo.listItemStates[0]; !state.LocalPresent || !state.RemotePresent {
		t.Fatalf("state = %#v, want listed on both sides", state)
	}
}

type favoriteRemoverStub struct {
	removed [][]string
}

func (*favoriteRemoverStub) Key() string         { return "plugin:4:list" }
func (*favoriteRemoverStub) DisplayName() string { return "List" }
func (*favoriteRemoverStub) AuthMethod() string  { return AuthMethodAPIKey }
func (*favoriteRemoverStub) Capabilities() Capabilities {
	return Capabilities{RemoveFavorites: true}
}
func (p *favoriteRemoverStub) RemoveFavorites(_ context.Context, _ ServerConfig, _ Connection, favorites []LocalFavorite) (ExportResult, error) {
	ids := make([]string, 0, len(favorites))
	for _, favorite := range favorites {
		ids = append(ids, favorite.MediaItemID)
	}
	p.removed = append(p.removed, ids)
	return ExportResult{Sent: ids}, nil
}

func TestServiceKeepsSharedKeyOnProviderWhileAnotherFavoriteHoldsIt(t *testing.T) {
	ctx := context.Background()
	repo := newServiceFakeRepo()
	conn := Connection{
		ID: "conn-1", Provider: "plugin:4:list", UserID: 7, ProfileID: "profile-1",
		ExportFavoritesEnabled: true, SyncFavoriteRemovalsEnabled: true,
	}
	repo.connections[connectionKey(conn.Provider, conn.UserID, conn.ProfileID)] = conn
	repo.listItemStates = []ListItemState{
		{ConnectionID: conn.ID, ListKind: ListKindFavorites, MediaItemID: "movie-a", Kind: "movie", ProviderItemKey: "imdb:tt0137523", RemotePresent: true, LocalPresent: true},
		{ConnectionID: conn.ID, ListKind: ListKindFavorites, MediaItemID: "movie-b", Kind: "movie", ProviderItemKey: "imdb:tt0137523", RemotePresent: true, LocalPresent: true},
	}
	provider := &favoriteRemoverStub{}
	reg := registryWith(t, provider)
	service := NewService(repo, reg)

	// Unfavoriting one of two items with the same key leaves the key on the
	// provider's list, from the realtime event and from the scheduled pass.
	err := service.processLocalListEvent(ctx, LocalListEvent{
		List: ListKindFavorites, Change: ListChangeRemoved, UserID: conn.UserID, ProfileID: conn.ProfileID,
		Items: []LocalFavorite{{MediaItemID: "movie-a", Kind: "movie", IMDbID: "tt0137523"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := service.removePendingListItems(ctx, conn, ServerConfig{}, provider, service.favoritesBinding())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 || len(provider.removed) != 0 {
		t.Fatalf("removed=%d provider calls=%v, want none while movie-b holds the key", removed, provider.removed)
	}
	if state := repo.listItemStates[0]; state.LocalPresent || !state.RemotePresent {
		t.Fatalf("movie-a state = %#v, want pending removal", state)
	}

	// Once the last item holding the key leaves the list, the key goes.
	err = service.processLocalListEvent(ctx, LocalListEvent{
		List: ListKindFavorites, Change: ListChangeRemoved, UserID: conn.UserID, ProfileID: conn.ProfileID,
		Items: []LocalFavorite{{MediaItemID: "movie-b", Kind: "movie", IMDbID: "tt0137523"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	removed, err = service.removePendingListItems(ctx, conn, ServerConfig{}, provider, service.favoritesBinding())
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"movie-b"}, {"movie-a"}}; !reflect.DeepEqual(provider.removed, want) || removed != 1 {
		t.Fatalf("removed=%d provider calls=%v, want %v", removed, provider.removed, want)
	}
	for _, state := range repo.listItemStates {
		if state.RemotePresent || state.LocalPresent {
			t.Fatalf("state = %#v, want removed on both sides", state)
		}
	}
}

func (r *serviceFakeRepo) UpdateConnectionSettings(ctx context.Context, provider string, userID int, profileID string, expected *ConnectionVersion, update ConnectionUpdate, before func(Connection) error) (Connection, error) {
	current, ok, err := r.GetConnection(ctx, provider, userID, profileID)
	if err != nil {
		return Connection{}, err
	}
	if !ok {
		return Connection{}, ErrConnectionNotFound
	}
	if expected != nil && (current.ID != expected.ID || !current.UpdatedAt.Equal(expected.UpdatedAt)) {
		return Connection{}, ErrStaleConnection
	}
	if before != nil {
		if err := before(current); err != nil {
			return Connection{}, err
		}
	}
	if update.ImportWatchedEnabled != nil {
		current.ImportWatchedEnabled = *update.ImportWatchedEnabled
	}
	if update.ImportProgressEnabled != nil {
		current.ImportProgressEnabled = *update.ImportProgressEnabled
	}
	if update.ExportWatchedEnabled != nil {
		current.ExportWatchedEnabled = *update.ExportWatchedEnabled
	}
	if update.ExportUnwatchedEnabled != nil {
		current.ExportUnwatchedEnabled = *update.ExportUnwatchedEnabled
	}
	if update.ImportFavoritesEnabled != nil {
		current.ImportFavoritesEnabled = *update.ImportFavoritesEnabled
	}
	if update.ExportFavoritesEnabled != nil {
		current.ExportFavoritesEnabled = *update.ExportFavoritesEnabled
	}
	if update.SyncFavoriteRemovalsEnabled != nil {
		current.SyncFavoriteRemovalsEnabled = *update.SyncFavoriteRemovalsEnabled
	}
	if update.ImportWatchlistEnabled != nil {
		current.ImportWatchlistEnabled = *update.ImportWatchlistEnabled
	}
	if update.ExportWatchlistEnabled != nil {
		current.ExportWatchlistEnabled = *update.ExportWatchlistEnabled
	}
	if update.SyncWatchlistRemovalsEnabled != nil {
		current.SyncWatchlistRemovalsEnabled = *update.SyncWatchlistRemovalsEnabled
	}
	if update.SyncWatchlistOrderEnabled != nil {
		current.SyncWatchlistOrderEnabled = *update.SyncWatchlistOrderEnabled
	}
	if update.ScrobbleEnabled != nil {
		current.ScrobbleEnabled = *update.ScrobbleEnabled
	}
	if len(update.ConnectionSettings) > 0 {
		merged := cloneStringMapForTest(current.ConnectionSettings)
		for key, value := range update.ConnectionSettings {
			merged[key] = encodeConnectionSettingValue(value)
		}
		current.ConnectionSettings = merged
	}
	return r.UpsertConnection(ctx, current)
}

func TestExportItemOutcomeDecidesByMediaItemIDBeforeKey(t *testing.T) {
	// A movie and a show share the key tmdb:550; the provider reports the
	// movie missing and the show sent.
	result := ExportResult{
		Sent:     []string{"show-1", "tmdb:550"},
		NotFound: []string{"movie-1", "tmdb:550"},
	}
	if sent, notFound := exportItemOutcome(result, "movie-1", "tmdb:550"); sent || !notFound {
		t.Fatalf("movie outcome = sent %v notFound %v, want not found", sent, notFound)
	}
	if sent, notFound := exportItemOutcome(result, "show-1", "tmdb:550"); !sent || notFound {
		t.Fatalf("show outcome = sent %v notFound %v, want sent", sent, notFound)
	}
	// A result that names items only by key still works.
	if sent, _ := exportItemOutcome(ExportResult{Sent: []string{"imdb:tt1"}}, "movie-2", "imdb:tt1"); !sent {
		t.Fatal("a key-only result must still confirm the item")
	}
	if sent, _ := exportItemOutcome(ExportResult{Failed: map[string]string{"movie-3": "x"}, Sent: []string{"imdb:tt3"}}, "movie-3", "imdb:tt3"); sent {
		t.Fatal("a failed media item must not be confirmed through its key")
	}
}
