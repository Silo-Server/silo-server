package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/policy"
)

func TestPolicyCapabilityShape(t *testing.T) {
	handler := NewPolicyHandler(policy.NewSystem(nil, nil, nil), nil, nil, policyEditorEnabled)
	rec := httptest.NewRecorder()
	req := newPolicyHandlerRequest(http.MethodGet, "/policy/capability", nil, nil)

	handler.HandleCapability(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response policyCapabilityResponse
	decodePolicyHandlerResponse(t, rec, &response)
	if !response.Enabled || !response.EditorAvailable {
		t.Fatalf("capability = %#v, want enabled editor", response)
	}
	wantDecisionTypes := []string{policy.DomainAction, policy.DomainPermission, policy.DomainScope}
	if !reflect.DeepEqual(response.DecisionTypes, wantDecisionTypes) {
		t.Fatalf("decision_types = %#v, want %#v", response.DecisionTypes, wantDecisionTypes)
	}
}

func TestPolicyValidateReturnsIssuesWithOK(t *testing.T) {
	handler := NewPolicyHandler(nil, nil, nil, policyEditorEnabled)
	rec := httptest.NewRecorder()
	req := newPolicyHandlerRequest(http.MethodPost, "/admin/policy/validate", map[string]any{
		"domain": policy.DomainScope,
		"source": "package silo_custom.scope\n\nbroken := if {",
	}, nil)

	handler.HandleValidate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response policyValidateResponse
	decodePolicyHandlerResponse(t, rec, &response)
	if response.CompiledOK {
		t.Fatalf("compiled_ok = true, want false")
	}
	if len(response.Errors) == 0 || response.Errors[0].Row == 0 || response.Errors[0].Col == 0 {
		t.Fatalf("errors = %#v, want row/col diagnostics", response.Errors)
	}
}

func TestPolicyValidateRejectsOversizedBody(t *testing.T) {
	handler := NewPolicyHandler(nil, nil, nil, policyEditorEnabled)
	rec := httptest.NewRecorder()
	req := newPolicyHandlerRequest(http.MethodPost, "/admin/policy/validate", map[string]any{
		"domain": policy.DomainScope,
		"source": strings.Repeat("a", maxPolicyRequestBodyBytes+1),
	}, nil)

	handler.HandleValidate(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
}

func TestPolicySimulateTighteningOverride(t *testing.T) {
	handler := NewPolicyHandler(nil, nil, nil, policyEditorEnabled)
	input := policy.ScopeInput{
		SchemaVersion:        1,
		UserID:               7,
		SessionID:            "session-1",
		AccountRestricted:    false,
		AccountMaxQuality:    "",
		AccessPolicyRevision: 12,
		DisabledLibraryIDs:   []int{},
		ProfilePresent:       false,
		ProfileVerified:      true,
		RequestTime:          "2026-07-02T12:00:00Z",
	}
	source := `package silo_custom.scope

import rego.v1

override(base, _) := result if {
	result := {
		"unrestricted": false,
		"allowed_library_ids": [2],
		"disabled_library_ids": [],
		"profile_verified": base.profile_verified,
	}
}`

	rec := httptest.NewRecorder()
	req := newPolicyHandlerRequest(http.MethodPost, "/admin/policy/simulate", map[string]any{
		"domain": policy.DomainScope,
		"source": source,
		"input":  input,
	}, nil)

	handler.HandleSimulate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var result policy.SimulateResult
	decodePolicyHandlerResponse(t, rec, &result)
	var decision policy.ScopeDecision
	if err := json.Unmarshal(result.Decision, &decision); err != nil {
		t.Fatalf("decode decision: %v", err)
	}
	if decision.Unrestricted || len(decision.AllowedLibraryIDs) != 1 || decision.AllowedLibraryIDs[0] != 2 {
		t.Fatalf("decision = %#v, want restricted to library 2", decision)
	}
	if result.EvalTimeNS <= 0 {
		t.Fatalf("eval_time_ns = %d, want positive", result.EvalTimeNS)
	}
}

func TestPolicyEditorDisabledGatesEditorEndpoints(t *testing.T) {
	handler := NewPolicyHandler(policy.NewSystem(nil, nil, nil), nil, nil, func() bool { return false })

	rec := httptest.NewRecorder()
	req := newPolicyHandlerRequest(http.MethodPost, "/admin/policy/validate", map[string]any{
		"domain": policy.DomainScope,
		"source": validHandlerPolicySource(),
	}, nil)
	handler.HandleValidate(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s, want forbidden", rec.Code, rec.Body.String())
	}
}

func TestPolicyDocumentVersionLifecycleDB(t *testing.T) {
	ctx := context.Background()
	pool, store := newPolicyHandlerStoreTest(t, ctx)
	bus := newPolicyHandlerEventBus()
	system := newStartedPolicyHandlerSystem(t, ctx, store, bus)
	defer system.Stop()
	handler := NewPolicyHandler(system, store, policy.NewDecisionRepository(pool), policyEditorEnabled)

	createDocRec := httptest.NewRecorder()
	handler.HandleCreateDocument(createDocRec, newPolicyHandlerRequest(http.MethodPost, "/admin/policy/documents", map[string]any{
		"domain": policy.DomainScope,
		"name":   "household scope",
	}, nil))
	if createDocRec.Code != http.StatusCreated {
		t.Fatalf("create document status = %d, body = %s", createDocRec.Code, createDocRec.Body.String())
	}
	var document policyDocumentResponse
	decodePolicyHandlerResponse(t, createDocRec, &document)

	createVersionRec := httptest.NewRecorder()
	handler.HandleCreateVersion(createVersionRec, newPolicyHandlerRequest(
		http.MethodPost,
		"/admin/policy/documents/"+strconv.FormatInt(document.ID, 10)+"/versions",
		map[string]any{
			"source":  validHandlerPolicySource(),
			"comment": "initial",
		},
		map[string]string{"id": strconv.FormatInt(document.ID, 10)},
	))
	if createVersionRec.Code != http.StatusCreated {
		t.Fatalf("create version status = %d, body = %s", createVersionRec.Code, createVersionRec.Body.String())
	}
	var created policyCreateVersionResponse
	decodePolicyHandlerResponse(t, createVersionRec, &created)
	if !created.CompiledOK || created.VersionNumber != 1 {
		t.Fatalf("created version = %#v, want compiled version 1", created)
	}

	listVersionsRec := httptest.NewRecorder()
	handler.HandleListVersions(listVersionsRec, newPolicyHandlerRequest(
		http.MethodGet,
		"/admin/policy/documents/"+strconv.FormatInt(document.ID, 10)+"/versions",
		nil,
		map[string]string{"id": strconv.FormatInt(document.ID, 10)},
	))
	if listVersionsRec.Code != http.StatusOK {
		t.Fatalf("list versions status = %d, body = %s", listVersionsRec.Code, listVersionsRec.Body.String())
	}
	var versions []policyVersionResponse
	decodePolicyHandlerResponse(t, listVersionsRec, &versions)
	if len(versions) != 1 || versions[0].Source != "" {
		t.Fatalf("versions = %#v, want metadata without source", versions)
	}

	activateRec := httptest.NewRecorder()
	handler.HandleActivateVersion(activateRec, newPolicyHandlerRequest(
		http.MethodPost,
		"/admin/policy/documents/"+strconv.FormatInt(document.ID, 10)+"/versions/"+strconv.FormatInt(created.ID, 10)+"/activate",
		nil,
		map[string]string{
			"id":      strconv.FormatInt(document.ID, 10),
			"version": strconv.FormatInt(created.ID, 10),
		},
	))
	if activateRec.Code != http.StatusOK {
		t.Fatalf("activate status = %d, body = %s", activateRec.Code, activateRec.Body.String())
	}
	var activated policyActivateVersionResponse
	decodePolicyHandlerResponse(t, activateRec, &activated)
	if activated.Generation == 0 || system.Generation() != activated.Generation {
		t.Fatalf("activated = %#v system generation = %d", activated, system.Generation())
	}
	if bus.PublishCount() == 0 {
		t.Fatal("NotifyChanged did not publish a policy change event")
	}
}

func TestPolicyVersionCompileFailurePersistsAndCannotActivateDB(t *testing.T) {
	ctx := context.Background()
	_, store := newPolicyHandlerStoreTest(t, ctx)
	system := newStartedPolicyHandlerSystem(t, ctx, store, newPolicyHandlerEventBus())
	defer system.Stop()
	handler := NewPolicyHandler(system, store, nil, policyEditorEnabled)
	document, err := store.CreateDocument(ctx, policy.DomainScope, "bad scope")
	if err != nil {
		t.Fatalf("CreateDocument() error: %v", err)
	}

	createRec := httptest.NewRecorder()
	handler.HandleCreateVersion(createRec, newPolicyHandlerRequest(
		http.MethodPost,
		"/admin/policy/documents/"+strconv.FormatInt(document.ID, 10)+"/versions",
		map[string]any{"source": "package silo_custom.scope\n\nbroken := if {"},
		map[string]string{"id": strconv.FormatInt(document.ID, 10)},
	))
	if createRec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create bad version status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var errorsResponse policyCompileErrorsResponse
	decodePolicyHandlerResponse(t, createRec, &errorsResponse)
	if len(errorsResponse.Errors) == 0 || errorsResponse.Errors[0].Row == 0 || errorsResponse.Errors[0].Col == 0 {
		t.Fatalf("errors = %#v, want row/col", errorsResponse.Errors)
	}
	versions, err := store.ListVersions(ctx, document.ID)
	if err != nil {
		t.Fatalf("ListVersions() error: %v", err)
	}
	if len(versions) != 1 || versions[0].CompiledOK || versions[0].CompileError == nil {
		t.Fatalf("versions = %#v, want persisted failed version", versions)
	}

	activateRec := httptest.NewRecorder()
	handler.HandleActivateVersion(activateRec, newPolicyHandlerRequest(
		http.MethodPost,
		"/admin/policy/documents/"+strconv.FormatInt(document.ID, 10)+"/versions/"+strconv.FormatInt(versions[0].ID, 10)+"/activate",
		nil,
		map[string]string{
			"id":      strconv.FormatInt(document.ID, 10),
			"version": strconv.FormatInt(versions[0].ID, 10),
		},
	))
	if activateRec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("activate failed version status = %d, body = %s", activateRec.Code, activateRec.Body.String())
	}
}

func TestPolicyEnabledToggleConflictDB(t *testing.T) {
	ctx := context.Background()
	_, store := newPolicyHandlerStoreTest(t, ctx)
	system := newStartedPolicyHandlerSystem(t, ctx, store, newPolicyHandlerEventBus())
	defer system.Stop()
	handler := NewPolicyHandler(system, store, nil, policyEditorEnabled)

	first, err := store.CreateDocument(ctx, policy.DomainScope, "first")
	if err != nil {
		t.Fatalf("CreateDocument(first) error: %v", err)
	}
	if _, err := store.SetEnabled(ctx, first.ID, false); err != nil {
		t.Fatalf("SetEnabled(first,false) error: %v", err)
	}
	if _, err := store.CreateDocument(ctx, policy.DomainScope, "second"); err != nil {
		t.Fatalf("CreateDocument(second) error: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.HandleSetDocumentEnabled(rec, newPolicyHandlerRequest(
		http.MethodPost,
		"/admin/policy/documents/"+strconv.FormatInt(first.ID, 10)+"/enabled",
		map[string]any{"enabled": true},
		map[string]string{"id": strconv.FormatInt(first.ID, 10)},
	))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPolicyDecisionsListAndGetDB(t *testing.T) {
	ctx := context.Background()
	pool, _ := newPolicyHandlerStoreTest(t, ctx)
	repo := policy.NewDecisionRepository(pool)
	handler := NewPolicyHandler(nil, nil, repo)

	old := insertPolicyHandlerDecision(t, ctx, pool, policy.Entry{
		Timestamp:        time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond),
		DecisionName:     policy.DecisionScope,
		PolicyGeneration: 1,
		UserID:           policyTestIntPtr(7),
		EvalTimeNS:       50,
		InputDigest:      "old",
	})
	newest := insertPolicyHandlerDecision(t, ctx, pool, policy.Entry{
		Timestamp:        time.Now().UTC().Truncate(time.Microsecond),
		DecisionName:     policy.DecisionScope,
		PolicyGeneration: 2,
		UserID:           policyTestIntPtr(7),
		EvalTimeNS:       75,
		InputDigest:      "new",
		InputSample:      json.RawMessage(`{"user_id":7}`),
		ResultSample:     json.RawMessage(`{"allowed":true}`),
	})

	listRec := httptest.NewRecorder()
	handler.HandleListDecisions(listRec, newPolicyHandlerRequest(http.MethodGet, "/admin/policy/decisions?limit=1", nil, nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var list policyDecisionListResponse
	decodePolicyHandlerResponse(t, listRec, &list)
	if len(list.Entries) != 1 || list.Entries[0].ID != newest.ID || list.NextCursor == "" {
		t.Fatalf("list = %#v, want newest with next cursor", list)
	}
	if len(list.Entries[0].InputSample) != 0 {
		t.Fatalf("list entry unexpectedly included input sample: %#v", list.Entries[0])
	}

	getRec := httptest.NewRecorder()
	handler.HandleGetDecision(getRec, newPolicyHandlerRequest(
		http.MethodGet,
		"/admin/policy/decisions/"+strconv.FormatInt(newest.ID, 10),
		nil,
		map[string]string{"id": strconv.FormatInt(newest.ID, 10)},
	))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRec.Code, getRec.Body.String())
	}
	var single policyDecisionResponse
	decodePolicyHandlerResponse(t, getRec, &single)
	if single.ID != newest.ID || len(single.InputSample) == 0 || old.ID == newest.ID {
		t.Fatalf("single = %#v, want newest with samples", single)
	}
}

func newPolicyHandlerRequest(method, target string, payload any, params map[string]string) *http.Request {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		body = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, target, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	routeCtx := chi.NewRouteContext()
	for key, value := range params {
		routeCtx.URLParams.Add(key, value)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{
		UserID:    42,
		Role:      "admin",
		SessionID: "session-42",
		TokenType: auth.TokenTypeAccess,
	})
	return req.WithContext(ctx)
}

func decodePolicyHandlerResponse(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}

func policyEditorEnabled() bool {
	return true
}

func newPolicyHandlerStoreTest(t *testing.T, ctx context.Context) (*pgxpool.Pool, *policy.PolicyStore) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.policy_documents')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check policy_documents table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied policy foundation migration")
	}

	resetPolicyHandlerTables(t, ctx, pool)
	return pool, policy.NewPolicyStore(pool)
}

