package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/storagetransition"
)

type fakeAdminStorageTransition struct {
	health   storagetransition.SourceHealth
	probes   []bool
	startErr error
}

func (f *fakeAdminStorageTransition) Start(context.Context, int, storagetransition.StartRequest) (*models.AdminJob, storagetransition.Preflight, error) {
	return nil, storagetransition.Preflight{}, f.startErr
}

func TestAdminStorageTransitionClassifiesStartErrors(t *testing.T) {
	path := Prefix + "/admin/storage-transitions"
	body := `{"policy":"start_fresh","values":{}}`
	service := &fakeAdminStorageTransition{startErr: storagetransition.NewValidationError(errors.New("invalid target"))}
	deps := requestDeps(fixtureRequests())
	deps.AdminStorageTransition = service
	handler := NewHandler(deps)
	requireProblem(t, do(t, handler, http.MethodPost, path, body, actingRequestAdmin), TypeValidationFailed)

	service.startErr = errors.New("database unavailable")
	requireProblem(t, do(t, handler, http.MethodPost, path, body, actingRequestAdmin), TypeInternalError)
}

func (f *fakeAdminStorageTransition) SourceHealth(_ context.Context, probe bool) (storagetransition.SourceHealth, error) {
	f.probes = append(f.probes, probe)
	return f.health, nil
}

func TestAdminStorageTransitionCapabilities(t *testing.T) {
	path := Prefix + "/admin/storage-transitions/capabilities"
	withoutService := NewHandler(requestDeps(fixtureRequests()))
	requireProblem(t, do(t, withoutService, http.MethodGet, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, withoutService, http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)

	response := do(t, withoutService, http.MethodGet, path, "", actingRequestAdmin)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var unavailable AdminStorageTransitionCapabilities
	if err := json.Unmarshal(response.Body.Bytes(), &unavailable); err != nil {
		t.Fatal(err)
	}
	if unavailable.State != StateNotConfigured || unavailable.Allowed == nil || *unavailable.Allowed {
		t.Fatalf("unconfigured capability = %#v", unavailable)
	}

	deps := requestDeps(fixtureRequests())
	deps.AdminStorageTransition = &fakeAdminStorageTransition{}
	response = do(t, NewHandler(deps), http.MethodGet, path, "", actingRequestAdmin)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if response.Header().Get("ETag") == "" || response.Header().Get("Cache-Control") != cachePrivateNoCache {
		t.Fatalf("capability cache headers = %#v", response.Header())
	}
	var capability AdminStorageTransitionCapabilities
	if err := json.Unmarshal(response.Body.Bytes(), &capability); err != nil {
		t.Fatal(err)
	}
	if capability.State != StateAvailable || capability.Allowed == nil || !*capability.Allowed ||
		!capability.StartFresh || !capability.PreserveUploads || !capability.MigrateAll ||
		!capability.LocalTarget || !capability.S3Target || !capability.SourceHealth ||
		!capability.JobCancellation || !capability.ResumableRecovery {
		t.Fatalf("available capability = %#v", capability)
	}
}

func TestAdminStorageTransitionHealthProjectsRecoveryState(t *testing.T) {
	service := &fakeAdminStorageTransition{health: storagetransition.SourceHealth{
		CurrentBackend: "s3", Reachable: true, PublicConfigured: true, PublicReachable: true,
		PrivateReachable: true, RecoveryPending: true, RecoveryState: "waiting_retry",
		RecoveryError: "https://private.invalid/bucket?secret=token /srv/private/artwork", RecoveryProgress: 37, RecoveryMessage: "private object key",
	}}
	deps := requestDeps(fixtureRequests())
	deps.AdminStorageTransition = service
	handler := NewHandler(deps)
	response := do(t, handler, http.MethodGet, Prefix+"/admin/storage-transitions/source-health", "", actingRequestAdmin)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if len(service.probes) != 1 || !service.probes[0] {
		t.Fatalf("default health request probes = %#v, want [true]", service.probes)
	}
	var body AdminStorageTransitionSourceHealth
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.RecoveryPending || body.RecoveryState != "waiting_retry" || body.RecoveryFailureCategory != "retryable" || body.RecoveryError == "" || body.RecoveryProgress != 37 || body.RecoveryMessage != "Storage recovery is waiting to retry." {
		t.Fatalf("recovery health = %#v", body)
	}
	if strings.Contains(response.Body.String(), "private.invalid") || strings.Contains(response.Body.String(), "/srv/private") || strings.Contains(response.Body.String(), "private object key") {
		t.Fatalf("private recovery details leaked: %s", response.Body)
	}
	service.health = storagetransition.SourceHealth{CurrentBackend: "s3", Reachable: true, PublicConfigured: true, PublicReachable: true, PrivateReachable: true}
	response = do(t, handler, http.MethodGet, Prefix+"/admin/storage-transitions/source-health", "", actingRequestAdmin)
	if response.Code != http.StatusOK || response.Body.String() == "" {
		t.Fatal(response.Code, response.Body.String())
	}
	body = AdminStorageTransitionSourceHealth{}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RecoveryPending {
		t.Fatalf("cleared recovery still pending: %#v", body)
	}
}

func TestAdminStorageTransitionHealthCanSkipReachabilityProbe(t *testing.T) {
	service := &fakeAdminStorageTransition{health: storagetransition.SourceHealth{
		CurrentBackend: "s3", PublicConfigured: true, RecoveryPending: true,
		RecoveryState: "waiting_retry", RecoveryError: "temporary outage",
	}}
	deps := requestDeps(fixtureRequests())
	deps.AdminStorageTransition = service
	response := do(t, NewHandler(deps), http.MethodGet, Prefix+"/admin/storage-transitions/source-health?probe=false", "", actingRequestAdmin)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if len(service.probes) != 1 || service.probes[0] {
		t.Fatalf("non-probing request probes = %#v, want [false]", service.probes)
	}
	var body AdminStorageTransitionSourceHealth
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.RecoveryPending || body.RecoveryState != "waiting_retry" || body.RecoveryError == "" {
		t.Fatalf("non-probing recovery health = %#v", body)
	}
}
