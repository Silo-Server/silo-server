package main

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/planstore"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func overlapTransport(t *testing.T, f *reportFixture, work string, card playback.RecipeCard, codec string) workerwire.SessionReport {
	t.Helper()
	a := f.activate(t, workerwire.Assignment{Version: 1, NodeID: f.node.ID, WorkID: work, Path: "/transcode/start"})
	if err := f.sink.recipes.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.sink.recipes.Delete(context.Background(), work) })
	return workerwire.SessionReport{WorkID: work, Generation: a.Generation, Info: nodesessions.SessionInfo{SessionID: work, Type: "transcode", CodecVideo: codec, StartedAt: "2099-01-01T00:00:00Z", AuthUserID: 999, ProfileID: "forged-profile", MediaFileID: 999}}
}

type reportPlanQueryCounter struct{ queries atomic.Int64 }

func (c *reportPlanQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.queries.Add(1)
	return ctx
}
func (*reportPlanQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func overlapPlanStores(t *testing.T) (*pgxpool.Pool, [3]*planstore.Postgres, *reportPlanQueryCounter) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	base, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "report_overlap_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := base.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		base.Close()
	})
	// Use the actual migrated table layouts, with independent fixture rows and
	// no copied foreign keys into the shared test database's account/catalog.
	for _, name := range []string{"playback_v3_attempts", "playback_v3_replans"} {
		if _, err := base.Exec(t.Context(), "CREATE TABLE "+quoted+"."+pgx.Identifier{name}.Sanitize()+" (LIKE public."+pgx.Identifier{name}.Sanitize()+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	var writer *pgxpool.Pool
	var stores [3]*planstore.Postgres
	counter := &reportPlanQueryCounter{}
	for i := range stores {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
		if i != 0 {
			cfg.ConnConfig.Tracer = counter
		}
		pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		stores[i] = planstore.NewPostgres(pool)
		if i == 0 {
			writer = pool
		}
	}
	return writer, stores, counter
}

//nolint:funlen,gocyclo // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func TestWorkerReportOverlappingTransportsUseCommittedRoute(t *testing.T) {
	writer, plans, counter := overlapPlanStores(t)
	f := newReportFixture(t)
	f.card.SessionID = uuid.NewString()
	if err := f.sink.recipes.Put(t.Context(), f.assignment.WorkID, f.card); err != nil {
		t.Fatal(err)
	}
	f.sink.plans = plans[1]
	replica := f.replica(t)
	replica.plans = plans[2]
	f.assignment = f.activate(t, f.assignment)
	f.assignment = f.activate(t, f.assignment)
	old := f.session()
	old.Info.SessionID, old.Info.CodecVideo, old.Info.StartedAt = old.WorkID, "h264", "2020-01-01T00:00:00Z"
	newCard := f.card
	newCard.MediaFileID++
	candidate := overlapTransport(t, f, "transport:z-"+uuid.NewString(), newCard, "hevc")
	otherCard := f.card
	otherCard.SessionID, otherCard.UserID, otherCard.ProfileID, otherCard.MediaFileID = uuid.NewString(), 52, "other-profile", 84
	other := overlapTransport(t, f, "other:"+uuid.NewString(), otherCard, "av1")
	stopped := overlapTransport(t, f, "stopped:"+uuid.NewString(), f.card, "stopped")
	if err := f.store.Stop(t.Context(), f.node.ID, stopped.WorkID); err != nil {
		t.Fatal(err)
	}
	stale := overlapTransport(t, f, "stale:"+uuid.NewString(), f.card, "stale")
	f.activate(t, workerwire.Assignment{Version: 1, NodeID: f.node.ID, WorkID: stale.WorkID, Path: "/transcode/start"})
	foreignCard := f.card
	foreignCard.TranscodeNodeURL = "https://foreign.example.test"
	foreign := overlapTransport(t, f, "foreign:"+uuid.NewString(), foreignCard, "foreign")
	phantom := candidate
	phantom.Info.SessionID = "phantom-" + uuid.NewString()
	record := playback.AttemptRecordV3{PlaybackAttemptID: "overlap-" + uuid.NewString(), SessionID: f.card.SessionID, UserID: f.card.UserID, ProfileID: f.card.ProfileID, RequestedMediaFileID: f.card.MediaFileID, EffectiveMediaFileID: f.card.MediaFileID, CurrentPlanID: "old-plan", CurrentPlan: playback.PlanV3{PlanID: "old-plan", SessionID: f.card.SessionID}, FrozenRecipe: playback.ExecutableRecipeV3{Version: 1, PlanID: "old-plan", PlayMethod: playback.PlayTranscode, TranscodeNodeURL: f.node.URL, TranscodeTransportID: old.WorkID}, ExpiresAt: time.Now().Add(time.Hour)}
	if err := plans[0].SaveAttempt(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	var sequence uint64
	check := func(name string, current *workerwire.SessionReport, currentCard playback.RecipeCard, withOverlap bool) {
		t.Helper()
		for order := range 2 {
			sequence++
			items := []workerwire.SessionReport{old, candidate}
			if !withOverlap {
				items = items[:1]
			} else if order == 1 {
				items[0], items[1] = items[1], items[0]
			}
			o := f.observations(int64(sequence * 100))
			o.Scopes = nil
			for _, item := range items {
				o.Scopes = append(o.Scopes, workerwire.ObservationScope{WorkID: item.WorkID, Generation: item.Generation, SessionID: f.card.SessionID})
			}
			o.Scopes = append(o.Scopes, workerwire.ObservationScope{WorkID: other.WorkID, Generation: other.Generation, SessionID: otherCard.SessionID})
			o.Snapshot.Sessions = append(o.Snapshot.Sessions, streamtelemetry.SessionView{SessionID: otherCard.SessionID, BytesAccepted: 25})
			batch := workerwire.Report{BootID: "overlap-boot", Sequence: sequence, Sessions: append(items, other, stopped, stale, foreign, phantom), Observations: o}
			sink := f.sink
			if order == 1 {
				sink = replica
			}
			before := counter.queries.Load()
			if err := sink.accept(t.Context(), f.node.ID, reportJSON(t, batch)); err != nil {
				t.Fatalf("%s order %d rejected whole report: %v", name, order, err)
			}
			wantQueries := int64(0)
			if withOverlap {
				wantQueries = 1
			}
			info, found := f.load(t, f.card.SessionID)
			if current == nil {
				if found {
					t.Fatalf("%s selected an uncommitted transport: %+v", name, info)
				}
			} else {
				expected := current.Info
				expected.SessionID, expected.NodeURL, expected.NodeName = f.card.SessionID, f.node.URL, "HTTP worker"
				expected.AuthUserID, expected.ProfileID, expected.MediaFileID = currentCard.UserID, currentCard.ProfileID, currentCard.MediaFileID
				if !found || info != expected {
					t.Fatalf("%s selected worker metadata instead of the core committed transport: got=%+v want=%+v", name, info, expected)
				}
			}
			if added := counter.queries.Load() - before; added != wantQueries {
				t.Fatalf("%s queried plan %d times; want %d for ambiguous groups only", name, added, wantQueries)
			}
			if unrelated, found := f.load(t, otherCard.SessionID); !found || unrelated.AuthUserID != otherCard.UserID || unrelated.MediaFileID != otherCard.MediaFileID {
				t.Fatalf("%s discarded unrelated session: %+v", name, unrelated)
			}
			for _, id := range []string{old.WorkID, candidate.WorkID, stopped.WorkID, stale.WorkID, foreign.WorkID, phantom.Info.SessionID} {
				if _, found := f.load(t, id); found {
					t.Fatalf("%s published unauthorized/raw transport identity %q", name, id)
				}
			}
			snapshot, err := sink.publishers[f.node.ID].Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if current != nil {
				wantCount++
			}
			if snapshot.Sequence != sequence || len(snapshot.Sessions) != wantCount {
				t.Fatalf("%s lost unrelated telemetry or kept uncommitted overlap: %+v", name, snapshot)
			}
			for _, view := range snapshot.Sessions {
				if view.SessionID == f.card.SessionID && (view.Subject.ID != strconv.Itoa(currentCard.UserID) || view.ProfileID != currentCard.ProfileID || view.MediaFileID != currentCard.MediaFileID || view.BytesAccepted != int64(sequence*100)) {
					t.Fatalf("%s telemetry used a retiring/candidate scope: %+v", name, view)
				}
			}
		}
	}
	lease, err := plans[0].BeginReplan(t.Context(), record.SessionID, "failed-switch", "failed-digest", "", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	check("newer candidate pending", &old, f.card, true)
	failed := record
	failed.CurrentPlanID, failed.CurrentPlan.PlanID, failed.CurrentReplanRequestID = "failed-plan", "failed-plan", "failed-switch"
	failed.FrozenRecipe.PlanID, failed.FrozenRecipe.TranscodeTransportID = "failed-plan", candidate.WorkID
	failed.EffectiveMediaFileID = newCard.MediaFileID
	// CompleteReplan updates the attempt before checking the replan lease.
	// Losing that lease must roll the candidate tuple back with the transaction.
	if err := plans[0].CompleteReplan(t.Context(), record.SessionID, "failed-switch", uuid.NewString(), "", json.RawMessage(`{"outcome":"playable"}`), failed); err == nil {
		t.Fatal("a replacement without the replan lease committed")
	}
	check("failed durable commit rolls back candidate", &old, f.card, true)
	if err := plans[0].ReleaseReplan(t.Context(), record.SessionID, "failed-switch", lease.LeaseToken); err != nil {
		t.Fatal(err)
	}
	check("failed replacement leaves old live", &old, f.card, true)
	lease, err = plans[0].BeginReplan(t.Context(), record.SessionID, "committed-switch", "commit-digest", "", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	record.CurrentPlanID, record.CurrentPlan.PlanID, record.CurrentReplanRequestID = "new-plan", "new-plan", "committed-switch"
	record.FrozenRecipe.PlanID, record.FrozenRecipe.TranscodeTransportID = "new-plan", candidate.WorkID
	record.EffectiveMediaFileID = newCard.MediaFileID
	if err := plans[0].CompleteReplan(t.Context(), record.SessionID, "committed-switch", lease.LeaseToken, "", json.RawMessage(`{"outcome":"playable"}`), record); err != nil {
		t.Fatal(err)
	}
	// The retiring worker can report a later timestamp and a larger per-work
	// generation; neither can override the committed route seen by a replica.
	old.Info.StartedAt, candidate.Info.StartedAt = "2199-01-01T00:00:00Z", "1900-01-01T00:00:00Z"
	check("durable commit selects new", &candidate, newCard, true)
	locked, err := writer.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Rollback(context.Background()) }()
	if _, err := locked.Exec(t.Context(), "LOCK TABLE playback_v3_attempts IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	check("blocked plan lookup preserves unrelated report", nil, playback.RecipeCard{}, true)
	if err := locked.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("plan lookup recovers committed route", &candidate, newCard, true)
	if _, err := writer.Exec(t.Context(), "UPDATE playback_v3_attempts SET frozen_recipe=jsonb_set(frozen_recipe,'{transcode_node_url}',to_jsonb($2::text)) WHERE session_id=$1", record.SessionID, "https://other-node.example.test"); err != nil {
		t.Fatal(err)
	}
	check("committed route on another node is omitted", nil, playback.RecipeCard{}, true)
	if _, err := writer.Exec(t.Context(), "UPDATE playback_v3_attempts SET frozen_recipe=frozen_recipe-'transcode_node_url'-'transcode_transport_id' WHERE session_id=$1", record.SessionID); err != nil {
		t.Fatal(err)
	}
	check("old attempt without tuple omits overlap", nil, playback.RecipeCard{}, true)
	check("single valid entry needs no plan lookup", &old, f.card, false)
}

func TestWorkerReportDuplicateTransportRejected(t *testing.T) {
	f := newReportFixture(t)
	first := f.session()
	second := first
	first.Info.CodecVideo, second.Info.CodecVideo = "h264", "hevc"
	if err := f.accept(t, workerwire.Report{BootID: "duplicate-boot", Sequence: 1, Sessions: []workerwire.SessionReport{first, second}, Observations: f.observations(100)}); err == nil {
		t.Fatal("repeated transport entries with conflicting metadata were accepted")
	}
	if _, found := f.load(t, f.card.SessionID); found {
		t.Fatal("invalid duplicate transport published a session")
	}
}