func resetPolicyHandlerTables(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `TRUNCATE TABLE public.policy_decisions RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate policy_decisions: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE TABLE public.policy_documents, public.policy_document_versions RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate policy documents: %v", err)
	}
	// policy_document_versions.created_by_user_id references users(id); the
	// claims injected by newPolicyHandlerRequest use UserID 42, so that row
	// must exist.
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.users (id, username, role, enabled)
		VALUES (42, 'policy-handler-test-admin', 'admin', true)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed policy test user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.policy_generation (id, generation)
		VALUES (true, 1)
		ON CONFLICT (id)
		DO UPDATE SET generation = 1, updated_at = now()`); err != nil {
		t.Fatalf("reset policy generation: %v", err)
	}
}

func newStartedPolicyHandlerSystem(t *testing.T, ctx context.Context, store *policy.PolicyStore, bus cache.EventBus) *policy.System {
	t.Helper()
	system := policy.NewSystem(
		store,
		bus,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		policy.WithSystemPollInterval(time.Hour),
	)
	if err := system.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	return system
}

type policyHandlerEventBus struct {
	mu        sync.Mutex
	published int
	handlers  map[string][]cache.EventHandler
}

func newPolicyHandlerEventBus() *policyHandlerEventBus {
	return &policyHandlerEventBus{handlers: make(map[string][]cache.EventHandler)}
}

