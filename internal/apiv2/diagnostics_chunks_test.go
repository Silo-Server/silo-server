package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

func diagnosticsChunksFixture(t *testing.T) (http.Handler, *handlers.DiagnosticsHandler, *ingressFixture) {
	t.Helper()
	f := availableIngress()
	service := handlers.NewDiagnosticsHandler(f)
	deps := parityDeps(false)
	deps.DiagnosticsIngress = service
	deps.DiagnosticsChunks = service
	return NewHandler(deps), service, f
}
func createDiagnosticUpload(t *testing.T, h http.Handler, size int) DiagnosticsChunkInitBody {
	t.Helper()
	r := do(t, h, http.MethodPost, Prefix+"/diagnostics/reports/uploads", fmt.Sprintf(`{"manifest":{"schema_version":1},"bundle_bytes":%d}`, size), bearer(memberToken))
	if r.Code != 201 {
		t.Fatalf("init %d %s", r.Code, r.Body.String())
	}
	var result DiagnosticsChunkInitBody
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.UploadID == "" || !strings.HasSuffix(result.ExpiresAt, ".000Z") {
		t.Fatal(result)
	}
	return result
}
func diagnosticChunkRequest(h http.Handler, id string, body io.Reader, token, media string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, Prefix+"/diagnostics/reports/uploads/"+id+"/chunks/0", body)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", media)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}
func TestDiagnosticsChunksRoundTripAndUncertainCompletion(t *testing.T) {
	h, service, f := diagnosticsChunksFixture(t)
	calls := 0
	f.ingest = func(ctx context.Context, user int, profile *string, manifest []byte, bundle io.Reader) (diagnostics.IngestResult, error) {
		calls++
		data, err := io.ReadAll(bundle)
		if err != nil {
			return diagnostics.IngestResult{}, err
		}
		if string(data) != "abc" || user != 1 || profile == nil || *profile != "captured-profile" {
			t.Errorf("authority/bytes: %d %v %q", user, profile, data)
		}
		return diagnostics.IngestResult{ReportID: "synthetic-report", ShortID: "SILO-TEST"}, nil
	}
	cap := do(t, h, "GET", Prefix+"/diagnostics/capabilities", "", bearer(memberToken))
	var available DiagnosticsCapabilities
	if err := json.Unmarshal(cap.Body.Bytes(), &available); err != nil {
		t.Fatal(err)
	}
	if available.UploadChunkBytes != diagnostics.UploadChunkBytes {
		t.Fatal(available)
	}
	init := createDiagnosticUpload(t, h, 3)
	t.Cleanup(func() { service.AbortDiagnosticChunks(1, init.UploadID) })
	first := diagnosticChunkRequest(h, init.UploadID, strings.NewReader("abc"), memberToken, mediaTypeBinary)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	// Accepted chunks are immutable; same-index replay cannot replace bytes.
	replay := diagnosticChunkRequest(h, init.UploadID, strings.NewReader("xyz"), memberToken, mediaTypeBinary)
	if replay.Code != 200 {
		t.Fatal(replay.Code, replay.Body.String())
	}
	path := Prefix + "/diagnostics/reports/uploads/" + init.UploadID + "/complete"
	headers := bearer(memberToken)
	headers["X-Profile-Id"] = "captured-profile"
	completed := do(t, h, "POST", path, "", headers)
	if completed.Code != 201 || calls != 1 {
		t.Fatal(completed.Code, completed.Body.String(), calls)
	}
	requireProblem(t, do(t, h, "POST", path, "", headers), TypeNotFound)
	if calls != 1 {
		t.Fatal("spent completion replayed ingest")
	}
}
func TestDiagnosticsChunksOwnershipMediaAndAbort(t *testing.T) {
	h, service, _ := diagnosticsChunksFixture(t)
	init := createDiagnosticUpload(t, h, 3)
	t.Cleanup(func() { service.AbortDiagnosticChunks(1, init.UploadID) })
	for _, tc := range []struct {
		id, token, media string
		want             ProblemType
	}{
		{init.UploadID, apiKeyToken, mediaTypeBinary, TypePermissionDenied},
		{"absent", memberToken, mediaTypeBinary, TypeNotFound},
		{init.UploadID, adminToken, mediaTypeBinary, TypeNotFound},
		{init.UploadID, memberToken, "application/json", TypeUnsupportedMediaType},
	} {
		unread := &ingressUnreadBody{}
		requireProblem(t, diagnosticChunkRequest(h, tc.id, unread, tc.token, tc.media), tc.want)
		if unread.read {
			t.Fatal("rejected chunk body read")
		}
	}
	// An unknown/foreign abort must neither disclose nor affect another account.
	service.AbortDiagnosticChunks(2, init.UploadID)
	ok := diagnosticChunkRequest(h, init.UploadID, strings.NewReader("abc"), memberToken, mediaTypeBinary)
	if ok.Code != 200 {
		t.Fatal(ok.Code, ok.Body.String())
	}
	path := Prefix + "/diagnostics/reports/uploads/" + init.UploadID
	for range 2 {
		if r := do(t, h, "DELETE", path, "", bearer(memberToken)); r.Code != 204 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	requireProblem(t, diagnosticChunkRequest(h, init.UploadID, &ingressUnreadBody{}, memberToken, mediaTypeBinary), TypeNotFound)
	requireProblem(t, diagnosticChunkRequest(h, "absent", bytes.NewReader(make([]byte, diagnostics.UploadChunkBytes+1)), memberToken, mediaTypeBinary), TypePayloadTooLarge)
}
func TestDiagnosticsChunksReinitAndExplicitRefusal(t *testing.T) {
	h, service, f := diagnosticsChunksFixture(t)
	old := createDiagnosticUpload(t, h, 3)
	current := createDiagnosticUpload(t, h, 3)
	t.Cleanup(func() { service.AbortDiagnosticChunks(1, current.UploadID) })
	requireProblem(t, diagnosticChunkRequest(h, old.UploadID, &ingressUnreadBody{}, memberToken, mediaTypeBinary), TypeNotFound)
	complete := Prefix + "/diagnostics/reports/uploads/" + current.UploadID + "/complete"
	requireProblem(t, do(t, h, "POST", complete, "", bearer(memberToken)), TypeConflict)
	if rec := diagnosticChunkRequest(h, current.UploadID, strings.NewReader("abc"), memberToken, mediaTypeBinary); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.status.Status = diagnostics.StatusDisabled
	requireProblem(t, do(t, h, "POST", complete, "", bearer(memberToken)), TypeCapabilityDisabled)
	f.status.Status = diagnostics.StatusAvailable
	requireProblem(t, do(t, h, "POST", complete, "", bearer(memberToken)), TypeNotFound)
}
func TestDiagnosticsChunksExpiryUsesAbsenceProblem(t *testing.T) {
	err := diagnosticsChunkProblem(&handlers.DiagnosticsUploadFailure{Status: 410, Message: "expired"})
	problem, ok := errors.AsType[*Problem](err)
	if !ok || problem.Type != TypeNotFound.URI() || problem.Status != 404 {
		t.Fatal(err)
	}
}

func TestDiagnosticsChunksReplicaAndStreamingLimit(t *testing.T) {
	// Construct both local managers before creating a session. This models
	// separate ownership maps; test constructors share a temporary spool root.
	h, service, _ := diagnosticsChunksFixture(t)
	replica, _, _ := diagnosticsChunksFixture(t)
	init := createDiagnosticUpload(t, h, int(diagnostics.UploadChunkBytes))
	t.Cleanup(func() { service.AbortDiagnosticChunks(1, init.UploadID) })
	unread := &ingressUnreadBody{}
	requireProblem(t, diagnosticChunkRequest(replica, init.UploadID, unread, memberToken, mediaTypeBinary), TypeNotFound)
	if unread.read {
		t.Fatal("another replica read unknown session bytes")
	}
	// io.LimitReader makes ContentLength unknown. The streaming cap must still
	// reject the extra byte rather than trusting a missing length header.
	body := io.LimitReader(bytes.NewReader(make([]byte, diagnostics.UploadChunkBytes+1)), diagnostics.UploadChunkBytes+1)
	requireProblem(t, diagnosticChunkRequest(h, init.UploadID, body, memberToken, mediaTypeBinary), TypePayloadTooLarge)
	request := httptest.NewRequest(http.MethodPut, Prefix+"/diagnostics/reports/uploads/"+init.UploadID+"/chunks/0", &ingressUnreadBody{})
	request.Header.Set("Authorization", "Bearer "+memberToken)
	request.Header.Set("Content-Type", mediaTypeBinary)
	request.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request)
	requireProblem(t, rec, TypeUnsupportedMediaType)
}
