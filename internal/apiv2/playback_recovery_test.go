package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/danielgtaylor/huma/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/planstore"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recoveryHTTPFixture struct {
	pool        *pgxpool.Pool
	store       *planstore.Postgres
	source      userstore.PlaybackSourceProvider
	sink        userstore.PlaybackSinkHandle
	binding     playback.InitialActivationBindingV3
	record      playback.AttemptRecordV3
	body        map[string]any
	user        int
	handler     http.Handler
	application *handlers.PlaybackHandler
}

// Every fence below comes from the real reservation CAS and every receipt from
// the selected PG/SQLite sink. The router uses normal auth/profile gates with
// fixture login stores. Recovery must never need a media producer or catalog.
func newRecoveryHTTPFixture(t *testing.T, backend, phase string) *recoveryHTTPFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("owner-loss HTTP requires an owned migrated SILO_TEST_DATABASE_URL")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := &recoveryHTTPFixture{pool: pool, body: playbackStartFixture(t)}
	var folder, file int
	if err = pool.QueryRow(ctx, `INSERT INTO users(username) VALUES($1) RETURNING id`, "owner-loss-http-"+uuid.NewString()).Scan(&f.user); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('movies',$1) RETURNING id`, uuid.NewString()).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id=$1`, f.user)
		_, _ = pool.Exec(c, `DELETE FROM media_folders WHERE id=$1`, folder)
	})
	if err = pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`, folder, "owner-loss-"+uuid.NewString()).Scan(&file); err != nil {
		t.Fatal(err)
	}
	f.body["file_id"], f.body["playback_attempt_id"] = fmt.Sprint(file), uuid.NewString()
	f.body["audio_track_id"] = fmt.Sprintf("file:%d:audio:0", file)
	if phase == "bound" {
		f.body["progress_persistence"] = "client_bound"
		f.body["timeline_id"] = strings.Repeat("a", 64)
		f.body["client_features"] = []string{playback.FeaturePlaybackPlanV3, playback.FeatureBoundClientTimelineV3}
	}
	var wire PlaybackStartBody
	if err = json.Unmarshal([]byte(playbackJSON(t, f.body)), &wire); err != nil {
		t.Fatal(err)
	}
	req := wire.domain(file)
	// This is the actual DTO encoding sent to the application. The empty device
	// header participates in the same length-delimited digest as production.
	encoded, _ := json.Marshal(req)
	sum := sha256.Sum256(append(fmt.Appendf(nil, "%d:", len(encoded)), encoded...))
	digest := hex.EncodeToString(sum[:])
	f.store, err = planstore.NewPostgresWithGrantPolicy(pool, playback.AttemptGrantPolicyV3{MaxDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ref := userstore.PlaybackSourceRef{Backend: backend, AccountID: f.user, SourceID: uuid.NewString(), SelectionGeneration: 1}
	admission := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO playback_source_registrations(user_id,backend,source_id,selection_generation,admission_id,admission_state) VALUES($1,$2,$3,1,$4,'admitting')`, f.user, backend, ref.SourceID, admission); err != nil {
		t.Fatal(err)
	}
	reservation, err := f.store.ReserveAttempt(ctx, playback.AttemptReservationRequestV3{ExpectedAdmissionID: admission, PlaybackAttemptID: req.PlaybackAttemptID, UserID: f.user, ProfileID: req.ProfileID, RequestedMediaFileID: file, RequestDigest: digest, NormalizedRequest: req, OwnerID: uuid.NewString(), LeaseDuration: time.Minute, Retention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	a := reservation.Authority
	b := playback.InitialActivationBindingV3{Source: ref, AdmissionID: admission, IntentID: uuid.NewString(), Scope: userstore.PlaybackProgressScope{ProfileID: req.ProfileID, SessionID: uuid.NewString(), MediaItemID: "owner-loss-item"}, Fence: userstore.PlaybackProgressFence{AttemptID: a.PlaybackAttemptID, OwnerID: a.OwnerID, Incarnation: a.Incarnation, Epoch: a.Epoch}}
	if phase == "bound" {
		b.ClientTimeline = playback.ClientPlaybackTimelineV3{TimelineID: strings.Repeat("a", 64), MediaItemID: b.Scope.MediaItemID, FileID: file, PartOffsetSeconds: 48, PartDurationSeconds: 52, DurationSeconds: 100}
		b.Progress.DurationSeconds = 100
		b.Progress.Hints.FileID = file
	}
	f.binding = b
	if _, err = f.store.BeginInitialActivation(ctx, b); err != nil {
		t.Fatal(err)
	}
	if backend == "sqlite" {
		dir := t.TempDir()
		db, err := userdb.NewUserDB(filepath.Join(dir, fmt.Sprintf("%d.db", f.user)), f.user)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.DB.Exec(`INSERT INTO playback_source_markers(user_id,source_id,selection_generation,gate) VALUES(?,?,1,'writable')`, f.user, ref.SourceID); err != nil {
			t.Fatal(err)
		}
		if err = userdb.NewSQLiteUserStore(db.DB).CreateProfile(ctx, userstore.Profile{ID: req.ProfileID, Name: "Recovery"}); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		p := userdb.NewSQLiteProvider(userdb.NewUserDBPool(userdb.PoolConfig{DataDir: dir}))
		t.Cleanup(func() { _ = p.Close() })
		f.source = p
	} else {
		p := pgstore.NewPostgresProvider(pool)
		f.source = p
		if _, err = pool.Exec(ctx, `INSERT INTO playback_source_markers(user_id,source_id,selection_generation,gate) VALUES($1,$2,1,'writable')`, f.user, ref.SourceID); err != nil {
			t.Fatal(err)
		}
		store, err := p.ForUser(ctx, f.user)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.CreateProfile(ctx, userstore.Profile{ID: req.ProfileID, Name: "Recovery"}); err != nil {
			t.Fatal(err)
		}
	}
	f.sink, err = f.source.OpenPlaybackSink(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.sink.Close() })
	f.record = playback.AttemptRecordV3{PlaybackAttemptID: req.PlaybackAttemptID, SessionID: b.Scope.SessionID, UserID: f.user, ProfileID: req.ProfileID, RequestedMediaFileID: file, EffectiveMediaFileID: file, CurrentPlanID: "recovery-plan", NormalizedRequest: req, RequestDigest: digest, ExpiresAt: time.Now().Add(time.Hour), CurrentPlan: playback.PlanV3{ProtocolVersion: 3, PlanID: "recovery-plan", SessionID: b.Scope.SessionID, RequestedMediaFileID: file, EffectiveMediaFileID: file}, FrozenRecipe: playback.ExecutableRecipeV3{Version: 1, PlanID: "recovery-plan", PlayMethod: playback.PlayDirect, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}}
	f.record.StartResponse = playback.DecisionResponseV3{ProtocolVersion: 3, Outcome: playback.OutcomePlayableV3, SessionID: b.Scope.SessionID, PlaybackPlan: &f.record.CurrentPlan}
	if phase != "uninstalled" {
		if _, err = f.sink.InstallPlaybackAuthority(ctx, userstore.InstallPlaybackAuthorityRequest{Scope: b.Scope, Next: b.Fence}); err != nil {
			t.Fatal(err)
		}
		if phase != "pending" {
			receipt, err := playback.ReadInitialActivationReceiptV3(ctx, b, f.sink)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.AcknowledgeInitialInstallation(ctx, b, receipt); err != nil {
				t.Fatal(err)
			}
		}
	}
	if phase == "activated" || phase == "bound" {
		route := playback.AttemptGrantRouteV3{Executor: playback.ExecutorNamespaceV3{Incarnation: a.Incarnation, Epoch: a.Epoch, ExecutorID: uuid.NewString()}, TransportID: uuid.NewString(), ExecutionNodeID: 1, EgressNodeID: 2}
		if err = f.store.StageAttemptRoute(ctx, a, f.record, route); err != nil {
			t.Fatal(err)
		}
		if _, err = f.store.PublishInitialActivation(ctx, b, f.record); err != nil {
			t.Fatal(err)
		}
	}
	f.restart(t)
	return f
}

