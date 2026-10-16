package jellycompat

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func recipeDurableFixture(t *testing.T) (*pgxpool.Pool, *redis.Client, *noderecipe.Store, *DurableCompatPlaybackStore) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if dsn == "" || endpoint == "" {
		t.Skip("requires actual PostgreSQL and Redis test fixtures")
	}
	base, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "recipe_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); base.Close() })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(t.Context(), `CREATE TABLE jellycompat_playback_sessions(id TEXT PRIMARY KEY,compat_token TEXT NOT NULL,user_id TEXT NOT NULL,data JSONB NOT NULL,expires_at TIMESTAMPTZ NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	options, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	recipes := noderecipe.NewStore(client, time.Hour)
	return pool, client, recipes, NewDurableCompatPlaybackStore(pool, time.Hour, nil)
}
func recipeFixtureSession(t *testing.T, state CompatPlaybackStore, recipes *noderecipe.Store, prior bool) (string, playback.RecipeCard) {
	t.Helper()
	work := "recipe-work-" + uuid.NewString()
	card := playback.NewRecipeCard(7, "profile", 42, "https://worker.example.test", playback.TranscodeOpts{SessionID: work, InputPath: "/approved/prior"})
	session := PlaybackSession{ID: work, UpstreamSessionID: work, CompatToken: "fixture", UserID: "fixture-user"}
	if prior {
		session.Recipe = &card
		session.TranscodeStarted = true
		session.RecipePublicationID = "legacy-prior"
		if err := recipes.Put(t.Context(), work, card); err != nil {
			t.Fatal(err)
		}
	}
	state.Put(session)
	t.Cleanup(func() { _ = recipes.Delete(context.Background(), work) })
	return work, card
}
func readRecipeRow(t *testing.T, pool *pgxpool.Pool, id string) PlaybackSession {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(t.Context(), "SELECT data FROM jellycompat_playback_sessions WHERE id=$1", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var session PlaybackSession
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}
	return session
}

type failedRecipePublicationStore struct {
	*noderecipe.Store
	after   func(context.Context, string, playback.RecipeCard) error
	failure error
}

func (s *failedRecipePublicationStore) PublishWithUndo(ctx context.Context, work string, card playback.RecipeCard, accepted ...*noderecipe.AcceptedPublication) (noderecipe.Publication, error) {
	publication, err := s.Store.PublishWithUndo(ctx, work, card, accepted...)
	if err != nil {
		return publication, err
	}
	if s.after != nil {
		if err = s.after(ctx, work, card); err != nil {
			return publication, err
		}
	}
	return publication, s.failure
}

func TestRecipePublicationRecoveryPreservesDatabaseCommittedCandidate(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "prior"}[prior], func(t *testing.T) {
			pool, client, recipes, state := recipeDurableFixture(t)
			work, card := recipeFixtureSession(t, state, recipes, prior)
			initial := readRecipeRow(t, pool, work)
			card.InputPath = "/approved/accepted-A"
			publication, err := recipes.PublishWithUndo(t.Context(), work, card, acceptedRecipe(&initial))
			if err != nil {
				t.Fatal(err)
			}
			// Actual database acceptance followed by process death before Redis ack.
			if err = state.Update(work, func(current *PlaybackSession) error {
				current.Recipe = &card
				current.RecipePublicationID = publication.ID()
				current.TranscodeStarted = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err = client.Del(t.Context(), "silo:noderecipe-owner:"+work).Err(); err != nil {
				t.Fatal(err)
			}
			candidate := card
			candidate.InputPath = "/approved/failed-B"
			failure := errors.New("publication response lost")
			replica := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
			err = replica.PublishRecipe(t.Context(), work, work, candidate, &failedRecipePublicationStore{Store: recipes, failure: failure})
			if !errors.Is(err, failure) {
				t.Fatal("B failure not surfaced", err)
			}
			durable := readRecipeRow(t, pool, work)
			got, found := recipes.Get(t.Context(), work)
			if durable.RecipePublicationID != publication.ID() || !found || got.InputPath != durable.Recipe.InputPath || got.InputPath != card.InputPath {
				t.Fatalf("recovery lost database-accepted A: durable=%+v node=%+v", durable.Recipe, got)
			}
		})
	}
}

func TestRecipePublicationRejectsStaleAuthoritativeRowsBeforeRedis(t *testing.T) {
	for _, kind := range []string{"missing", "expired", "terminal", "moved"} {
		t.Run(kind, func(t *testing.T) {
			pool, _, recipes, state := recipeDurableFixture(t)
			work, card := recipeFixtureSession(t, state, recipes, true)
			switch kind {
			case "missing":
				_, _ = pool.Exec(t.Context(), "DELETE FROM jellycompat_playback_sessions WHERE id=$1", work)
			case "expired":
				_, _ = pool.Exec(t.Context(), "UPDATE jellycompat_playback_sessions SET expires_at=now()-interval '1 second' WHERE id=$1", work)
			default:
				column := "Terminal"
				value := `true`
				if kind == "moved" {
					column = "UpstreamSessionID"
					value = `"another-work"`
				}
				_, _ = pool.Exec(t.Context(), "UPDATE jellycompat_playback_sessions SET data=jsonb_set(data,ARRAY[$2],$3::jsonb) WHERE id=$1", work, column, value)
			}
			before, _, err := recipes.RecipeVersion(t.Context(), work)
			if err != nil {
				t.Fatal(err)
			}
			card.InputPath = "/approved/candidate"
			if err = state.PublishRecipe(t.Context(), work, work, card, recipes); err == nil {
				t.Fatal("stale cached row authorized publication")
			}
			after, _, err := recipes.RecipeVersion(t.Context(), work)
			if err != nil || before.InputPath != after.InputPath {
				t.Fatal("rejected row changed Redis", err)
			}
			if len(state.pendingUpdatesSnapshot(work)) != 0 {
				t.Fatal("publication rejection queued callback for replay")
			}
		})
	}
}

