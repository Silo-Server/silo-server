package watchsync

import (
	"os"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestDroppedSyncRepositoryDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var userID int
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "watch-dropped-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,'dropped-p','Dropped')", userID); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("watch-dropped-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)

	// Only dropped-show sync is on, so the connection is due for that alone.
	conn, err := repo.UpsertConnection(ctx, Connection{
		Provider: "dropped", UserID: userID, ProfileID: "dropped-p", AccessToken: "token",
		ProviderAccountID: "acct-1", SyncDroppedEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !conn.SyncDroppedEnabled {
		t.Fatal("inserted dropped toggle is off")
	}
	due, err := repo.ListConnectionsDueForSync(ctx, conn.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range due {
		found = found || c.ID == conn.ID
	}
	if !found {
		t.Fatal("a connection syncing only dropped shows must be due")
	}

	again := conn
	again.SyncDroppedEnabled = false
	if saved, err := repo.UpsertConnection(ctx, again); err != nil || !saved.SyncDroppedEnabled {
		t.Fatalf("a token upsert must not change the dropped toggle: %v %v", saved.SyncDroppedEnabled, err)
	}
	events, err := repo.ListDroppedEventConnections(ctx, userID, "dropped-p")
	if err != nil || len(events) != 1 {
		t.Fatalf("event connections = %d, %v; want 1", len(events), err)
	}
	if _, err := repo.UpdateConnectionSettings(ctx, "dropped", userID, "dropped-p", nil, ConnectionUpdate{SyncDroppedEnabled: new(false)}, nil); err != nil {
		t.Fatal(err)
	}
	if events, err := repo.ListDroppedEventConnections(ctx, userID, "dropped-p"); err != nil || len(events) != 0 {
		t.Fatalf("event connections with sync off = %d, %v; want 0", len(events), err)
	}

	// States are fenced to the bound account and keep a known provider key.
	if err := repo.UpsertDroppedSyncStates(ctx, []DroppedSyncState{
		{ConnectionID: conn.ID, ProviderAccountID: "acct-1", SeriesID: "series-1", ProviderItemKey: "tvdb:1"},
		{ConnectionID: conn.ID, ProviderAccountID: "acct-old", SeriesID: "series-2", ProviderItemKey: "tvdb:2"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDroppedSyncStates(ctx, []DroppedSyncState{
		{ConnectionID: conn.ID, ProviderAccountID: "acct-1", SeriesID: "series-1", RemoteSeen: true},
	}); err != nil {
		t.Fatal(err)
	}
	states, err := repo.ListDroppedSyncStates(ctx, conn.ID, "acct-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].SeriesID != "series-1" || !states[0].RemoteSeen || states[0].ProviderItemKey != "tvdb:1" {
		t.Fatalf("states = %#v, want series-1 seen with its key", states)
	}
	if err := repo.DeleteDroppedSyncStates(ctx, conn.ID, "acct-1", []string{"series-1"}); err != nil {
		t.Fatal(err)
	}
	if states, _ := repo.ListDroppedSyncStates(ctx, conn.ID, "acct-1", nil); len(states) != 0 {
		t.Fatalf("states after delete = %#v", states)
	}
}

// TestPluginDroppedSyncDB runs a plugin provider's dropped-show merge against
// PostgreSQL: the connection's cursor, the agreed drops, and the profile's
// drops, across a complete read, an incremental tombstone, and an account
// switch.
func TestPluginDroppedSyncDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const profileID = "dropped-plugin-p"
	var userID int
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "watch-dropped-plugin-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,$2,'Dropped plugin')", userID, profileID); err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	seriesA, seriesB := "dropped-plugin-a-"+suffix, "dropped-plugin-b-"+suffix
	tmdbA, tmdbB := "a-"+suffix, "b-"+suffix
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,genres,tmdb_id) VALUES ($1,'series','A','{}',$3),($2,'series','B','{}',$4)`,
		seriesA, seriesB, tmdbA, tmdbB); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM media_items WHERE content_id = ANY($1)", []string{seriesA, seriesB})
	}()
	cipher, err := secret.New([]byte("watch-dropped-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)
	client := &fakeWatchSyncPluginClient{applyStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED}
	provider := testPluginProviderWithDescriptor(t, client, droppedTestDescriptor())
	conn, err := repo.UpsertConnection(ctx, Connection{
		Provider: provider.Key(), UserID: userID, ProfileID: profileID, AccessToken: "token",
		ProviderAccountID: "acct-1", SyncDroppedEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	drops := catalog.NewDroppedSeriesRepo(pool)
	service := NewService(repo, registry).
		WithMatcher(ratingMatcherStub{media: map[string]LocalFavorite{
			seriesA: {MediaItemID: seriesA, Kind: historyimport.KindSeries, TMDBID: tmdbA},
			seriesB: {MediaItemID: seriesB, Kind: historyimport.KindSeries, TMDBID: tmdbB},
		}}).
		WithDroppedStore(drops)

	sync := func() {
		t.Helper()
		client.applyRequest, client.listRequests = nil, nil
		if _, err := service.syncDropped(ctx, conn, ServerConfig{}, provider); err != nil {
			t.Fatal(err)
		}
	}
	localDrops := func() map[string]catalog.DroppedSeries {
		t.Helper()
		rows, err := drops.ListDropped(ctx, userID, profileID, []string{seriesA, seriesB})
		if err != nil {
			t.Fatal(err)
		}
		byID := make(map[string]catalog.DroppedSeries, len(rows))
		for _, row := range rows {
			byID[row.SeriesID] = row
		}
		return byID
	}
	agreed := func(account string) map[string]DroppedSyncState {
		t.Helper()
		states, err := repo.ListDroppedSyncStates(ctx, conn.ID, account, nil)
		if err != nil {
			t.Fatal(err)
		}
		byID := make(map[string]DroppedSyncState, len(states))
		for _, state := range states {
			byID[state.SeriesID] = state
		}
		return byID
	}
	cursor := func() string {
		t.Helper()
		fresh, ok, err := repo.GetConnectionByID(ctx, conn.ID)
		if err != nil || !ok {
			t.Fatalf("reload connection: ok=%v err=%v", ok, err)
		}
		return fresh.SyncCursors[pluginDroppedCursorKey]
	}

	// A complete read holds B under the plugin's key; A is dropped only in Silo.
	if err := drops.Drop(ctx, userID, profileID, seriesA); err != nil {
		t.Fatal(err)
	}
	droppedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	client.listResponse = &pluginv1.WatchSyncListRemoteStateResponse{
		CompleteSnapshot: true,
		NextCursor:       "c1",
		Items:            []*pluginv1.WatchSyncRemoteState{remoteDroppedState("remote-b", tmdbB, timestamppb.New(droppedAt))},
	}
	sync()
	local := localDrops()
	if !local[seriesA].Active || !local[seriesB].Active || !local[seriesB].DroppedAt.Equal(droppedAt) {
		t.Fatalf("local drops = %#v, want A kept and B imported at %s", local, droppedAt)
	}
	states := agreed("acct-1")
	if a := states[seriesA]; a.RemoteSeen || a.ProviderItemKey != "tmdb:"+tmdbA {
		t.Fatalf("A agreement = %#v, want sent under Silo's key and not yet seen", a)
	}
	if b := states[seriesB]; !b.RemoteSeen || b.ProviderItemKey != "remote-b" {
		t.Fatalf("B agreement = %#v, want seen under the plugin's key", b)
	}
	if events := client.applyRequest.GetEvents(); len(events) != 1 ||
		events[0].GetOperation() != pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_MARK_DROPPED ||
		events[0].GetMedia().GetMediaItemId() != seriesA {
		t.Fatalf("events = %#v, want A dropped", events)
	}
	if got := cursor(); got != "c1" {
		t.Fatalf("cursor = %q, want c1", got)
	}

	// An incremental read from that cursor undrops B by the plugin's key.
	client.listResponse = &pluginv1.WatchSyncListRemoteStateResponse{
		NextCursor: "c2",
		Items:      []*pluginv1.WatchSyncRemoteState{droppedTombstone("remote-b")},
	}
	sync()
	if len(client.listRequests) != 1 || client.listRequests[0].GetCursor() != "c1" {
		t.Fatalf("requests = %#v, want the read resumed from c1", client.listRequests)
	}
	local = localDrops()
	if _, ok := local[seriesB]; ok || !local[seriesA].Active {
		t.Fatalf("local drops = %#v, want B undropped and A kept", local)
	}
	states = agreed("acct-1")
	if _, ok := states[seriesB]; ok || len(states) != 1 {
		t.Fatalf("agreements = %#v, want only A", states)
	}
	if client.applyRequest != nil {
		t.Fatalf("request = %#v, want no provider writes", client.applyRequest)
	}
	if got := cursor(); got != "c2" {
		t.Fatalf("cursor = %q, want c2", got)
	}

	// A switch to another provider account drops the dropped cursor and the
	// previous account's agreements.
	if _, err := service.persistConnection(ctx, provider.Key(), userID, profileID, TokenSet{AccessToken: "token-2"}, ProviderAccount{ID: "acct-2"}); err != nil {
		t.Fatal(err)
	}
	if got := cursor(); got != "" {
		t.Fatalf("cursor after account switch = %q, want none", got)
	}
	if states := agreed("acct-1"); len(states) != 0 {
		t.Fatalf("previous account's agreements = %#v, want cleared", states)
	}
}
