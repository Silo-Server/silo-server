package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scopeFunc adapts a function to ScopeResolver.
type scopeFunc func(context.Context, access.ResolveInput) (access.Scope, error)

func (f scopeFunc) Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error) {
	return f(ctx, input)
}

// scopeByProfile resolves fixed scopes; an unknown profile does not exist.
type scopeByProfile map[string]access.Scope

func (s scopeByProfile) Resolve(_ context.Context, input access.ResolveInput) (access.Scope, error) {
	scope, ok := s[input.ProfileID]
	if !ok {
		return access.Scope{}, access.ErrProfileNotFound
	}
	return scope, nil
}

// accessCatalog is a mature-rated series that belongs to two libraries, with
// one episode, in the shared catalog tables.
type accessCatalog struct {
	library, otherLibrary int
	series, episode       string
}

func seedAccessCatalog(t *testing.T, pool *pgxpool.Pool) accessCatalog {
	t.Helper()
	ctx := t.Context()
	nonce := time.Now().UnixNano()
	prefix := fmt.Sprintf("recipient-access-%d", nonce)
	c := accessCatalog{
		library:      700000 + int(nonce%50000),
		otherLibrary: 750000 + int(nonce%50000),
		series:       prefix + "-series",
		episode:      prefix + "-e2",
	}
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, c.series)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{c.library, c.otherLibrary})
	})
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_folders (id, type, name) VALUES ($1, 'series', $3), ($2, 'series', $3 || '-other')`,
			[]any{c.library, c.otherLibrary, prefix}},
		{`INSERT INTO media_items (content_id, type, title, genres, content_rating, content_rating_age, advisory_age)
			VALUES ($1, 'series', 'Series', '{}', 'TV-MA', 17, 14)`, []any{c.series}},
		{`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2), ($1, $3)`,
			[]any{c.series, c.library, c.otherLibrary}},
		{`INSERT INTO seasons (content_id, series_id, season_number) VALUES ($1 || '-s1', $1, 1)`, []any{c.series}},
		{`INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title)
			VALUES ($1, $2, $2 || '-s1', 1, 2, 'E2')`, []any{c.episode, c.series}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed catalog: %v", err)
		}
	}
	return c
}

// TestFanoutSkipsRecipientsWithoutAccess covers interest rows that outlive a
// profile's access to the series: a content-rating ceiling, an advisory-age
// limit, or a library restriction set after the profile followed it. Fanout
// runs after those changes, so only profiles that can open the episode now
// get a delivery, and the others' notification cursors stay put.
func TestFanoutSkipsRecipientsWithoutAccess(t *testing.T) {
	pool := inboxPageDB(t)
	ctx := t.Context()
	c := seedAccessCatalog(t, pool)

	scopes := scopeByProfile{
		"open":    {},
		"allowed": {AllowedLibraryIDs: []int{c.library}, MaturityLimits: access.MaturityLimits{MaxContentRating: "R"}},
		"rated":   {MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}},
		"advised": {MaturityLimits: access.MaturityLimits{MaxAdvisoryAge: 10}},
		// Sees the series through the other library only; the episode
		// arrived in a library outside its scope.
		"other-library": {AllowedLibraryIDs: []int{c.otherLibrary}},
		// "deleted" has an interest row but no profile.
	}
	profiles := []string{"open", "allowed", "rated", "advised", "other-library", "deleted"}
	episodeKey := seedFanoutEvent(t, pool, c, profiles)

	worker := newAccessFanoutWorker(pool, scopes)
	processed, err := worker.processBatch(ctx)
	if err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed %d events, want 1", processed)
	}

	delivered := map[string]bool{}
	for _, profile := range deliveryRecipients(t, pool, `release_event_id = 'event-1'`) {
		delivered[profile] = true
	}
	want := map[string]bool{"open": true, "allowed": true}
	for _, profile := range profiles {
		if delivered[profile] != want[profile] {
			t.Errorf("profile %q delivered = %v, want %v", profile, delivered[profile], want[profile])
		}
		var advanced bool
		if err := pool.QueryRow(ctx, `
			SELECT last_notified_episode_key IS NOT DISTINCT FROM $2 FROM profile_series_interest WHERE profile_id = $1`,
			profile, episodeKey).Scan(&advanced); err != nil {
			t.Fatalf("read cursor for %q: %v", profile, err)
		}
		if advanced != want[profile] {
			t.Errorf("profile %q notification cursor advanced = %v, want %v", profile, advanced, want[profile])
		}
	}
}

// TestNotifyFulfilledSkipsRecipientWithoutAccess sends request.fulfilled for
// a title the follower's content-rating ceiling hides. The requester is told;
// the follower is not, and the request still counts as handled.
func TestNotifyFulfilledSkipsRecipientWithoutAccess(t *testing.T) {
	pool := inboxPageDB(t)
	c := seedAccessCatalog(t, pool)
	system := newAccessSystem(pool, scopeByProfile{
		"requester": {},
		"follower":  {MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}},
	})
	req := fulfilledRequest(requests.Follower{UserID: 2, ProfileID: "follower"})
	if err := NewRequestFulfillmentNotifier(system).NotifyFulfilled(t.Context(), req, c.series); err != nil {
		t.Fatalf("NotifyFulfilled: %v", err)
	}

	got := deliveryRecipients(t, pool, `type = '`+DeliveryTypeRequestFulfilled+`'`)
	if len(got) != 1 || got[0] != "requester" {
		t.Fatalf("request.fulfilled recipients = %v, want only the requester", got)
	}
}

// TestFanoutRetriesWhenRecipientScopeFails covers a recipient whose scope
// cannot be resolved right now: the resolver fails, or the profile's viewer
// preferences could not be read. The batch rolls back and the event stays
// unprocessed for the next run, rather than dropping that recipient's
// notification or sending it on an incomplete scope.
func TestFanoutRetriesWhenRecipientScopeFails(t *testing.T) {
	for name, scopes := range map[string]ScopeResolver{
		"resolver error": scopeFunc(func(_ context.Context, input access.ResolveInput) (access.Scope, error) {
			if input.ProfileID == "broken" {
				return access.Scope{}, errors.New("connection reset")
			}
			return access.Scope{}, nil
		}),
		"degraded preferences": scopeByProfile{"open": {}, "broken": {PreferencesDegraded: true}},
	} {
		t.Run(name, func(t *testing.T) {
			pool := inboxPageDB(t)
			c := seedAccessCatalog(t, pool)
			seedFanoutEvent(t, pool, c, []string{"open", "broken"})

			if _, err := newAccessFanoutWorker(pool, scopes).processBatch(t.Context()); err == nil {
				t.Fatal("processBatch succeeded, want the scope error")
			}
			if got := deliveryRecipients(t, pool, `true`); len(got) != 0 {
				t.Errorf("deliveries = %v, want none", got)
			}
			var unprocessed bool
			if err := pool.QueryRow(t.Context(), `SELECT processed_at IS NULL FROM release_events WHERE id = 'event-1'`).Scan(&unprocessed); err != nil {
				t.Fatalf("read event: %v", err)
			}
			if !unprocessed {
				t.Error("event marked processed, want it left for the next batch")
			}
		})
	}
}

// TestFanoutResolvesOnlyEligibleRecipients checks that fanout resolves a scope
// only for candidates who would get a delivery: a profile with notifications
// turned off is never resolved.
func TestFanoutResolvesOnlyEligibleRecipients(t *testing.T) {
	pool := inboxPageDB(t)
	c := seedAccessCatalog(t, pool)
	seedFanoutEvent(t, pool, c, []string{"open", "muted"})
	muted := DefaultPreferences("muted")
	muted.Enabled = false
	if err := NewPreferencesRepository(pool).Upsert(t.Context(), muted); err != nil {
		t.Fatalf("mute profile: %v", err)
	}

	var resolved []string
	scopes := scopeFunc(func(_ context.Context, input access.ResolveInput) (access.Scope, error) {
		resolved = append(resolved, input.ProfileID)
		return access.Scope{}, nil
	})
	if _, err := newAccessFanoutWorker(pool, scopes).processBatch(t.Context()); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if !slices.Equal(resolved, []string{"open"}) {
		t.Errorf("resolved %v, want only the profile with notifications on", resolved)
	}
	if got := deliveryRecipients(t, pool, `true`); !slices.Equal(got, []string{"open"}) {
		t.Errorf("deliveries = %v, want only open", got)
	}
}

// TestNotifyFulfilledRetriesWhenRecipientScopeFails returns the scope error,
// so the caller retries the request instead of stamping it notified.
func TestNotifyFulfilledRetriesWhenRecipientScopeFails(t *testing.T) {
	pool := inboxPageDB(t)
	c := seedAccessCatalog(t, pool)
	system := newAccessSystem(pool, scopeByProfile{
		"requester": {},
		"follower":  {PreferencesDegraded: true},
	})
	req := fulfilledRequest(requests.Follower{UserID: 2, ProfileID: "follower"})
	if err := NewRequestFulfillmentNotifier(system).NotifyFulfilled(t.Context(), req, c.series); err == nil {
		t.Fatal("NotifyFulfilled succeeded, want the scope error")
	}
	if got := deliveryRecipients(t, pool, `profile_id = 'follower'`); len(got) != 0 {
		t.Errorf("follower deliveries = %v, want none", got)
	}
}

// TestNotifyFulfilledRetryResolvesOnlyUntoldRecipients covers the retry of a
// partly delivered request. A recipient whose scope fails does not stop the
// ones after it, and the retry skips recipients an earlier pass told, so the
// requester's scope failing later cannot hold the request back.
func TestNotifyFulfilledRetryResolvesOnlyUntoldRecipients(t *testing.T) {
	pool := inboxPageDB(t)
	c := seedAccessCatalog(t, pool)
	var failing map[string]bool
	var resolved []string
	system := newAccessSystem(pool, scopeFunc(func(_ context.Context, input access.ResolveInput) (access.Scope, error) {
		resolved = append(resolved, input.ProfileID)
		if failing[input.ProfileID] {
			return access.Scope{}, errors.New("connection reset")
		}
		return access.Scope{}, nil
	}))
	notifier := NewRequestFulfillmentNotifier(system)
	req := fulfilledRequest(requests.Follower{UserID: 2, ProfileID: "first"}, requests.Follower{UserID: 3, ProfileID: "second"})
	fulfilled := `type = '` + DeliveryTypeRequestFulfilled + `'`

	failing = map[string]bool{"first": true}
	if err := notifier.NotifyFulfilled(t.Context(), req, c.series); err == nil {
		t.Fatal("first pass succeeded, want the scope error")
	}
	if got := deliveryRecipients(t, pool, fulfilled); !slices.Equal(got, []string{"requester", "second"}) {
		t.Fatalf("first pass told %v, want requester and second", got)
	}

	failing, resolved = map[string]bool{"requester": true}, nil
	if err := notifier.NotifyFulfilled(t.Context(), req, c.series); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !slices.Equal(resolved, []string{"first"}) {
		t.Errorf("retry resolved %v, want only first", resolved)
	}
	if got := deliveryRecipients(t, pool, fulfilled); !slices.Equal(got, []string{"first", "requester", "second"}) {
		t.Errorf("told %v after retry, want all three once", got)
	}
}

// TestDispatchOperationalResolvesBeforeHoldingConnection runs request.fulfilled
// on a one-connection pool with a resolver that reads through that pool, as
// the production resolver does. Resolving inside the dispatch transaction
// would wait for a second connection until the deadline.
func TestDispatchOperationalResolvesBeforeHoldingConnection(t *testing.T) {
	pool := inboxPageDB(t)
	c := seedAccessCatalog(t, pool)
	cfg := pool.Config()
	cfg.MaxConns = 1
	single, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(single.Close)
	system := newAccessSystem(single, scopeFunc(func(ctx context.Context, _ access.ResolveInput) (access.Scope, error) {
		var one int
		return access.Scope{}, single.QueryRow(ctx, `SELECT 1`).Scan(&one)
	}))

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := NewRequestFulfillmentNotifier(system).NotifyFulfilled(ctx, fulfilledRequest(), c.series); err != nil {
		t.Fatalf("NotifyFulfilled: %v", err)
	}
	if got := deliveryRecipients(t, single, `true`); !slices.Equal(got, []string{"requester"}) {
		t.Errorf("deliveries = %v, want the requester", got)
	}
}

// seedFanoutEvent creates the fanout tables in the test schema, an interest
// row on account 1 favoriting c's series for each profile, and release event
// "event-1" for c's episode. It returns the episode key.
func seedFanoutEvent(t *testing.T, pool *pgxpool.Pool, c accessCatalog, profiles []string) int {
	t.Helper()
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE release_events (LIKE public.release_events INCLUDING ALL);
		CREATE TABLE profile_series_interest (LIKE public.profile_series_interest INCLUDING ALL)`); err != nil {
		t.Fatalf("create fanout tables: %v", err)
	}
	for _, profile := range profiles {
		if _, err := pool.Exec(ctx, `
			INSERT INTO profile_series_interest (user_id, profile_id, library_id, series_id, favorite)
			VALUES (1, $1, $2, $3, true)`, profile, c.library, c.series); err != nil {
			t.Fatalf("seed interest: %v", err)
		}
	}
	episodeKey := EpisodeKey(1, 2)
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_events (id, library_id, series_id, episode_id, season_number, episode_number,
			episode_key, available_at, dedupe_key)
		VALUES ('event-1', $1, $2, $3, 1, 2, $4, now(), $5)`,
		c.library, c.series, c.episode, episodeKey, EpisodeDedupeKey(c.library, c.series, episodeKey)); err != nil {
		t.Fatalf("seed release event: %v", err)
	}
	return episodeKey
}

func newAccessFanoutWorker(pool *pgxpool.Pool, scopes ScopeResolver) *FanoutWorker {
	worker := NewFanoutWorker(pool, NewReleaseRepository(pool), NewInterestRepository(pool),
		NewDeliveryRepository(pool), NewPreferencesRepository(pool),
		NewSettings(mapSettingReader{SettingFanoutSettleSeconds: "0"}), scopes, NewMultiDispatcher())
	worker.logger = slog.New(slog.DiscardHandler)
	return worker
}

func newAccessSystem(pool *pgxpool.Pool, scopes ScopeResolver) *System {
	return &System{
		pool:        pool,
		Settings:    NewSettings(mapSettingReader{}),
		Deliveries:  NewDeliveryRepository(pool),
		Preferences: NewPreferencesRepository(pool),
		dispatcher:  NewMultiDispatcher(),
		scopes:      scopes,
		logger:      slog.New(slog.DiscardHandler),
	}
}

// deliveryRecipients lists the profiles of the deliveries matching where.
func deliveryRecipients(t *testing.T, pool *pgxpool.Pool, where string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT profile_id FROM notification_deliveries WHERE `+where+` ORDER BY profile_id`)
	if err != nil {
		t.Fatalf("query deliveries: %v", err)
	}
	profiles, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read deliveries: %v", err)
	}
	return profiles
}