func (f *recoveryHTTPFixture) restart(t *testing.T) {
	t.Helper()
	// A new handler, session manager and boot owner have no old process state.
	manager := playback.NewSessionManager(0, 0)
	h := handlers.NewPlaybackHandler(manager, nil)
	clock, err := playback.NewRuntimeGrantClockV3()
	if err != nil {
		t.Fatal(err)
	}
	policy := playback.RuntimeGrantPolicyV3{MaxDuration: time.Second, SafetyMargin: 100 * time.Millisecond, RenewBefore: 200 * time.Millisecond, PollInterval: time.Millisecond}
	// No Redis access: recovery only uses the durable control and captured sink.
	recipes := noderecipe.NewStore(nil, time.Minute)
	runtime, err := planstore.NewExecutorRuntime(f.store, recipes, 0, clock, policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.ConfigureInitialPlaybackV3(&handlers.InitialPlaybackFlowV3{InstallationID: playbackTestInstallation, Control: f.store, Sources: f.source, Recipes: recipes, OwnerID: uuid.NewString(), Context: t.Context(), Clock: clock, Policy: policy, AcquireGrant: runtime.Acquire, ResolveRecipe: runtime.Resolve}); err != nil {
		t.Fatal(err)
	}
	deps := parityDeps(false)
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{map[string]*auth.Claims{memberToken: {UserID: f.user, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess}, adminToken: {UserID: f.user + 1, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess}}}, fakeSessions{map[string]bool{"s1": true}}, nil, nil)
	deps.Playback = h
	f.application = h
	f.handler = NewHandler(deps)
}
func (f *recoveryHTTPFixture) expire(t *testing.T) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `UPDATE playback_v3_attempts SET control_lease_expires_at=clock_timestamp()-interval '1 second' WHERE playback_attempt_id=$1`, f.binding.Fence.AttemptID); err != nil {
		t.Fatal(err)
	}
}
func (f *recoveryHTTPFixture) call(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, f.handler, method, Prefix+"/playback"+path, playbackJSON(t, body), viewerHeaders())
}
func assertRecovery(t *testing.T, response *httptest.ResponseRecorder, status int, b playback.InitialActivationBindingV3, accepted bool) map[string]any {
	t.Helper()
	if response.Code != status {
		t.Fatalf("recovery HTTP %d: %s", response.Code, response.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	registry := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	responseType := reflect.TypeFor[PlaybackRecoveryPending]()
	if status == 201 {
		responseType = reflect.TypeFor[PlaybackRecoveryStart]()
	}
	if status == 200 {
		responseType = reflect.TypeFor[PlaybackRecoveryStop]()
	}
	validation := new(huma.ValidateResult)
	huma.Validate(registry, huma.SchemaFromType(registry, responseType), huma.NewPathBuffer([]byte("body"), 0), huma.ModeReadFromServer, doc, validation)
	if len(validation.Errors) > 0 {
		t.Fatalf("response violates source schema: %v", validation.Errors)
	}
	r, ok := doc["recovery"].(map[string]any)
	if !ok {
		t.Fatalf("missing recovery: %s", response.Body.String())
	}
	if r["playback_attempt_id"] != b.Fence.AttemptID || r["session_id"] != b.Scope.SessionID || r["reason"] != "owner_lost" || !playbackUUID(fmt.Sprint(r["recovery_id"])) {
		t.Fatalf("wrong identity: %v", r)
	}
	_, has := r["accepted"]
	if has != accepted {
		t.Fatalf("invented/lost sample: %v", r)
	}
	for _, key := range []string{"session_id", "playback_plan", "progress_timeline", "stop_id", "accepted", "history_id"} {
		if _, ok := doc[key]; ok {
			t.Fatalf("recovery contains %s", key)
		}
	}
	if status == 202 && (r["state"] != "draining" || doc["terminal"] != nil || has) {
		t.Fatalf("pending: %v", doc)
	}
	if status != 202 && r["state"] != "aborted" {
		t.Fatalf("terminal: %v", doc)
	}
	return r
}

func TestPlaybackOwnerLossHTTPRecovery(t *testing.T) {
	for _, backend := range []string{"postgres", "sqlite"} {
		for _, phase := range []string{"uninstalled", "pending", "installed", "activated", "bound"} {
			t.Run(backend+"/"+phase, func(t *testing.T) {
				f := newRecoveryHTTPFixture(t, backend, phase)
				last := phase == "activated" || phase == "bound"
				if last {
					sample := f.binding.Progress
					sample.Sequence, sample.PositionSeconds, sample.Paused = 7, 12.5+f.binding.ClientTimeline.PartOffsetSeconds, true
					if _, err := f.sink.ApplyPlaybackProgress(t.Context(), userstore.ApplyPlaybackProgressRequest{Scope: f.binding.Scope, Fence: f.binding.Fence, Sample: sample}); err != nil {
						t.Fatal(err)
					}
				}
				f.expire(t)
				// Discard the first response to model a lost terminal reply.
				first := f.call(t, "POST", "/start", f.body)
				receipt := assertRecovery(t, first, 201, f.binding, last)
				f.restart(t)
				replay := f.call(t, "POST", "/start", f.body)
				assertRecovery(t, replay, 201, f.binding, last)
				if first.Body.String() != replay.Body.String() {
					t.Fatal("restart changed retained recovery")
				}
				stop := map[string]any{"installation_id": playbackTestInstallation, "stop_id": playbackTestStop, "sequence": 8, "position": 999, "is_paused": false}
				if f.binding.ClientTimeline.TimelineID != "" {
					stop["timeline_id"] = f.binding.ClientTimeline.TimelineID
				}
				recovered := assertRecovery(t, f.call(t, "DELETE", "/"+f.binding.Scope.SessionID, stop), 200, f.binding, last)
				if recovered["recovery_id"] != receipt["recovery_id"] {
					t.Fatal("STOP minted another abort")
				}
				if last {
					a := recovered["accepted"].(map[string]any)
					if phase == "bound" && (a["timeline_id"] != f.binding.ClientTimeline.TimelineID || a["item_position"] != 60.5) {
						t.Fatal("lost bound clocks")
					}
					if a["sequence"] != float64(7) || a["position"] != 12.5 || a["is_paused"] != true {
						t.Fatal("applied uncommitted STOP sample")
					}
				}
				f.body["start_position"] = 14
				if r := f.call(t, "POST", "/start", f.body); r.Code != 409 {
					t.Fatalf("changed digest %d: %s", r.Code, r.Body.String())
				}
				if r := do(t, f.handler, "DELETE", Prefix+"/playback/"+f.binding.Scope.SessionID, playbackJSON(t, stop), with(viewerHeaders(), "Authorization", "Bearer "+adminToken)); r.Code < 400 {
					t.Fatal("wrong account recovered")
				}
				if r := do(t, f.handler, "DELETE", Prefix+"/playback/"+f.binding.Scope.SessionID, playbackJSON(t, stop), with(viewerHeaders(), "X-Profile-Id", "p-primary")); r.Code < 400 {
					t.Fatal("wrong profile recovered")
				}
				stop["installation_id"] = uuid.NewString()
				if r := f.call(t, "DELETE", "/"+f.binding.Scope.SessionID, stop); r.Code < 400 {
					t.Fatal("wrong installation recovered")
				}
			})
		}
	}
}

func TestPlaybackOwnerLossHTTPDrainAndOriginalStop(t *testing.T) {
	for _, backend := range []string{"postgres", "sqlite"} {
		t.Run(backend+"/drain", func(t *testing.T) {
			f := newRecoveryHTTPFixture(t, backend, "activated")
			// An explicit DB deadline makes draining deterministic, independent of CPU
			// speed. Advancing only this owned fixture's deadline replaces wall sleeps.
			if _, err := f.pool.Exec(t.Context(), `UPDATE playback_v3_attempts SET control_grant_not_after=clock_timestamp()+interval '30 seconds' WHERE playback_attempt_id=$1`, f.binding.Fence.AttemptID); err != nil {
				t.Fatal(err)
			}
			f.expire(t)
			first := assertRecovery(t, f.call(t, "POST", "/start", f.body), 202, f.binding, false)
			// An emitted recovery202 means AbortID already won the durable CAS.
			// A later original StopID must not change this attempt to ordinary STOP.
			if _, err := f.store.BeginBoundStop(t.Context(), f.binding, playbackTestStop); err == nil {
				t.Fatal("ordinary stop replaced observed recovery202")
			}

			f.restart(t)
			stop := map[string]any{"installation_id": playbackTestInstallation, "stop_id": playbackTestStop}
			second := assertRecovery(t, f.call(t, "DELETE", "/"+f.binding.Scope.SessionID, stop), 202, f.binding, false)
			if first["recovery_id"] != second["recovery_id"] {
				t.Fatal("pending identity changed")
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE playback_v3_attempts SET control_grant_not_after=clock_timestamp()-interval '1 second',control_drain_not_before=clock_timestamp()-interval '1 second',control_activation=jsonb_set(control_activation,'{drain_not_before}',to_jsonb(clock_timestamp()-interval '1 second')) WHERE playback_attempt_id=$1`, f.binding.Fence.AttemptID); err != nil {
				t.Fatal(err)
			}
			assertRecovery(t, f.call(t, "DELETE", "/"+f.binding.Scope.SessionID, stop), 200, f.binding, false)
		})
		t.Run(backend+"/original-stop-missing-receipt", func(t *testing.T) {
			f := newRecoveryHTTPFixture(t, backend, "activated")
			if _, err := f.store.BeginBoundStop(t.Context(), f.binding, playbackTestStop); err != nil {
				t.Fatal(err)
			}
			f.expire(t)
			f.restart(t)
			reconciled, err := f.application.ReconcileInitialPlayback(t.Context(), "", 10)
			if err == nil || reconciled.Pending != 1 {
				t.Fatalf("missing ordinary receipt was synthesized: %+v %v", reconciled, err)
			}
			receipt, err := playback.ReadInitialActivationReceiptV3(t.Context(), f.binding, f.sink)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := receipt.StateFor(f.binding)
			if err != nil || observed.Stop != nil {
				t.Fatal("reconciler invented client payload", err)
			}
			// Ordinary STOP owns the captured source independently of later
			// admission withdrawal; owner-loss must not intercept its receipt.
			if _, err := f.pool.Exec(t.Context(), `UPDATE playback_source_registrations SET admission_state='retiring' WHERE user_id=$1`, f.user); err != nil {
				t.Fatal(err)
			}
			stop := map[string]any{"installation_id": playbackTestInstallation, "stop_id": uuid.NewString()}
			if r := f.call(t, "DELETE", "/"+f.binding.Scope.SessionID, stop); r.Code < 400 {
				t.Fatal("changed stop accepted")
			}
			state, err := f.store.ReadInitialActivation(t.Context(), f.binding)
			if err != nil || state.AbortID != "" || state.StopID != playbackTestStop {
				t.Fatalf("stop replaced: %+v %v", state, err)
			}
			stop["stop_id"] = playbackTestStop
			stop["sequence"] = 4
			stop["position"] = 21
			r := f.call(t, "DELETE", "/"+f.binding.Scope.SessionID, stop)
			if r.Code != 200 || !strings.Contains(r.Body.String(), `"stop_id":"`+playbackTestStop+`"`) || strings.Contains(r.Body.String(), `"recovery"`) {
				t.Fatalf("ordinary stop %d: %s", r.Code, r.Body.String())
			}
		})
	}
}

func TestPlaybackOwnerLossHTTPSourceRefusal(t *testing.T) {
	for _, change := range []string{"withdrawal", "reselection", "wrong-session", "wrong-device"} {
		t.Run(change, func(t *testing.T) {
			f := newRecoveryHTTPFixture(t, "postgres", "activated")
			f.expire(t)
			before, err := f.store.ReadInitialActivation(t.Context(), f.binding)
			if err != nil {
				t.Fatal(err)
			}
			headers := viewerHeaders()
			path := "/start"
			method := "POST"
			body := f.body
			switch change {
			case "withdrawal":
				_, err = f.pool.Exec(t.Context(), `UPDATE playback_source_registrations SET admission_state='retiring' WHERE user_id=$1`, f.user)
			case "reselection":
				_, err = f.pool.Exec(t.Context(), `UPDATE playback_source_registrations SET source_id=$2 WHERE user_id=$1`, f.user, uuid.NewString())
			case "wrong-session":
				path = "/" + uuid.NewString()
				method = "DELETE"
				body = map[string]any{"installation_id": playbackTestInstallation, "stop_id": playbackTestStop}
			case "wrong-device":
				headers = with(headers, "X-Device-ID", "different-device")
			}
			if err != nil {
				t.Fatal(err)
			}
			r := do(t, f.handler, method, Prefix+"/playback"+path, playbackJSON(t, body), headers)
			if r.Code < 400 || strings.Contains(r.Body.String(), `"recovery"`) {
				t.Fatalf("refusal %d: %s", r.Code, r.Body.String())
			}
			state, err := f.store.ReadInitialActivation(t.Context(), f.binding)
			if err != nil || state.AbortID != before.AbortID || state.Phase != before.Phase {
				t.Fatalf("refused request mutated activation: %+v %v", state, err)
			}
			receipt, err := playback.ReadInitialActivationReceiptV3(t.Context(), f.binding, f.sink)
			if err != nil {
				t.Fatal(err)
			}
			source, err := receipt.StateFor(f.binding)
			if err != nil || source.Stop != nil {
				t.Fatal("refused request closed source", err)
			}
		})
	}
}

func TestPlaybackOwnerLossSchema(t *testing.T) {
	r := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	pending := huma.SchemaFromType(r, reflect.TypeFor[PlaybackRecoveryPending]())
	completed := huma.SchemaFromType(r, reflect.TypeFor[PlaybackRecoveryStop]())
	identity := map[string]any{"recovery_id": uuid.NewString(), "playback_attempt_id": "original-attempt", "session_id": uuid.NewString(), "state": "draining", "reason": "owner_lost"}
	validate := func(schema *huma.Schema, value any, valid bool) {
		t.Helper()
		result := new(huma.ValidateResult)
		huma.Validate(r, schema, huma.NewPathBuffer([]byte("body"), 0), huma.ModeReadFromServer, value, result)
		if (len(result.Errors) == 0) != valid {
			t.Fatalf("valid=%v errors=%v", valid, result.Errors)
		}
	}
	value := map[string]any{"outcome": "draining", "recovery": identity}
	validate(pending, value, true)
	for _, field := range []string{"recovery_id", "playback_attempt_id", "session_id", "state", "reason"} {
		saved := identity[field]
		delete(identity, field)
		validate(pending, value, false)
		identity[field] = saved
	}
	identity["accepted"] = map[string]any{"sequence": float64(1), "position": float64(0), "is_paused": false}
	validate(pending, value, false)
	identity["state"] = "aborted"
	value["outcome"] = "aborted"
	validate(completed, value, true)
	identity["accepted"].(map[string]any)["sequence"] = float64(0)
	validate(completed, value, false)
	delete(identity, "accepted")
	validate(completed, value, true)
	value["stop_id"] = playbackTestStop
	validate(completed, value, false)
	doc := generatedDocument(t)
	paths := doc["paths"].(map[string]any)
	for _, path := range []string{"/api/v2/playback/start", "/api/v2/playback/{session_id}"} {
		method, status := "post", "201"
		if strings.HasSuffix(path, "{session_id}") {
			method, status = "delete", "200"
		}
		responses := paths[path].(map[string]any)[method].(map[string]any)["responses"].(map[string]any)
		if responses["202"] == nil || responses[status] == nil {
			t.Fatal("missing recovery status declarations")
		}
		schema := responses[status].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		wantRef := "#/components/schemas/PlaybackDecision"
		if method == "delete" {
			wantRef = "#/components/schemas/PlaybackMutation"
		}
		if schema["$ref"] != wantRef || schema["oneOf"] != nil {
			t.Fatalf("original response reference changed: %v", schema)
		}
		pendingJSON, _ := json.Marshal(responses["202"])
		if strings.Contains(string(pendingJSON), "PlaybackRecoveryStop") || strings.Contains(string(pendingJSON), "PlaybackStopResult") {
			t.Fatal("202 schema permits completed recovery")
		}

	}
}

func TestPlaybackOwnerLossReconcileRetentionExpiry(t *testing.T) {
	for _, phase := range []string{"uninstalled", "pending", "installed", "activated"} {
		t.Run(phase, func(t *testing.T) {
			f := newRecoveryHTTPFixture(t, "postgres", phase)
			if _, err := f.pool.Exec(t.Context(), `UPDATE playback_v3_attempts SET expires_at=clock_timestamp()-interval '1 second' WHERE playback_attempt_id=$1`, f.binding.Fence.AttemptID); err != nil {
				t.Fatal(err)
			}
			result, err := f.application.ReconcileInitialPlayback(t.Context(), "", 10)
			if err != nil || result.Visited != 1 || result.Completed != 1 || result.Pending != 0 {
				t.Fatalf("expiry reconciliation: %+v %v", result, err)
			}
			assertRecovery(t, f.call(t, "POST", "/start", f.body), 201, f.binding, false)
			again, err := f.application.ReconcileInitialPlayback(t.Context(), "", 10)
			if err != nil || again.Visited != 0 {
				t.Fatalf("terminal re-inventoried: %+v %v", again, err)
			}
		})
	}
}