func TestRecipePublicationDatabaseFailureRestoresWithoutQueuedReplay(t *testing.T) {
	pool, _, recipes, state := recipeDurableFixture(t)
	work, card := recipeFixtureSession(t, state, recipes, true)
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_candidate_recipe() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.data->'Recipe'->>'input_path'='/approved/rejected' THEN RAISE EXCEPTION 'injected durable recipe rejection'; END IF;RETURN NEW;END $$; CREATE TRIGGER reject_candidate BEFORE UPDATE ON jellycompat_playback_sessions FOR EACH ROW EXECUTE FUNCTION reject_candidate_recipe()`); err != nil {
		t.Fatal(err)
	}
	card.InputPath = "/approved/rejected"
	if err := state.PublishRecipe(t.Context(), work, work, card, recipes); err == nil {
		t.Fatal("database rejection was swallowed")
	}
	got, found := recipes.Get(t.Context(), work)
	durable := readRecipeRow(t, pool, work)
	cached, ok := state.mem.Get(work)
	if !found || got.InputPath != "/approved/prior" || durable.Recipe.InputPath != got.InputPath || !ok || cached.Recipe.InputPath != got.InputPath {
		t.Fatal("failed database publication changed accepted state")
	}
	if len(state.pendingUpdatesSnapshot(work)) != 0 {
		t.Fatal("failed recipe callback queued for later replay")
	}
	if err := state.Update(work, func(current *PlaybackSession) error { current.ClientDeviceID = "later-progress"; return nil }); err != nil {
		t.Fatal(err)
	}
	if readRecipeRow(t, pool, work).Recipe.InputPath != "/approved/prior" {
		t.Fatal("later ordinary Update replayed rejected publication")
	}
	card.InputPath = "/approved/retried"
	if err := state.PublishRecipe(t.Context(), work, work, card, recipes); err != nil {
		t.Fatal("restored predecessor prevented a legitimate retry", err)
	}
}

func TestRecipePublicationFailurePreservesIdenticalOrdinarySuccessor(t *testing.T) {
	pool, _, recipes, state := recipeDurableFixture(t)
	work, card := recipeFixtureSession(t, state, recipes, true)
	card.InputPath = "/approved/candidate"
	var successorVersion string
	failure := errors.New("lost candidate response")
	failed := &failedRecipePublicationStore{Store: recipes, failure: failure, after: func(ctx context.Context, work string, card playback.RecipeCard) error {
		if err := recipes.Put(ctx, work, card); err != nil {
			return err
		}
		_, version, err := recipes.RecipeVersion(ctx, work)
		successorVersion = version
		return err
	}}
	if err := state.PublishRecipe(t.Context(), work, work, card, failed); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	got, version, err := recipes.RecipeVersion(t.Context(), work)
	if err != nil || got == nil || got.InputPath != card.InputPath || version != successorVersion {
		t.Fatal("failed publication erased identical ordinary successor", err)
	}
	if readRecipeRow(t, pool, work).Recipe.InputPath != "/approved/prior" {
		t.Fatal("failure committed compat state")
	}
}

// Filter server messages only after the real backend executes the targeted
// command. Losing CommandComplete for COMMIT models an uncertain DB result;
// losing the unlock DataRow verifies a possibly locked session is discarded.
type recipeResponseFault struct {
	target  string
	armed   atomic.Bool
	dropped atomic.Int32
}
type recipeFaultConn struct {
	net.Conn
	fault             *recipeResponseFault
	startup           bool
	outbound, pending []byte
	drop              bool
}

func (c *recipeFaultConn) Write(p []byte) (int, error) {
	c.outbound = append(c.outbound, p...)
	for len(c.outbound) >= 5 {
		if c.startup {
			length := int(binary.BigEndian.Uint32(c.outbound[:4]))
			if len(c.outbound) < length {
				break
			}
			c.outbound = c.outbound[length:]
			c.startup = false
			continue
		}
		length := 1 + int(binary.BigEndian.Uint32(c.outbound[1:5]))
		if len(c.outbound) < length {
			break
		}
		packet := c.outbound[:length]
		query := ""
		switch packet[0] {
		case 'Q':
			query = string(packet[5 : len(packet)-1])
		case 'P':
			rest := packet[5:]
			if i := bytes.IndexByte(rest, 0); i >= 0 {
				rest = rest[i+1:]
				if j := bytes.IndexByte(rest, 0); j >= 0 {
					query = string(rest[:j])
				}
			}
		}
		matches := c.fault.target == "commit" && strings.EqualFold(strings.TrimSpace(query), "commit") || c.fault.target == "unlock" && strings.Contains(query, "pg_advisory_unlock") || c.fault.target == "lock" && strings.Contains(query, "pg_advisory_lock(")
		if matches && c.fault.armed.CompareAndSwap(true, false) {
			c.drop = true
		}
		c.outbound = c.outbound[length:]
	}
	return c.Conn.Write(p)
}

//nolint:nestif // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func (c *recipeFaultConn) Read(p []byte) (int, error) {
	if len(c.pending) == 0 {
		header := make([]byte, 5)
		if _, err := io.ReadFull(c.Conn, header); err != nil {
			return 0, err
		}
		length := int(binary.BigEndian.Uint32(header[1:]))
		if length < 4 {
			return 0, io.ErrUnexpectedEOF
		}
		body := make([]byte, length-4)
		if _, err := io.ReadFull(c.Conn, body); err != nil {
			return 0, err
		}
		if c.drop && (c.fault.target == "commit" && header[0] == 'C' && string(body) == "COMMIT\x00" || (c.fault.target == "unlock" || c.fault.target == "lock") && header[0] == 'D') {
			c.fault.dropped.Add(1)
			_ = c.Close()
			return 0, io.ErrUnexpectedEOF
		}
		c.pending = make([]byte, len(header)+len(body))
		copy(c.pending, header)
		copy(c.pending[len(header):], body)
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
func TestRecipePublicationDiscardsLostPostgresLockReply(t *testing.T) {
	testRecipePostgresResponseFault(t, "lock")
}
func TestRecipePublicationReconcilesLostPostgresCommitReply(t *testing.T) {
	testRecipePostgresResponseFault(t, "commit")
}
func TestRecipePublicationDiscardsLostPostgresUnlockReply(t *testing.T) {
	testRecipePostgresResponseFault(t, "unlock")
}
func testRecipePostgresResponseFault(t *testing.T, target string) {
	t.Helper()
	pool, _, recipes, state := recipeDurableFixture(t)
	work, card := recipeFixtureSession(t, state, recipes, true)
	fault := &recipeResponseFault{target: target}
	config := pool.Config().Copy()
	config.MaxConns = 2
	config.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &recipeFaultConn{Conn: conn, fault: fault, startup: true}, nil
	}
	affected, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(affected.Close)
	var before uint32
	if err = affected.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	fault.armed.Store(true)
	card.InputPath = "/approved/accepted-after-lost-" + target
	replica := NewDurableCompatPlaybackStore(affected, time.Hour, nil)
	err = replica.PublishRecipe(t.Context(), work, work, card, recipes)
	if target == "lock" {
		if err == nil {
			t.Fatal("lost acquisition reply was treated as ownership")
		}
	} else if err != nil {
		t.Fatal("known committed publication was lost", err)
	}
	if fault.dropped.Load() != 1 {
		t.Fatal("fault did not follow an actual PostgreSQL command")
	}
	if len(replica.mem.recipeAdmission) != 0 {
		t.Fatal("lost PostgreSQL response leaked ownership admission")
	}

	durable := readRecipeRow(t, pool, work)
	got, found := recipes.Get(t.Context(), work)
	expected := card.InputPath
	if target == "lock" {
		expected = "/approved/prior"
	}
	if !found || durable.Recipe.InputPath != expected || got.InputPath != expected || durable.RecipePublicationID == "" {
		t.Fatal("uncertain command left database and Redis inconsistent")
	}
	var after uint32
	if err = affected.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&after); err != nil || before == after {
		t.Fatal("possibly locked connection returned to pool", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	locked, err := acquireRecipeConnection(ctx, pool, work)
	if err != nil {
		t.Fatal("fault retained the work lock", err)
	}
	locked.release()
}

func TestRecipePublicationMemorySerializesTwoFailingHandlers(t *testing.T) {
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("requires actual Redis")
	}
	options, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	recipes := noderecipe.NewStore(client, time.Minute)
	for _, prior := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "prior"}[prior], func(t *testing.T) {
			state := NewPlaybackSessionStore(time.Hour, nil)
			work, card := recipeFixtureSession(t, state, recipes, prior)
			entered, resume := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(resume) })
			defer release()
			failed := &failedRecipePublicationStore{Store: recipes, failure: errors.New("failed A"), after: func(context.Context, string, playback.RecipeCard) error { close(entered); <-resume; return nil }}
			first := make(chan error, 1)
			candidate := card
			candidate.InputPath = "/approved/A"
			go func() { first <- state.PublishRecipe(t.Context(), work, work, candidate, failed) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("A did not publish")
			}
			second := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				candidate := card
				candidate.InputPath = "/approved/B"
				second <- state.PublishRecipe(t.Context(), work, work, candidate, &failedRecipePublicationStore{Store: recipes, failure: errors.New("failed B")})
			}()
			<-started
			release()
			if <-first == nil || <-second == nil {
				t.Fatal("failed publication succeeded")
			}
			got, found := recipes.Get(t.Context(), work)
			if found != prior || prior && got.InputPath != "/approved/prior" {
				t.Fatalf("two failures revived candidate: %+v", got)
			}
			current, _ := state.Get(work)
			if current.RecipePublicationID != "legacy-prior" && prior || !prior && current.Recipe != nil {
				t.Fatal("two failures changed shared memory state")
			}
		})
	}
}

func TestRecipePublicationPostgresSerializesTwoFailingHandlers(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "prior"}[prior], func(t *testing.T) {
			pool, _, recipes, state := recipeDurableFixture(t)
			work, _ := recipeFixtureSession(t, state, recipes, prior)
			entered, resume := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(resume) })
			defer release()
			firstStore := &failedRecipePublicationStore{Store: recipes, failure: errors.New("failed first handler"), after: func(context.Context, string, playback.RecipeCard) error { close(entered); <-resume; return nil }}
			config := pool.Config().Copy()
			application := "recipe-replica-" + uuid.NewString()
			config.ConnConfig.RuntimeParams["application_name"] = application
			secondPool, err := pgxpool.NewWithConfig(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(secondPool.Close)
			makeHandler := func(state CompatPlaybackStore, store recipeNodePutter) *PlaybackHandler {
				return &PlaybackHandler{playbackStore: state, RecipeNodeStore: store, tm: playback.NewTranscodeManager(), sessionMgr: &lockedCompatSessionManager{session: playback.Session{ID: work, UserID: 7, ProfileID: "profile", MediaFileID: 42, TranscodeNodeURL: "https://worker.example.test"}}}
			}
			firstHandler := makeHandler(state, firstStore)
			secondHandler := makeHandler(NewDurableCompatPlaybackStore(secondPool, time.Hour, nil), &failedRecipePublicationStore{Store: recipes, failure: errors.New("failed second handler")})
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() {
				firstDone <- firstHandler.persistTranscodeRecipe(t.Context(), work, work, playback.TranscodeOpts{SessionID: work, InputPath: "/approved/failed-A"})
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("first handler did not publish")
			}
			go func() {
				secondDone <- secondHandler.persistTranscodeRecipe(t.Context(), work, work, playback.TranscodeOpts{SessionID: work, InputPath: "/approved/failed-B"})
			}()
			barrierCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			for {
				var waiting bool
				if err = pool.QueryRow(barrierCtx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND wait_event='advisory')", application).Scan(&waiting); err != nil {
					t.Fatal("second replica did not wait on shared work lock", err)
				}
				if waiting {
					break
				}
				select {
				case <-barrierCtx.Done():
					t.Fatal("cross-replica lock barrier not reached")
				case <-time.After(time.Millisecond):
				}
			}
			release()
			if <-firstDone == nil || <-secondDone == nil {
				t.Fatal("failed handler reported success")
			}
			durable := readRecipeRow(t, pool, work)
			got, found := recipes.Get(t.Context(), work)
			if found != prior || prior && (got.InputPath != "/approved/prior" || durable.Recipe.InputPath != got.InputPath) || !prior && durable.Recipe != nil {
				t.Fatalf("failing replicas revived authority: durable=%+v node=%+v", durable.Recipe, got)
			}
		})
	}
}

type compensationBarrierStore struct {
	*noderecipe.Store
	entered, resume chan struct{}
	once            sync.Once
}

func (s *compensationBarrierStore) pause() { s.once.Do(func() { close(s.entered); <-s.resume }) }
func (s *compensationBarrierStore) PublishWithUndo(ctx context.Context, work string, card playback.RecipeCard, accepted ...*noderecipe.AcceptedPublication) (noderecipe.Publication, error) {
	p, e := s.Store.PublishWithUndo(ctx, work, card, accepted...)
	if e != nil {
		return p, e
	}
	return &compensationBarrierReceipt{Publication: p, store: s}, nil
}
func (s *compensationBarrierStore) ReconcileAccepted(ctx context.Context, work string, a *noderecipe.AcceptedPublication) error {
	if a != nil && a.Card != nil && a.Card.InputPath == "/approved/prior" {
		s.pause()
	}
	return s.Store.ReconcileAccepted(ctx, work, a)
}

type compensationBarrierReceipt struct {
	noderecipe.Publication
	store *compensationBarrierStore
}

func (r *compensationBarrierReceipt) Undo(ctx context.Context) error {
	r.store.pause()
	return r.Publication.Undo(ctx)
}

func TestRecipeSQLRejectionKeepsLifecycleSerializedThroughCompensation(t *testing.T) {
	pool, _, recipes, state := recipeDurableFixture(t)
	work, card := recipeFixtureSession(t, state, recipes, true)
	_, priorVersion, err := recipes.RecipeVersion(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `CREATE FUNCTION reject_publication_probe() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.data->'Recipe'->>'input_path'='/approved/rejected' THEN RAISE EXCEPTION 'fixture rejects candidate'; END IF;RETURN NEW;END $$; CREATE TRIGGER reject_publication_probe BEFORE UPDATE ON jellycompat_playback_sessions FOR EACH ROW EXECUTE FUNCTION reject_publication_probe()`); err != nil {
		t.Fatal(err)
	}
	barrier := &compensationBarrierStore{Store: recipes, entered: make(chan struct{}), resume: make(chan struct{})}
	release := sync.OnceFunc(func() { close(barrier.resume) })
	defer release()
	card.InputPath = "/approved/rejected"
	done := make(chan error, 1)
	go func() { done <- state.PublishRecipe(t.Context(), work, work, card, barrier) }()
	select {
	case <-barrier.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("SQL rejection did not reach compensation")
	}
	cfg := pool.Config().Copy()
	app := "lifecycle-probe-" + uuid.NewString()
	cfg.ConnConfig.RuntimeParams["application_name"] = app
	otherPool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	replica := NewDurableCompatPlaybackStore(otherPool, time.Hour, nil)
	// This is the existing method-switch order: its previously captured recipe
	// deletion is exact-version guarded, and generic Update clears/moves the row.
	if err = recipes.DeleteVersion(t.Context(), work, priorVersion); err != nil {
		t.Fatal(err)
	}
	changed := make(chan error, 1)
	go func() {
		changed <- replica.Update(work, func(s *PlaybackSession) error {
			s.UpstreamSessionID = work + "-moved"
			s.Recipe = nil
			s.TranscodeStarted = false
			return nil
		})
	}()
	deadline := time.After(3 * time.Second)
	early := false
	observed := false
	for !observed {
		select {
		case err = <-changed:
			if err != nil {
				t.Fatal(err)
			}
			early = true
			observed = true
		case <-deadline:
			t.Fatal("lifecycle writer neither completed nor waited on row lock")
		case <-time.After(time.Millisecond):
			var waiting bool
			if err = pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, app).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				observed = true
			}
		}
	}
	release()
	if err = <-done; err == nil {
		t.Fatal("rejected SQL candidate reported success")
	}
	if !early {
		if err = <-changed; err != nil {
			t.Fatal(err)
		}
		return
	}
	durable := readRecipeRow(t, pool, work)
	got, found := recipes.Get(t.Context(), work)
	path := "<missing>"
	if got != nil {
		path = got.InputPath
	}
	t.Fatalf("SQL rejection released row lock before compensation: lifecycle moved=%v recipe_cleared=%v; late undo restored Redis found=%v path=%q", durable.UpstreamSessionID != work, durable.Recipe == nil, found, path)
}

func TestRecipePublicationCanReplaceAdoptedLegacyPublication(t *testing.T) {
	pool, client, recipes, state := recipeDurableFixture(t)
	work, card := recipeFixtureSession(t, state, recipes, true)
	_, version, err := recipes.RecipeVersion(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.Update(work, func(s *PlaybackSession) error { s.RecipePublicationID = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	// Exact pre-publication flat record: adoption through the existing current
	// RecipeVersion API creates a token but no new publication receipt.
	if err = client.Del(t.Context(), "silo:noderecipe-publication:"+work, "silo:noderecipe-undo:"+version).Err(); err != nil {
		t.Fatal(err)
	}
	if got, adopted, e := recipes.RecipeVersion(t.Context(), work); e != nil || got == nil || adopted == "" {
		t.Fatal("legacy adoption failed", e)
	}
	if _, ok := recipes.Get(t.Context(), work); !ok {
		t.Fatal("legacy recipe was not serving before replacement")
	}
	card.InputPath = "/approved/replacement"
	if err = state.PublishRecipe(t.Context(), work, work, card, recipes); err != nil {
		t.Fatalf("valid legacy record cannot be replaced after current RecipeVersion adoption: %v", err)
	}
	if row := readRecipeRow(t, pool, work); row.Recipe == nil || row.Recipe.InputPath != card.InputPath {
		t.Fatal(errors.New("replacement was not committed"))
	}
}

type unavailablePublicationAckStore struct{ *noderecipe.Store }

func (*unavailablePublicationAckStore) ReconcileAccepted(context.Context, string, *noderecipe.AcceptedPublication) error {
	return context.DeadlineExceeded
}

func TestCommittedRecipeAcknowledgementOutageDoesNotStopAcceptedTransport(t *testing.T) {
	pool, _, recipes, state := recipeDurableFixture(t)
	deleted := make(chan string, 1)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted <- r.URL.Path
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/transcode/start" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var request transcodenode.TranscodeStartRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started", AudioRecipeVersion: request.AudioRecipeVersion, CopyFMP4RecipeVersion: request.CopyFMP4RecipeVersion, ThrottleSeconds: request.ThrottleSeconds})
	}))
	defer node.Close()
	handler, _, _ := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	handler.playbackStore = state
	handler.RecipeNodeStore = &unavailablePublicationAckStore{recipes}
	state.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", CompatToken: "fixture", UserID: "fixture-user"})
	err := handler.startRemoteTranscode(t.Context(), "play-1", "upstream-1", testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL)
	row := readRecipeRow(t, pool, "play-1")
	card, found := recipes.Get(t.Context(), "upstream-1")
	if !row.TranscodeStarted || row.Recipe == nil || row.RecipePublicationID == "" || !found || card.InputPath != row.Recipe.InputPath {
		t.Fatal("fixture did not reach accepted matching DB/Redis publication", err)
	}
	select {
	case path := <-deleted:
		t.Fatalf("known accepted publication returned %v and stopped transport %s despite durable Started=true publication=%s", err, path, row.RecipePublicationID)
	default:
	}
	if err != nil {
		t.Fatalf("known accepted publication reported failure after acknowledgement outage: %v", err)
	}
}

func TestReconciledRecipeCommitConsumesAlreadyAppliedPendingUpdates(t *testing.T) {
	pool, _, recipes, state := recipeDurableFixture(t)
	work, card := recipeFixtureSession(t, state, recipes, true)
	fault := &recipeResponseFault{target: "commit"}
	cfg := pool.Config().Copy()
	cfg.MaxConns = 2
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		c, e := (&net.Dialer{}).DialContext(ctx, network, address)
		if e != nil {
			return nil, e
		}
		return &recipeFaultConn{Conn: c, fault: fault, startup: true}, nil
	}
	affected, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer affected.Close()
	replica := NewDurableCompatPlaybackStore(affected, time.Hour, nil)
	if _, err = pool.Exec(t.Context(), `CREATE FUNCTION reject_pending_probe() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.data->>'ClientDeviceID'='queued-device' THEN RAISE EXCEPTION 'temporary persistence failure'; END IF;RETURN NEW;END $$; CREATE TRIGGER reject_pending_probe BEFORE UPDATE ON jellycompat_playback_sessions FOR EACH ROW EXECUTE FUNCTION reject_pending_probe()`); err != nil {
		t.Fatal(err)
	}
	if err = replica.Update(work, func(s *PlaybackSession) error { s.ClientDeviceID = "queued-device"; return nil }); err == nil {
		t.Fatal("fixture did not queue failed generic update")
	}
	if len(replica.pendingUpdatesSnapshot(work)) != 1 {
		t.Fatal("failed update not queued")
	}
	if _, err = pool.Exec(t.Context(), `DROP TRIGGER reject_pending_probe ON jellycompat_playback_sessions`); err != nil {
		t.Fatal(err)
	}
	fault.armed.Store(true)
	card.InputPath = "/approved/accepted-with-pending"
	if err = replica.PublishRecipe(t.Context(), work, work, card, recipes); err != nil {
		t.Fatal(err)
	}
	if fault.dropped.Load() != 1 {
		t.Fatal("actual COMMIT acknowledgement was not lost")
	}
	remaining := len(replica.pendingUpdatesSnapshot(work))
	other := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	if err = other.Update(work, func(s *PlaybackSession) error { s.ClientDeviceID = "newer-replica-device"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = replica.Update(work, func(s *PlaybackSession) error { s.ClientIP = "later-progress"; return nil }); err != nil {
		t.Fatal(err)
	}
	current := readRecipeRow(t, pool, work)
	if remaining != 0 || current.ClientDeviceID != "newer-replica-device" {
		t.Fatalf("reconciled committed recipe retained %d already-applied callbacks, later progress changed successor device to %q", remaining, current.ClientDeviceID)
	}
}

type uncertainRecipePlaybackStore struct{ CompatPlaybackStore }

func (s uncertainRecipePlaybackStore) PublishRecipe(context.Context, string, string, playback.RecipeCard, recipeNodePutter) error {
	return noderecipe.ErrPublicationUncertain
}

func recipeCallerNode(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	starts, deletes := &atomic.Int32{}, &atomic.Int32{}
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/transcode/start" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var request transcodenode.TranscodeStartRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		starts.Add(1)
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started", AudioRecipeVersion: request.AudioRecipeVersion, CopyFMP4RecipeVersion: request.CopyFMP4RecipeVersion, ThrottleSeconds: request.ThrottleSeconds})
	}))
	t.Cleanup(node.Close)
	return node, starts, deletes
}
func TestRecipePublicationUncertainStartKeepsRemoteTransport(t *testing.T) {
	node, starts, deletes := recipeCallerNode(t)
	handler, _, state := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	state.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	handler.playbackStore = uncertainRecipePlaybackStore{state}
	err := handler.startRemoteTranscode(t.Context(), "play-1", "upstream-1", testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL)
	if !errors.Is(err, noderecipe.ErrPublicationUncertain) {
		t.Fatal("unknown outcome was not preserved", err)
	}
	if starts.Load() != 1 || deletes.Load() != 0 {
		t.Fatalf("uncertain start changed transport: starts=%d deletes=%d", starts.Load(), deletes.Load())
	}
}
func TestRecipePublicationUncertainMasterDoesNotRetryOrTearDown(t *testing.T) {
	node, starts, deletes := recipeCallerNode(t)
	handler, manager, state := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	source := testRemoteTranscodeSource()
	state.Put(PlaybackSession{ID: "play-1", CompatToken: "compat-token", RouteItemID: "item", MediaSources: []PlaybackMediaSource{source}, UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode"})
	handler.playbackStore = uncertainRecipePlaybackStore{state}
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{{URL: node.URL, Enabled: true, Healthy: true}})
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	requireCompatWorkerRouting(handler)
	request := httptest.NewRequest(http.MethodGet, "/Videos/item/master.m3u8?PlaySessionId=play-1&MediaSourceId="+source.ID, nil)
	request = request.WithContext(context.WithValue(request.Context(), compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"}))
	recorder := httptest.NewRecorder()
	handler.HandleMasterManifest(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unknown outcome status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if starts.Load() != 1 || deletes.Load() != 0 || len(manager.stopCalls) != 0 {
		t.Fatalf("unknown outcome retried or tore down: starts=%d deletes=%d stops=%v", starts.Load(), deletes.Load(), manager.stopCalls)
	}
	if current, ok := state.Get("play-1"); !ok || current.Terminal {
		t.Fatal("unknown outcome retired the compat session")
	}
}
func TestRecipePublicationUncertainAudioKeepsAttemptedSelection(t *testing.T) {
	node, _, deletes := recipeCallerNode(t)
	handler, manager, state := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	source := testRemoteTranscodeSource()
	original := len(source.Version.VideoTracks)
	source.SelectedAudioStreamIndex = intPtr(original)
	state.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode", MediaSources: []PlaybackMediaSource{source}})
	handler.playbackStore = uncertainRecipePlaybackStore{state}
	session, _ := state.Get("play-1")
	attempted := original + 1
	_, _, _, err := handler.applyCompatAudioSelection(t.Context(), session, source.ID, attempted, 0)
	if !errors.Is(err, noderecipe.ErrPublicationUncertain) {
		t.Fatal("unknown outcome was not preserved", err)
	}
	current, _ := state.Get("play-1")
	if current.MediaSources[0].SelectedAudioStreamIndex == nil || *current.MediaSources[0].SelectedAudioStreamIndex != attempted || manager.sessions["upstream-1"].AudioTrackIndex != 1 {
		t.Fatalf("unknown outcome rolled back attempted audio: index=%v upstream=%d", current.MediaSources[0].SelectedAudioStreamIndex, manager.sessions["upstream-1"].AudioTrackIndex)
	}
	if deletes.Load() != 0 {
		t.Fatal("unknown audio outcome stopped remote transport")
	}
}

type recipeReconcileBeginBarrier struct {
	count           atomic.Int32
	entered, resume chan struct{}
}

func (b *recipeReconcileBeginBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.EqualFold(strings.TrimSpace(data.SQL), "begin") && b.count.Add(1) == 2 {
		close(b.entered)
		<-b.resume
	}
	return ctx
}
func (*recipeReconcileBeginBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func TestRejectedRecipeIsNotReadableAfterLifecycleWinsReconciliationGap(t *testing.T) {
	for _, successor := range []string{"none", "different", "identical"} {
		t.Run(successor, func(t *testing.T) {
			pool, _, recipes, state := recipeDurableFixture(t)
			work, card := recipeFixtureSession(t, state, recipes, true)
			_, priorVersion, err := recipes.RecipeVersion(t.Context(), work)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(t.Context(), `CREATE FUNCTION reject_gap_probe() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.data->'Recipe'->>'input_path'='/approved/rejected-gap' THEN RAISE EXCEPTION 'reject gap candidate'; END IF;RETURN NEW;END $$; CREATE TRIGGER reject_gap_probe BEFORE UPDATE ON jellycompat_playback_sessions FOR EACH ROW EXECUTE FUNCTION reject_gap_probe()`); err != nil {
				t.Fatal(err)
			}
			barrier := &recipeReconcileBeginBarrier{entered: make(chan struct{}), resume: make(chan struct{})}
			release := sync.OnceFunc(func() { close(barrier.resume) })
			defer release()
			cfg := pool.Config().Copy()
			cfg.ConnConfig.Tracer = barrier
			affected, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { release(); affected.Close() }()
			replica := NewDurableCompatPlaybackStore(affected, time.Hour, nil)
			card.InputPath = "/approved/rejected-gap"
			done := make(chan error, 1)
			go func() { done <- replica.PublishRecipe(t.Context(), work, work, card, recipes) }()
			select {
			case <-barrier.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("did not reach fresh reconciliation BEGIN")
			}
			if err = recipes.DeleteVersion(t.Context(), work, priorVersion); err != nil {
				t.Fatal(err)
			}
			other := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
			if err = other.Update(work, func(s *PlaybackSession) error {
				s.UpstreamSessionID = work + "-moved"
				s.Recipe = nil
				s.TranscodeStarted = false
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var version string
			if successor != "none" {
				replacement := card
				if successor == "different" {
					replacement.InputPath = "/approved/successor"
				}
				if err = recipes.Put(t.Context(), work, replacement); err != nil {
					t.Fatal(err)
				}
				_, version, err = recipes.RecipeVersion(t.Context(), work)
				if err != nil {
					t.Fatal(err)
				}
			}
			release()
			if err = <-done; err == nil {
				t.Fatal("rejected candidate returned success")
			}
			row := readRecipeRow(t, pool, work)
			got, found := recipes.Get(t.Context(), work)
			if successor == "none" {
				if found {
					t.Fatalf("SQL-rejected candidate still readable after lifecycle moved=%v recipe_cleared=%v: %q error=%v", row.UpstreamSessionID != work, row.Recipe == nil, got.InputPath, err)
				}
			} else {
				_, current, e := recipes.RecipeVersion(t.Context(), work)
				if e != nil || !found || current != version {
					t.Fatalf("retirement changed successor: found=%v version=%s error=%v", found, current, e)
				}
			}
		})
	}
}

func TestUncertainRecipeCommitDoesNotRetireMovedCandidate(t *testing.T) {
	pool, _, recipes, state := recipeDurableFixture(t)
	work, card := recipeFixtureSession(t, state, recipes, true)
	barrier := &recipeReconcileBeginBarrier{entered: make(chan struct{}), resume: make(chan struct{})}
	release := sync.OnceFunc(func() { close(barrier.resume) })
	defer release()
	fault := &recipeResponseFault{target: "commit"}
	config := pool.Config().Copy()
	config.ConnConfig.Tracer = barrier
	config.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &recipeFaultConn{Conn: c, fault: fault, startup: true}, nil
	}
	affected, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); affected.Close() }()
	replica := NewDurableCompatPlaybackStore(affected, time.Hour, nil)
	fault.armed.Store(true)
	card.InputPath = "/approved/ambiguous-commit"
	done := make(chan error, 1)
	go func() { done <- replica.PublishRecipe(t.Context(), work, work, card, recipes) }()
	select {
	case <-barrier.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("did not reach serialized reconciliation after lost COMMIT")
	}
	if fault.dropped.Load() != 1 {
		t.Fatal("actual COMMIT acknowledgement was not lost")
	}
	accepted := readRecipeRow(t, pool, work)
	if accepted.Recipe == nil || accepted.Recipe.InputPath != card.InputPath {
		t.Fatal("original transaction did not commit candidate")
	}
	_, version, err := recipes.RecipeVersion(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	other := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	if err = other.Update(work, func(s *PlaybackSession) error {
		s.UpstreamSessionID = work + "-moved"
		s.Recipe = nil
		s.TranscodeStarted = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release()
	if err = <-done; !recipePublicationUncertain(err) {
		t.Fatal("ambiguous lifecycle outcome was classified as definite rejection", err)
	}
	got, current, err := recipes.RecipeVersion(t.Context(), work)
	if err != nil || got == nil || got.InputPath != card.InputPath || current != version {
		t.Fatal("ambiguous committed candidate was retired or prior restored", err)
	}
}

func TestRejectedRemoteStartDoesNotStopAnotherReplicaSuccessor(t *testing.T) {
	for _, alias := range []bool{false, true} {
		for _, identical := range []bool{false, true} {
			name := "same-row"
			if alias {
				name = "alias-row"
			}
			if identical {
				name += "-identical"
			} else {
				name += "-different"
			}
			t.Run(name, func(t *testing.T) { testRejectedRemoteStartSuccessor(t, alias, identical) })
		}
	}
}

//nolint:funlen // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func testRejectedRemoteStartSuccessor(t *testing.T, alias, identical bool) {
	pool, _, recipes, state := recipeDurableFixture(t)
	work := uuid.NewString()
	secondID := work
	if alias {
		secondID = work + "-alias"
	}
	var starts, killed atomic.Int32
	var alive atomic.Bool
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			killed.Store(starts.Load())
			alive.Store(false)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/transcode/start" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req transcodenode.TranscodeStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		starts.Add(1)
		alive.Store(true)
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: req.SessionID, Status: "started", AudioRecipeVersion: req.AudioRecipeVersion, CopyFMP4RecipeVersion: req.CopyFMP4RecipeVersion, ThrottleSeconds: req.ThrottleSeconds})
	}))
	defer node.Close()
	state.Put(PlaybackSession{ID: work, UpstreamSessionID: work, CompatToken: "fixture", UserID: "fixture-user"})
	if alias {
		state.Put(PlaybackSession{ID: secondID, UpstreamSessionID: work, CompatToken: "fixture", UserID: "fixture-user"})
	}
	appA := "remote-first-" + uuid.NewString()
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_first_start() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF current_setting('application_name')='`+appA+`' AND NEW.data->'Recipe'->>'input_path'='/approved/rejected-start' THEN RAISE EXCEPTION 'first publication rejected'; END IF;RETURN NEW;END $$; CREATE TRIGGER reject_first_start BEFORE UPDATE ON jellycompat_playback_sessions FOR EACH ROW EXECUTE FUNCTION reject_first_start()`); err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	firstRecipes := &failedRecipePublicationStore{Store: recipes, after: func(ctx context.Context, _ string, _ playback.RecipeCard) error {
		close(entered)
		select {
		case <-resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	ac := pool.Config().Copy()
	ac.MaxConns = 1
	ac.ConnConfig.RuntimeParams["application_name"] = appA
	ap, err := pgxpool.NewWithConfig(t.Context(), ac)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); ap.Close() }()
	a, am, _ := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	a.playbackStore = NewDurableCompatPlaybackStore(ap, time.Hour, nil)
	a.RecipeNodeStore = firstRecipes
	am.sessions[work] = am.sessions["upstream-1"]
	am.sessions[work].ID = work
	delete(am.sessions, "upstream-1")
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	app := "remote-successor-" + uuid.NewString()
	cfg.ConnConfig.RuntimeParams["application_name"] = app
	bp, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); bp.Close() }()
	b, bm, _ := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	b.playbackStore = NewDurableCompatPlaybackStore(bp, time.Hour, nil)
	b.RecipeNodeStore = recipes
	bm.sessions[work] = bm.sessions["upstream-1"]
	bm.sessions[work].ID = work
	delete(bm.sessions, "upstream-1")
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		firstDone <- a.startRemoteTranscode(t.Context(), work, work, testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/approved/rejected-start"}, 0, node.URL)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first start did not reach publication")
	}
	path := "/approved/accepted-successor"
	if identical {
		path = "/approved/rejected-start"
	}
	go func() {
		secondDone <- b.startRemoteTranscode(t.Context(), secondID, work, testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: path}, 0, node.URL)
	}()
	deadline := time.After(3 * time.Second)
	for {
		var waiting bool
		if err = pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, app).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-deadline:
			t.Fatal("second start never waited on shared work ownership")
		case <-time.After(time.Millisecond):
		}
	}
	if starts.Load() != 1 {
		t.Errorf("successor POST ran before shared ownership: starts=%d", starts.Load())
	}
	release()
	if err = <-firstDone; err == nil {
		t.Fatal("first rejected start returned success")
	}
	if err = <-secondDone; err != nil {
		t.Fatal("second start failed", err)
	}
	row := readRecipeRow(t, pool, secondID)
	if row.Recipe == nil || row.Recipe.InputPath != path || !row.TranscodeStarted {
		t.Fatal("successor was not durably accepted")
	}
	if !alive.Load() || killed.Load() != 1 || starts.Load() != 2 {
		t.Fatalf("failed publisher killed successor: killed=%d starts=%d alive=%v", killed.Load(), starts.Load(), alive.Load())
	}
}