func (b *policyHandlerEventBus) Publish(_ context.Context, channel string, event cache.Event) error {
	b.mu.Lock()
	b.published++
	handlers := append([]cache.EventHandler(nil), b.handlers[channel]...)
	b.mu.Unlock()
	for _, handler := range handlers {
		handler(event)
	}
	return nil
}

func (b *policyHandlerEventBus) Subscribe(_ context.Context, channel string, handler cache.EventHandler) error {
	b.mu.Lock()
	b.handlers[channel] = append(b.handlers[channel], handler)
	b.mu.Unlock()
	return nil
}

func (b *policyHandlerEventBus) Close() error { return nil }

func (b *policyHandlerEventBus) PublishCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.published
}

func insertPolicyHandlerDecision(t *testing.T, ctx context.Context, pool *pgxpool.Pool, entry policy.Entry) policy.Entry {
	t.Helper()
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.InputDigest == "" {
		entry.InputDigest = "digest"
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO policy_decisions (
			"timestamp", decision_name, policy_generation, user_id, profile_id,
			session_id, request_id, node_id, allowed, eval_time_ns, input_digest,
			input_sample, result_sample, error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb, $14)
		RETURNING id, "timestamp"
	`,
		entry.Timestamp,
		string(entry.DecisionName),
		entry.PolicyGeneration,
		entry.UserID,
		nullablePolicyHandlerString(entry.ProfileID),
		nullablePolicyHandlerString(entry.SessionID),
		nullablePolicyHandlerString(entry.RequestID),
		nullablePolicyHandlerString(entry.NodeID),
		entry.Allowed,
		entry.EvalTimeNS,
		entry.InputDigest,
		nullablePolicyHandlerJSON(entry.InputSample),
		nullablePolicyHandlerJSON(entry.ResultSample),
		nullablePolicyHandlerString(entry.Error),
	).Scan(&entry.ID, &entry.Timestamp); err != nil {
		t.Fatalf("insert policy decision row: %v", err)
	}
	return entry
}

func nullablePolicyHandlerString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullablePolicyHandlerJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func policyTestIntPtr(value int) *int {
	return &value
}

func validHandlerPolicySource() string {
	return `package silo_custom.scope

import rego.v1

override(base, _) := base`
}

type erroringPolicyEventBus struct{}

func (erroringPolicyEventBus) Publish(context.Context, string, cache.Event) error {
	return errBusDown
}

func (erroringPolicyEventBus) Subscribe(context.Context, string, cache.EventHandler) error {
	return nil
}

func (erroringPolicyEventBus) Close() error { return nil }

var errBusDown = errors.New("event bus down")

func createCompiledPolicyVersion(t *testing.T, handler *PolicyHandler, documentID int64) int64 {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.HandleCreateVersion(rec, newPolicyHandlerRequest(
		http.MethodPost,
		"/admin/policy/documents/"+strconv.FormatInt(documentID, 10)+"/versions",
		map[string]any{"source": validHandlerPolicySource()},
		map[string]string{"id": strconv.FormatInt(documentID, 10)},
	))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create version status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created policyCreateVersionResponse
	decodePolicyHandlerResponse(t, rec, &created)
	return created.ID
}

func TestPolicyActivateReportsLocalReloadFailureDB(t *testing.T) {
	ctx := context.Background()
	_, store := newPolicyHandlerStoreTest(t, ctx)

	// The system reloads from an unreachable store, so persistence (through
	// the good store) succeeds while the live apply fails.
	badPool, err := pgxpool.New(ctx, "postgres://silo:silo@127.0.0.1:1/silo?connect_timeout=1")
	if err != nil {
		t.Fatalf("create unreachable pool: %v", err)
	}
	t.Cleanup(badPool.Close)
	system := policy.NewSystem(
		policy.NewPolicyStore(badPool),
		&cache.NoopEventBus{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		policy.WithSystemPollInterval(time.Hour),
	)
	if err := system.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer system.Stop()

	handler := NewPolicyHandler(system, store, nil, policyEditorEnabled)
	document, err := store.CreateDocument(ctx, policy.DomainScope, "apply failure scope")
	if err != nil {
		t.Fatalf("CreateDocument() error: %v", err)
	}
	versionID := createCompiledPolicyVersion(t, handler, document.ID)

	rec := httptest.NewRecorder()
	handler.HandleActivateVersion(rec, newPolicyHandlerRequest(
		http.MethodPost,
		"/admin/policy/documents/"+strconv.FormatInt(document.ID, 10)+"/versions/"+strconv.FormatInt(versionID, 10)+"/activate",
		nil,
		map[string]string{
			"id":      strconv.FormatInt(document.ID, 10),
			"version": strconv.FormatInt(versionID, 10),
		},
	))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("activate status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}
	var response policyActivateVersionResponse
	decodePolicyHandlerResponse(t, rec, &response)
	if response.Applied || response.FailedStep != "local_reload" {
		t.Fatalf("apply status = %#v, want applied=false failed_step=local_reload", response.policyApplyStatus)
	}
	if response.Generation == 0 || response.LoadedGeneration == response.Generation {
		t.Fatalf("loaded generation %d should lag stored generation %d", response.LoadedGeneration, response.Generation)
	}
}

func TestPolicySetEnabledReportsPublishFailureDB(t *testing.T) {
	ctx := context.Background()
	_, store := newPolicyHandlerStoreTest(t, ctx)
	system := newStartedPolicyHandlerSystem(t, ctx, store, erroringPolicyEventBus{})
	defer system.Stop()

	handler := NewPolicyHandler(system, store, nil, policyEditorEnabled)
	document, err := store.CreateDocument(ctx, policy.DomainScope, "publish failure scope")
	if err != nil {
		t.Fatalf("CreateDocument() error: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.HandleSetDocumentEnabled(rec, newPolicyHandlerRequest(
		http.MethodPost,
		"/admin/policy/documents/"+strconv.FormatInt(document.ID, 10)+"/enabled",
		map[string]any{"enabled": true},
		map[string]string{"id": strconv.FormatInt(document.ID, 10)},
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("set enabled status = %d, want 200 (local apply succeeded), body = %s", rec.Code, rec.Body.String())
	}
	var response policySetEnabledResponse
	decodePolicyHandlerResponse(t, rec, &response)
	if !response.Applied || response.FailedStep != "event_publish" {
		t.Fatalf("apply status = %#v, want applied=true failed_step=event_publish", response.policyApplyStatus)
	}
	if response.LoadedGeneration != response.Generation {
		t.Fatalf("loaded generation %d != stored generation %d after successful local reload", response.LoadedGeneration, response.Generation)
	}
}
