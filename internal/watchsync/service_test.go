package watchsync

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type serviceFakeRepo struct {
	connections         map[string]Connection
	dueConnections      []Connection
	sessions            map[string]DeviceAuthSession
	settings            map[string]string
	syncRuns            []SyncRun
	historyExports      []HistoryExport
	listItemStates      []ListItemState
	scrobbleConnections []Connection
	scrobbleSessions    []ScrobbleSession
	scrobbleUpdates     []scrobbleUpdate
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
		connections: make(map[string]Connection),
		sessions:    make(map[string]DeviceAuthSession),
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
	for i := len(r.syncRuns) - 1; i >= 0; i-- {
		if r.syncRuns[i].ConnectionID == connectionID {
			return r.syncRuns[i], true, nil
		}
	}
	return SyncRun{}, false, nil
}

func (r *serviceFakeRepo) GetActiveSyncRun(_ context.Context, connectionID string) (SyncRun, bool, error) {
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
		switch kind {
		case LocalWatchEventMarkedWatched:
			if conn.ExportWatchedEnabled {
				conns = append(conns, cloneConnectionForTest(conn))
			}
		case LocalWatchEventMarkedUnwatched:
			if conn.ExportUnwatchedEnabled {
				conns = append(conns, cloneConnectionForTest(conn))
			}
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
				export.ID = r.historyExports[i].ID
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

func (r *serviceFakeRepo) ListPendingHistoryExports(_ context.Context, connectionID string, limit int) ([]HistoryExport, error) {
	var exports []HistoryExport
	for _, export := range r.historyExports {
		if export.ConnectionID == connectionID && export.Status == "pending" {
			exports = append(exports, export)
			if limit > 0 && len(exports) >= limit {
				break
			}
		}
	}
	return exports, nil
}

func (r *serviceFakeRepo) MarkHistoryExportStatus(_ context.Context, id string, status string, lastError string) error {
	for i := range r.historyExports {
		if r.historyExports[i].ID == id {
			r.historyExports[i].Status = status
			r.historyExports[i].LastError = lastError
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
		if state.ConnectionID == connectionID && state.ListKind == kind && !state.LocalPresent && state.RemotePresent && state.LastError == "" {
			states = append(states, state)
			if limit > 0 && len(states) >= limit {
				break
			}
		}
	}
	return states, nil
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

func (r *serviceFakeRepo) ListScrobbleConnections(_ context.Context, _ int, _ string) ([]Connection, error) {
	conns := make([]Connection, 0, len(r.scrobbleConnections))
	for _, conn := range r.scrobbleConnections {
		conns = append(conns, cloneConnectionForTest(conn))
	}
	return conns, nil
}

func (r *serviceFakeRepo) UpsertScrobbleSession(_ context.Context, _ ScrobbleEvent, _ string, _ string) error {
	return nil
}

func (r *serviceFakeRepo) UpdateScrobbleSession(_ context.Context, playbackSessionID string, connectionID string, action string, positionSeconds float64, historyID string, lastError string, stopSentAt *time.Time) error {
	r.scrobbleUpdates = append(r.scrobbleUpdates, scrobbleUpdate{
		playbackSessionID: playbackSessionID,
		connectionID:      connectionID,
		action:            action,
		positionSeconds:   positionSeconds,
		historyID:         historyID,
		lastError:         lastError,
		stopSentAt:        stopSentAt,
	})
	return nil
}

func (r *serviceFakeRepo) ListOpenScrobbleSessions(_ context.Context) ([]ScrobbleSession, error) {
	return r.scrobbleSessions, nil
}

func connectionKey(provider string, userID int, profileID string) string {
	return provider + "|" + strconv.Itoa(userID) + "|" + profileID
}

func cloneConnectionForTest(conn Connection) Connection {
	conn.SyncCursors = cloneStringMapForTest(conn.SyncCursors)
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
	refreshed     bool
	refreshTokens TokenSet
	refreshErr    error
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
	expires := time.Now().Add(time.Hour)
	return TokenSet{AccessToken: "access", RefreshToken: "refresh", TokenExpiresAt: &expires}, nil
}

func (p *authProviderStub) RefreshToken(context.Context, ServerConfig, Connection) (TokenSet, error) {
	p.refreshed = true
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

type watchedImporterStub struct {
	key    string
	source userstore.WatchHistorySource
	rows   []RemoteWatch
}

func (p watchedImporterStub) Key() string {
	if p.key != "" {
		return p.key
	}
	return "trakt"
}

func (p watchedImporterStub) DisplayName() string {
	return "Trakt"
}

func (p watchedImporterStub) Capabilities() Capabilities {
	return Capabilities{ImportWatched: true}
}

func (p watchedImporterStub) FetchWatched(context.Context, ServerConfig, Connection) ([]RemoteWatch, error) {
	return p.rows, nil
}

func (p watchedImporterStub) HistorySource() userstore.WatchHistorySource {
	if p.source == "" {
		return userstore.WatchHistorySourceTrakt
	}
	return p.source
}

type watchedBatchImporterStub struct {
	watchedImporterStub
	batch WatchedImportBatch
}

func (p watchedBatchImporterStub) FetchWatchedBatch(context.Context, ServerConfig, Connection) (WatchedImportBatch, error) {
	return p.batch, nil
}

type progressImporterStub struct {
	rows []RemoteProgress
}

func (p progressImporterStub) FetchProgress(context.Context, ServerConfig, Connection) ([]RemoteProgress, error) {
	return p.rows, nil
}

type progressBatchImporterStub struct {
	progressImporterStub
	batch ProgressImportBatch
}

func (p progressBatchImporterStub) FetchProgressBatch(context.Context, ServerConfig, Connection) (ProgressImportBatch, error) {
	return p.batch, nil
}

type watchedExporterStub struct {
	exportErr error
	key       string
	source    userstore.WatchHistorySource
}

func (p watchedExporterStub) Key() string {
	if p.key != "" {
		return p.key
	}
	return "trakt"
}

func (p watchedExporterStub) DisplayName() string {
	return "Trakt"
}

func (p watchedExporterStub) Capabilities() Capabilities {
	return Capabilities{ExportWatched: true}
}

func (p watchedExporterStub) FetchHistory(context.Context, ServerConfig, Connection) ([]RemotePlay, error) {
	return nil, nil
}

func (p watchedExporterStub) ExportHistory(context.Context, ServerConfig, Connection, []LocalPlay) (ExportResult, error) {
	if p.exportErr != nil {
		return ExportResult{}, p.exportErr
	}
	return ExportResult{}, nil
}

func (p watchedExporterStub) HistorySource() userstore.WatchHistorySource {
	if p.source == "" {
		return userstore.WatchHistorySourceTrakt
	}
	return p.source
}

type watchedImportExportStub struct {
	key       string
	source    userstore.WatchHistorySource
	rows      []RemoteWatch
	exportErr error
}

func (p watchedImportExportStub) Key() string {
	if p.key != "" {
		return p.key
	}
	return "trakt"
}

func (p watchedImportExportStub) DisplayName() string {
	return "Trakt"
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

func (p watchedImportExportStub) HistorySource() userstore.WatchHistorySource {
	if p.source == "" {
		return userstore.WatchHistorySourceTrakt
	}
	return p.source
}

type scrobblerStub struct {
	stopErr       error
	refreshed     bool
	refreshTokens TokenSet
	refreshErr    error
	stopConns     chan Connection
	stopEvents    chan ScrobbleEvent
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

func (p scrobblerStub) Start(context.Context, ServerConfig, Connection, ScrobbleEvent) error {
	return nil
}

func (p scrobblerStub) Pause(context.Context, ServerConfig, Connection, ScrobbleEvent) error {
	return nil
}

func (p scrobblerStub) Stop(_ context.Context, _ ServerConfig, conn Connection, event ScrobbleEvent) error {
	if p.stopConns != nil {
		p.stopConns <- conn
	}
	if p.stopEvents != nil {
		p.stopEvents <- event
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

type noOpWatchState struct{}

func (noOpWatchState) RecordImportedHistoryWithSource(
	context.Context,
	int,
	string,
	string,
	float64,
	bool,
	*time.Time,
	userstore.WatchHistorySource,
) (bool, error) {
	return false, nil
}

func (noOpWatchState) RecordImportedWatchIfNewerWithSource(
	context.Context,
	int,
	string,
	string,
	float64,
	float64,
	bool,
	time.Time,
	*time.Time,
	userstore.WatchHistorySource,
) (bool, error) {
	return false, nil
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

func (s *recordingWatchState) RecordImportedHistoryWithSource(
	_ context.Context,
	_ int,
	_ string,
	_ string,
	_ float64,
	_ bool,
	_ *time.Time,
	source userstore.WatchHistorySource,
) (bool, error) {
	s.sources = append(s.sources, source)
	return true, nil
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
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
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
	if conn.ProviderUsername != "alex" || conn.AccessToken != "access" {
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

func TestServiceStartsAndPollsDeviceAuthRejectsMismatchedSession(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := &authProviderStub{}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
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
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
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
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
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

func TestServicePollDeviceAuthPreservesExistingConnectionToggles(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := &authProviderStub{}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	service := NewService(repo, reg)

	existing := Connection{
		ID:                    "existing-conn",
		Provider:              "trakt",
		UserID:                7,
		ProfileID:             "profile-1",
		ImportWatchedEnabled:  false,
		ImportProgressEnabled: false,
		ExportWatchedEnabled:  true,
		ScrobbleEnabled:       true,
	}
	repo.connections[connectionKey("trakt", 7, "profile-1")] = existing
	repo.sessions["auth-1"] = DeviceAuthSession{
		ID:        "auth-1",
		Provider:  "trakt",
		UserID:    7,
		ProfileID: "profile-1",
		ExpiresAt: time.Now().Add(time.Minute),
	}

	conn, err := service.PollDeviceAuth(context.Background(), 7, "profile-1", "trakt", "auth-1")
	if err != nil {
		t.Fatalf("PollDeviceAuth: %v", err)
	}
	if conn.ID != existing.ID {
		t.Fatalf("connection ID = %q, want existing ID %q", conn.ID, existing.ID)
	}
	if conn.ImportWatchedEnabled || conn.ImportProgressEnabled ||
		!conn.ExportWatchedEnabled || !conn.ScrobbleEnabled {
		t.Fatalf("connection toggles were not preserved: %+v", conn)
	}
	if conn.AccessToken != "access" || conn.ProviderUsername != "alex" {
		t.Fatalf("connection credentials/account were not refreshed: %+v", conn)
	}
}

func TestServiceRequestManualSyncCreatesAsyncRun(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := &authProviderStub{}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	service := NewService(repo, reg)
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	repo.connections[connectionKey("trakt", 7, "profile-1")] = Connection{
		ID:        "conn-1",
		Provider:  "trakt",
		UserID:    7,
		ProfileID: "profile-1",
	}

	result, err := service.RequestManualSync(context.Background(), 7, "profile-1", "trakt")
	if err != nil {
		t.Fatalf("RequestManualSync: %v", err)
	}
	if result.Run.ID == "" || result.Run.Status != string(SyncRunStatusRunning) {
		t.Fatalf("run = %+v, want running run", result.Run)
	}
	if result.RetryAfterSeconds != 0 {
		t.Fatalf("retry after = %d, want 0", result.RetryAfterSeconds)
	}

	deadline := time.Now().Add(time.Second)
	for {
		latest, ok, err := repo.GetLatestSyncRun(context.Background(), "conn-1")
		if err != nil {
			t.Fatalf("GetLatestSyncRun: %v", err)
		}
		if ok && latest.Status == string(SyncRunStatusSuccess) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync run did not complete: %+v", repo.syncRuns)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServiceRequestManualSyncReturnsActiveRun(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := &authProviderStub{}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
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
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
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

func TestServiceConnectionStatusRejectsBlankAccessToken(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := &authProviderStub{}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	repo.connections[connectionKey("trakt", 7, "profile-1")] = Connection{
		ID:          "conn-1",
		Provider:    "trakt",
		UserID:      7,
		ProfileID:   "profile-1",
		AccessToken: "   ",
	}

	status, err := NewService(repo, reg).GetConnectionStatus(context.Background(), 7, "profile-1", "trakt")
	if err != nil {
		t.Fatalf("GetConnectionStatus: %v", err)
	}
	if status.Connected {
		t.Fatalf("status = %+v, want disconnected for blank access token", status)
	}
	if status.LastError == "" {
		t.Fatalf("status = %+v, want reconnect error", status)
	}
}

func TestServiceSyncConnectionRejectsBlankAccessToken(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := watchedExporterStub{}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
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
	repo := newServiceFakeRepo()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(-time.Minute)
	refreshedExpiresAt := now.Add(time.Hour)
	provider := &authProviderStub{
		refreshTokens: TokenSet{
			AccessToken:    "new-access",
			RefreshToken:   "new-refresh",
			TokenExpiresAt: &refreshedExpiresAt,
		},
	}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	service := NewService(repo, reg)
	service.now = func() time.Time { return now }
	conn := Connection{
		ID:             "conn-1",
		Provider:       "trakt",
		UserID:         7,
		ProfileID:      "profile-1",
		AccessToken:    "old-access",
		RefreshToken:   "old-refresh",
		TokenExpiresAt: &expiresAt,
	}
	repo.connections[connectionKey("trakt", 7, "profile-1")] = conn

	if err := service.SyncConnection(context.Background(), conn, "scheduled"); err != nil {
		t.Fatalf("SyncConnection: %v", err)
	}
	if !provider.refreshed {
		t.Fatal("provider was not asked to refresh the expired token")
	}
	updated := repo.connections[connectionKey("trakt", 7, "profile-1")]
	if updated.AccessToken != "new-access" || updated.RefreshToken != "new-refresh" {
		t.Fatalf("connection tokens = %q/%q, want refreshed tokens", updated.AccessToken, updated.RefreshToken)
	}
	if updated.TokenExpiresAt == nil || !updated.TokenExpiresAt.Equal(refreshedExpiresAt) {
		t.Fatalf("token expiry = %v, want %v", updated.TokenExpiresAt, refreshedExpiresAt)
	}
}

func TestServiceSyncConnectionTreatsMatcherWarningsAsSuccess(t *testing.T) {
	repo := newServiceFakeRepo()
	watchedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	provider := watchedImporterStub{rows: []RemoteWatch{{
		Provider:      "trakt",
		Kind:          "movie",
		Title:         "Ghost Hunters",
		Year:          2019,
		LastWatchedAt: &watchedAt,
	}}}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	service := NewService(repo, reg).
		WithMatcher(unmatchedMatcherStub{reason: `no tmdb_id match for "92820"`}).
		WithWatchState(noOpWatchState{})
	conn := Connection{
		ID:                   "conn-1",
		Provider:             "trakt",
		UserID:               7,
		ProfileID:            "profile-1",
		AccessToken:          "access",
		ImportWatchedEnabled: true,
	}

	if err := service.SyncConnection(context.Background(), conn, "scheduled"); err != nil {
		t.Fatalf("SyncConnection: %v", err)
	}
	latest, ok, err := repo.GetLatestSyncRun(context.Background(), "conn-1")
	if err != nil {
		t.Fatalf("GetLatestSyncRun: %v", err)
	}
	if !ok || latest.Status != string(SyncRunStatusSuccess) {
		t.Fatalf("latest run = %+v, want success run", latest)
	}
	if latest.Warning == "" {
		t.Fatalf("latest run warning is empty: %+v", latest)
	}
}

func TestServiceImportWatchedUsesProviderHistorySource(t *testing.T) {
	repo := newServiceFakeRepo()
	watchedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	provider := watchedImporterStub{
		key:    "simkl",
		source: userstore.WatchHistorySourceSimkl,
		rows: []RemoteWatch{{
			Provider:      "simkl",
			Kind:          historyimport.KindMovie,
			Title:         "Inception",
			Year:          2010,
			LastWatchedAt: &watchedAt,
		}},
	}
	watchState := &recordingWatchState{}
	service := NewService(repo, NewRegistry()).
		WithMatcher(matchedMatcherStub{mediaItemID: "movie-1"}).
		WithWatchState(watchState)

	result, err := service.ImportWatched(context.Background(), Connection{
		ID:        "conn-1",
		Provider:  "simkl",
		UserID:    7,
		ProfileID: "profile-1",
	}, ServerConfig{}, provider)
	if err != nil {
		t.Fatalf("ImportWatched: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("imported = %d, want 1", result.Imported)
	}
	if len(watchState.sources) != 1 || watchState.sources[0] != userstore.WatchHistorySourceSimkl {
		t.Fatalf("recorded sources = %+v, want simkl", watchState.sources)
	}
	if len(watchState.targetIDs) != 1 || watchState.targetIDs[0] != "movie-1" {
		t.Fatalf("recorded target ids = %+v, want movie-1", watchState.targetIDs)
	}
	if len(watchState.completed) != 1 || !watchState.completed[0] {
		t.Fatalf("recorded completed flags = %+v, want true", watchState.completed)
	}
	if len(watchState.positions) != 1 || watchState.positions[0] != 0 {
		t.Fatalf("recorded positions = %+v, want 0", watchState.positions)
	}
	if len(watchState.updatedAt) != 1 || !watchState.updatedAt[0].Equal(watchedAt) {
		t.Fatalf("recorded updated_at = %+v, want %v", watchState.updatedAt, watchedAt)
	}
	if len(watchState.watchedAt) != 1 || watchState.watchedAt[0] == nil || !watchState.watchedAt[0].Equal(watchedAt) {
		t.Fatalf("recorded watched_at = %+v, want %v", watchState.watchedAt, watchedAt)
	}
}

func TestServiceImportWatchedSkipsRowsWithoutLastWatchedAt(t *testing.T) {
	repo := newServiceFakeRepo()
	provider := watchedImporterStub{
		rows: []RemoteWatch{{
			Provider: "trakt",
			Kind:     historyimport.KindMovie,
			Title:    "Inception",
			Year:     2010,
		}},
	}
	watchState := &recordingWatchState{}
	service := NewService(repo, NewRegistry()).
		WithMatcher(matchedMatcherStub{mediaItemID: "movie-1"}).
		WithWatchState(watchState)

	result, err := service.ImportWatched(context.Background(), Connection{
		ID:        "conn-1",
		Provider:  "trakt",
		UserID:    7,
		ProfileID: "profile-1",
	}, ServerConfig{}, provider)
	if err != nil {
		t.Fatalf("ImportWatched: %v", err)
	}
	if result.Found != 1 || result.Imported != 0 {
		t.Fatalf("result = %+v, want found row skipped with no import", result)
	}
	if len(watchState.targetIDs) != 0 {
		t.Fatalf("watch state calls = %+v, want none", watchState.targetIDs)
	}
}

func TestServiceImportWatchedPersistsBatchCursorsAndWarnings(t *testing.T) {
	repo := newServiceFakeRepo()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	provider := watchedBatchImporterStub{
		watchedImporterStub: watchedImporterStub{key: "simkl", source: userstore.WatchHistorySourceSimkl},
		batch: WatchedImportBatch{
			UpdatedCursors: map[string]string{"simkl.inbound.movies.completed": "2026-05-04T11:00:00Z"},
			Warnings:       []string{"simkl removed_from_list changed; removals are not imported"},
		},
	}
	service := NewService(repo, NewRegistry()).
		WithMatcher(unmatchedMatcherStub{}).
		WithWatchState(noOpWatchState{})
	service.now = func() time.Time { return now }

	result, err := service.ImportWatched(context.Background(), Connection{
		ID:          "conn-1",
		Provider:    "simkl",
		UserID:      7,
		ProfileID:   "profile-1",
		SyncCursors: map[string]string{"existing": "cursor"},
	}, ServerConfig{}, provider)
	if err != nil {
		t.Fatalf("ImportWatched: %v", err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "simkl removed_from_list changed; removals are not imported" {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	updated := repo.connections[connectionKey("simkl", 7, "profile-1")]
	if updated.LastInboundSyncAt == nil || !updated.LastInboundSyncAt.Equal(now) {
		t.Fatalf("last inbound sync = %v, want %v", updated.LastInboundSyncAt, now)
	}
	if updated.SyncCursors["existing"] != "cursor" ||
		updated.SyncCursors["simkl.inbound.movies.completed"] != "2026-05-04T11:00:00Z" {
		t.Fatalf("sync cursors = %+v", updated.SyncCursors)
	}
}

func TestServiceImportWatchedLegacyImporterStillSetsLastSyncTimestamp(t *testing.T) {
	repo := newServiceFakeRepo()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	service := NewService(repo, NewRegistry()).
		WithMatcher(unmatchedMatcherStub{}).
		WithWatchState(noOpWatchState{})
	service.now = func() time.Time { return now }

	_, err := service.ImportWatched(context.Background(), Connection{
		ID:        "conn-1",
		Provider:  "trakt",
		UserID:    7,
		ProfileID: "profile-1",
	}, ServerConfig{}, watchedImporterStub{})
	if err != nil {
		t.Fatalf("ImportWatched: %v", err)
	}
	updated := repo.connections[connectionKey("trakt", 7, "profile-1")]
	if updated.LastInboundSyncAt == nil || !updated.LastInboundSyncAt.Equal(now) {
		t.Fatalf("last inbound sync = %v, want %v", updated.LastInboundSyncAt, now)
	}
	if len(updated.SyncCursors) != 0 {
		t.Fatalf("sync cursors = %+v, want empty for legacy importer", updated.SyncCursors)
	}
}

func TestServiceImportProgressPersistsBatchCursorsAndWarnings(t *testing.T) {
	repo := newServiceFakeRepo()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	service := NewService(repo, NewRegistry()).
		WithMatcher(unmatchedMatcherStub{}).
		WithUserStoreProvider(staticStoreProvider{})
	service.now = func() time.Time { return now }
	provider := progressBatchImporterStub{
		batch: ProgressImportBatch{
			UpdatedCursors: map[string]string{"simkl.progress.movies": "2026-05-04T11:30:00Z"},
			Warnings:       []string{"simkl playback movie skipped because it has no usable external id"},
		},
	}

	result, err := service.ImportProgress(context.Background(), Connection{
		ID:          "conn-1",
		Provider:    "simkl",
		UserID:      7,
		ProfileID:   "profile-1",
		SyncCursors: map[string]string{"existing": "cursor"},
	}, ServerConfig{}, provider)
	if err != nil {
		t.Fatalf("ImportProgress: %v", err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "simkl playback movie skipped because it has no usable external id" {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	updated := repo.connections[connectionKey("simkl", 7, "profile-1")]
	if updated.LastProgressSyncAt == nil || !updated.LastProgressSyncAt.Equal(now) {
		t.Fatalf("last progress sync = %v, want %v", updated.LastProgressSyncAt, now)
	}
	if updated.SyncCursors["existing"] != "cursor" ||
		updated.SyncCursors["simkl.progress.movies"] != "2026-05-04T11:30:00Z" {
		t.Fatalf("sync cursors = %+v", updated.SyncCursors)
	}
}

func TestServiceSyncConnectionPreservesConnectionUpdatesAcrossFlows(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := userdb.AddHistory(db, userstore.WatchHistoryEntry{
		ID:              "history-1",
		ProfileID:       "profile-1",
		MediaItemID:     "movie-1",
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
		key:    "simkl",
		source: userstore.WatchHistorySourceSimkl,
		rows: []RemoteWatch{{
			Provider:      "simkl",
			Kind:          historyimport.KindMovie,
			Title:         "Inception",
			Year:          2010,
			LastWatchedAt: &watchedAt,
		}},
	}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	repo := newServiceFakeRepo()
	service := NewService(repo, reg).
		WithMatcher(matchedMatcherStub{mediaItemID: "movie-1"}).
		WithWatchState(&recordingWatchState{}).
		WithUserStoreProvider(staticStoreProvider{store: userdb.NewSQLiteUserStore(db)})
	now := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	conn := Connection{
		ID:                   "conn-1",
		Provider:             "simkl",
		UserID:               7,
		ProfileID:            "profile-1",
		AccessToken:          "access",
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

func TestServiceExportWatchedDrainsPendingBatches(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	for i := range 101 {
		id := strconv.Itoa(i)
		if err := userdb.AddHistory(db, userstore.WatchHistoryEntry{
			ID:              "history-" + id,
			ProfileID:       "profile-1",
			MediaItemID:     "movie-" + id,
			WatchedAt:       "2026-05-04T12:00:00Z",
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourcePlayback,
			Identity: userstore.WatchIdentity{
				StableType:  "movie",
				ProviderIDs: map[string]string{"tmdb": "60" + id},
			},
		}); err != nil {
			t.Fatalf("AddHistory %d: %v", i, err)
		}
	}

	repo := newServiceFakeRepo()
	service := NewService(repo, NewRegistry()).WithUserStoreProvider(staticStoreProvider{
		store: userdb.NewSQLiteUserStore(db),
	})
	result, err := service.ExportWatched(context.Background(), Connection{
		ID:        "conn-1",
		Provider:  "simkl",
		UserID:    7,
		ProfileID: "profile-1",
	}, ServerConfig{}, watchedImportExportStub{key: "simkl", source: userstore.WatchHistorySourceSimkl})
	if err != nil {
		t.Fatalf("ExportWatched: %v", err)
	}
	if result.Sent != 101 {
		t.Fatalf("sent = %d, want 101 (result=%+v)", result.Sent, result)
	}
	for _, export := range repo.historyExports {
		if export.Status != "sent" {
			t.Fatalf("history exports = %+v, want all sent", repo.historyExports)
		}
	}
}

func TestServiceSyncConnectionMarksRunFailedWhenExportTransportFails(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := userdb.AddHistory(db, userstore.WatchHistoryEntry{
		ID:              "history-1",
		ProfileID:       "profile-1",
		MediaItemID:     "movie-1",
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
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	service := NewService(repo, reg).WithUserStoreProvider(staticStoreProvider{
		store: userdb.NewSQLiteUserStore(db),
	})
	conn := Connection{
		ID:                   "conn-1",
		Provider:             "trakt",
		UserID:               7,
		ProfileID:            "profile-1",
		AccessToken:          "access",
		ExportWatchedEnabled: true,
	}

	err = service.SyncConnection(context.Background(), conn, "scheduled")
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
	if len(repo.historyExports) != 1 || repo.historyExports[0].Status != "failed" {
		t.Fatalf("history exports = %+v, want one failed export", repo.historyExports)
	}
}

func TestServiceOrderedScrobblerDispatchesInQueueOrder(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.scrobbleConnections = []Connection{{
		ID:              "conn-1",
		Provider:        "simkl",
		UserID:          7,
		ProfileID:       "profile-1",
		ScrobbleEnabled: true,
	}}
	provider := newOrderedScrobblerStub()
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	service := NewService(repo, reg)
	event := ScrobbleEvent{
		PlaybackSessionID: "session-1",
		UserID:            7,
		ProfileID:         "profile-1",
		Kind:              historyimport.KindMovie,
		MediaItemID:       "movie-1",
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
	repo.scrobbleConnections = []Connection{{
		ID:              "conn-1",
		Provider:        "trakt",
		UserID:          7,
		ProfileID:       "profile-1",
		ScrobbleEnabled: true,
	}}
	provider := scrobblerStub{stopErr: errors.New("stop failed")}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	service := NewService(repo, reg)

	err := service.ScrobbleStop(context.Background(), ScrobbleEvent{
		PlaybackSessionID: "playback-1",
		UserID:            7,
		ProfileID:         "profile-1",
		MediaItemID:       "movie-1",
		HistoryID:         "history-1",
		PositionSeconds:   120,
	})
	if err != nil {
		t.Fatalf("ScrobbleStop: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		for _, update := range repo.scrobbleUpdates {
			if update.lastError == "stop failed" {
				for _, seen := range repo.scrobbleUpdates {
					if seen.stopSentAt != nil {
						t.Fatalf("stop_sent_at was set despite provider failure: %+v", repo.scrobbleUpdates)
					}
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for failed stop update: %+v", repo.scrobbleUpdates)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServiceScrobbleRefreshesExpiredTokenBeforeDispatch(t *testing.T) {
	repo := newServiceFakeRepo()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(-time.Minute)
	refreshedExpiresAt := now.Add(time.Hour)
	repo.scrobbleConnections = []Connection{{
		ID:              "conn-1",
		Provider:        "trakt",
		UserID:          7,
		ProfileID:       "profile-1",
		AccessToken:     "old-access",
		RefreshToken:    "old-refresh",
		TokenExpiresAt:  &expiresAt,
		ScrobbleEnabled: true,
	}}
	provider := &scrobblerStub{
		refreshTokens: TokenSet{
			AccessToken:    "new-access",
			RefreshToken:   "new-refresh",
			TokenExpiresAt: &refreshedExpiresAt,
		},
		stopConns: make(chan Connection, 1),
	}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	service := NewService(repo, reg)
	service.now = func() time.Time { return now }

	err := service.ScrobbleStop(context.Background(), ScrobbleEvent{
		PlaybackSessionID: "playback-1",
		UserID:            7,
		ProfileID:         "profile-1",
		MediaItemID:       "movie-1",
		HistoryID:         "history-1",
		PositionSeconds:   120,
	})
	if err != nil {
		t.Fatalf("ScrobbleStop: %v", err)
	}
	if !provider.refreshed {
		t.Fatal("provider was not asked to refresh the expired token")
	}
	updated := repo.connections[connectionKey("trakt", 7, "profile-1")]
	if updated.AccessToken != "new-access" || updated.RefreshToken != "new-refresh" {
		t.Fatalf("stored tokens = %q/%q, want refreshed tokens", updated.AccessToken, updated.RefreshToken)
	}

	select {
	case conn := <-provider.stopConns:
		if conn.AccessToken != "new-access" {
			t.Fatalf("scrobble used access token %q, want refreshed token", conn.AccessToken)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scrobble dispatch")
	}
}

func TestServiceSweepOpenScrobblesRetriesProviderStop(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.connections[connectionKey("trakt", 7, "profile-1")] = Connection{
		ID:              "conn-1",
		Provider:        "trakt",
		UserID:          7,
		ProfileID:       "profile-1",
		AccessToken:     "access",
		ScrobbleEnabled: true,
	}
	repo.scrobbleSessions = []ScrobbleSession{{
		PlaybackSessionID: "playback-1",
		ConnectionID:      "conn-1",
		MediaItemID:       "movie-1",
		Kind:              "movie",
		TMDBID:            "603",
		HistoryID:         "history-1",
		LastProgress:      5400,
		DurationSeconds:   7200,
		Completed:         true,
	}}
	provider := scrobblerStub{stopEvents: make(chan ScrobbleEvent, 1)}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
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
	for _, update := range repo.scrobbleUpdates {
		if update.action == "stop" && update.stopSentAt != nil {
			foundClosed = true
		}
	}
	if !foundClosed {
		t.Fatalf("scrobble updates = %+v, want successful stop to mark session closed", repo.scrobbleUpdates)
	}
}

func TestServiceSweepOpenScrobblesKeepsFailedStopOpen(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.connections[connectionKey("trakt", 7, "profile-1")] = Connection{
		ID:              "conn-1",
		Provider:        "trakt",
		UserID:          7,
		ProfileID:       "profile-1",
		AccessToken:     "access",
		ScrobbleEnabled: true,
	}
	repo.scrobbleSessions = []ScrobbleSession{{
		PlaybackSessionID: "playback-1",
		ConnectionID:      "conn-1",
		MediaItemID:       "movie-1",
		Kind:              "movie",
		TMDBID:            "603",
		HistoryID:         "history-1",
		LastProgress:      5400,
		DurationSeconds:   7200,
	}}
	provider := scrobblerStub{stopErr: errors.New("provider offline")}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	service := NewService(repo, reg)

	if err := service.SweepOpenScrobbles(context.Background()); err != nil {
		t.Fatalf("SweepOpenScrobbles: %v", err)
	}
	for _, update := range repo.scrobbleUpdates {
		if update.stopSentAt != nil {
			t.Fatalf("stop_sent_at was set despite provider failure: %+v", repo.scrobbleUpdates)
		}
		if update.lastError == "provider offline" {
			return
		}
	}
	t.Fatalf("scrobble updates = %+v, want provider error recorded", repo.scrobbleUpdates)
}

func TestAppendWarningSummarizesDuplicateReasons(t *testing.T) {
	got := appendWarning("", []string{
		"missing season or episode number",
		"missing season or episode number",
		"no tmdb_id match for \"92820\"",
	})
	want := "missing season or episode number (2 items); no tmdb_id match for \"92820\""
	if got != want {
		t.Fatalf("appendWarning() = %q, want %q", got, want)
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
			MediaItemID: "movie-1",
			WatchedAt:   at.Add(-time.Duration(500-i) * time.Hour).Format(time.RFC3339),
			Completed:   true,
		}
	}
	rows[500] = userstore.WatchHistoryEntry{
		ProfileID:   "profile-1",
		MediaItemID: "movie-1",
		WatchedAt:   at.Add(time.Minute).Format(time.RFC3339),
		Completed:   true,
	}
	store := &completedHistoryListerStub{rows: rows}

	found, err := hasVisibleCompletedHistoryAtOrAfter(context.Background(), store, "profile-1", "movie-1", at)
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
		if query.ProfileID != "profile-1" || len(query.MediaItemIDs) != 1 || query.MediaItemIDs[0] != "movie-1" {
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

func TestHistorySourceForProviderDefaultsAndUsesProviderSource(t *testing.T) {
	if got := historySourceForProvider(watchedExporterStub{source: userstore.WatchHistorySourceSimkl}); got != userstore.WatchHistorySourceSimkl {
		t.Fatalf("historySourceForProvider(simkl) = %q, want simkl", got)
	}
	if got := historySourceForProvider(struct{}{}); got != userstore.WatchHistorySourceImport {
		t.Fatalf("historySourceForProvider(no source) = %q, want import", got)
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

func TestServiceSyncConnectionDefersOnRateLimit(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	repo := newServiceFakeRepo()
	progressCalled := false
	provider := rateLimitedImporterStub{progressCalled: &progressCalled}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	service := NewService(repo, reg).
		WithMatcher(matchedMatcherStub{mediaItemID: "movie-1"}).
		WithWatchState(noOpWatchState{}).
		WithUserStoreProvider(staticStoreProvider{store: userdb.NewSQLiteUserStore(db)})
	service.now = func() time.Time { return now }
	conn := Connection{
		ID:                    "conn-1",
		Provider:              "trakt",
		UserID:                7,
		ProfileID:             "profile-1",
		AccessToken:           "access",
		ImportWatchedEnabled:  true,
		ImportProgressEnabled: true,
	}
	repo.connections[connectionKey("trakt", 7, "profile-1")] = conn

	err = service.SyncConnection(context.Background(), conn, "scheduled")
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

func TestServiceSyncConnectionLeavesExportsPendingOnRateLimit(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := userdb.AddHistory(db, userstore.WatchHistoryEntry{
		ID:              "history-1",
		ProfileID:       "profile-1",
		MediaItemID:     "movie-1",
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
	provider := watchedExporterStub{exportErr: RateLimitedError{Provider: "trakt", RetryAfter: time.Hour}}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	service := NewService(repo, reg).WithUserStoreProvider(staticStoreProvider{
		store: userdb.NewSQLiteUserStore(db),
	})
	conn := Connection{
		ID:                   "conn-1",
		Provider:             "trakt",
		UserID:               7,
		ProfileID:            "profile-1",
		AccessToken:          "access",
		ExportWatchedEnabled: true,
	}
	repo.connections[connectionKey("trakt", 7, "profile-1")] = conn

	if err := service.SyncConnection(context.Background(), conn, "scheduled"); err == nil {
		t.Fatal("SyncConnection error = nil, want rate limit failure")
	}
	if len(repo.historyExports) != 1 || repo.historyExports[0].Status != "pending" {
		t.Fatalf("history exports = %+v, want one still-pending export", repo.historyExports)
	}
	updated := repo.connections[connectionKey("trakt", 7, "profile-1")]
	if updated.RateLimitedUntil == nil {
		t.Fatal("RateLimitedUntil not set after rate-limited export")
	}
}

func TestSyncDueConnectionsSkipsSiblingsOfRateLimitedAccount(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	repo := newServiceFakeRepo()
	fetchCalls := 0
	progressCalled := false
	provider := rateLimitedImporterStub{progressCalled: &progressCalled, fetchCalls: &fetchCalls}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	service := NewService(repo, reg).
		WithMatcher(matchedMatcherStub{mediaItemID: "movie-1"}).
		WithWatchState(noOpWatchState{}).
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
		AccessToken:          "access",
		ImportWatchedEnabled: true,
	}
	second := Connection{
		ID:                   "conn-2",
		Provider:             "trakt",
		UserID:               7,
		ProfileID:            "profile-2",
		ProviderAccountID:    "acct-1",
		AccessToken:          "access",
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
