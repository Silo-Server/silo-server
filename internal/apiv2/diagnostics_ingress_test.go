package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

type ingressFixture struct {
	status diagnostics.Status
	ingest func(context.Context, int, *string, []byte, io.Reader) (diagnostics.IngestResult, error)
}

func (f *ingressFixture) Status(context.Context, int) (diagnostics.Status, error) {
	return f.status, nil
}
func (f *ingressFixture) Ingest(ctx context.Context, user int, profile *string, manifest []byte, bundle io.Reader) (diagnostics.IngestResult, error) {
	if f.ingest != nil {
		return f.ingest(ctx, user, profile, manifest, bundle)
	}
	if _, err := io.Copy(io.Discard, bundle); err != nil {
		return diagnostics.IngestResult{}, err
	}
	return diagnostics.IngestResult{ReportID: "synthetic-report", ShortID: "SILO-TEST"}, nil
}
func ingressHandler(f *ingressFixture) http.Handler {
	deps := parityDeps(false)
	deps.DiagnosticsIngress = handlers.NewDiagnosticsHandler(f)
	return NewHandler(deps)
}
func availableIngress() *ingressFixture {
	return &ingressFixture{status: diagnostics.Status{Status: diagnostics.StatusAvailable, ServerInstanceID: "synthetic-instance", AcceptedSchemaVersions: []int{1}, MaxBundleBytes: diagnostics.DefaultMaxBundleBytes, MaxManifestBytes: diagnostics.MaxManifestBytes, UploadChunkBytes: diagnostics.UploadChunkBytes}}
}
func ingressPart(w *multipart.Writer, name, contentType string) (io.Writer, error) {
	return w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"; filename="%s"`, name, name)}, "Content-Type": {contentType}})
}
func ingressBody(t *testing.T, reverse bool, size int) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	names := []string{"manifest", "bundle"}
	if reverse {
		names = []string{"bundle", "manifest"}
	}
	for _, name := range names {
		content, body := "application/json", []byte(`{"schema_version":1}`)
		if name == "bundle" {
			content = diagnostics.BundleContentType
			body = bytes.Repeat([]byte("x"), size)
		}
		part, err := ingressPart(w, name, content)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}
func ingressRequest(body io.Reader, ct, token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, Prefix+"/diagnostics/reports", body)
	r.Header.Set("Content-Type", ct)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}
func TestDiagnosticsIngressCapabilitiesAndAccess(t *testing.T) {
	f := availableIngress()
	h := ingressHandler(f)
	path := Prefix + "/diagnostics/capabilities"
	for _, tc := range []struct {
		status diagnostics.AvailabilityStatus
		state  string
	}{{diagnostics.StatusAvailable, StateAvailable}, {diagnostics.StatusDisabled, StateDisabled}, {diagnostics.StatusStorageUnavailable, StateNotConfigured}} {
		f.status.Status = tc.status
		rec := do(t, h, "GET", path, "", bearer(memberToken))
		var body DiagnosticsCapabilities
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || body.State != tc.state || body.Revision == "" || body.UploadChunkBytes != 0 || body.MaxBundleBytes != f.status.MaxBundleBytes {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	requireProblem(t, do(t, h, "GET", path, "", bearer(apiKeyToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	missing := do(t, NewHandler(parityDeps(false)), "GET", path, "", bearer(memberToken))
	if missing.Code != 200 || !strings.Contains(missing.Body.String(), `"state":"not_configured"`) {
		t.Fatal(missing.Code, missing.Body.String())
	}
}

func TestDiagnosticsIngressStreamsBeforeRequestCompletes(t *testing.T) {
	f := availableIngress()
	entered := make(chan struct{})
	f.ingest = func(ctx context.Context, user int, profile *string, manifest []byte, bundle io.Reader) (diagnostics.IngestResult, error) {
		if user != 1 || profile == nil || *profile != "captured-profile" || string(manifest) != `{"schema_version":1}` || apimw.GetClaims(ctx) == nil {
			return diagnostics.IngestResult{}, errors.New("lost captured authority or manifest")
		}
		close(entered)
		data, err := io.ReadAll(bundle)
		if err != nil {
			return diagnostics.IngestResult{}, err
		}
		if string(data) != "streamed bundle" {
			return diagnostics.IngestResult{}, errors.New("bundle changed")
		}
		return diagnostics.IngestResult{ReportID: "synthetic-report", ShortID: "SILO-TEST"}, nil
	}
	h := ingressHandler(f)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	form := multipart.NewWriter(writer)
	request := ingressRequest(reader, form.FormDataContentType(), memberToken)
	request.Header.Set("X-Profile-Id", "captured-profile")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { rec := httptest.NewRecorder(); h.ServeHTTP(rec, request); done <- rec }()
	produced := make(chan error, 1)
	go func() {
		part, err := ingressPart(form, "manifest", "application/json")
		if err == nil {
			_, err = io.WriteString(part, `{"schema_version":1}`)
		}
		if err == nil {
			part, err = ingressPart(form, "bundle", diagnostics.BundleContentType)
		}
		if err == nil {
			select {
			case <-entered:
			case <-t.Context().Done():
				err = t.Context().Err()
			}
		}
		if err == nil {
			_, err = io.WriteString(part, "streamed bundle")
		}
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
		produced <- err
	}()
	select {
	case rec := <-done:
		if rec.Code != 201 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	case <-time.After(5 * time.Second):
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal("ingest did not start before the complete request body; possible multipart buffering")
	}
	if err := <-produced; err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticsIngressRejectsBeforeReadingAndPreservesFailures(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		status      diagnostics.AvailabilityStatus
		want        ProblemType
	}{
		{"unsigned", "", diagnostics.StatusAvailable, TypeAuthenticationRequired},
		{"api_key", apiKeyToken, diagnostics.StatusAvailable, TypePermissionDenied},
		{"disabled", memberToken, diagnostics.StatusDisabled, TypeCapabilityDisabled},
		{"storage", memberToken, diagnostics.StatusStorageUnavailable, TypeCapabilityNotConfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := availableIngress()
			f.status.Status = tc.status
			body := &ingressUnreadBody{}
			rec := httptest.NewRecorder()
			ingressHandler(f).ServeHTTP(rec, ingressRequest(body, "multipart/form-data; boundary=test", tc.token))
			requireProblem(t, rec, tc.want)
			if body.read {
				t.Fatal("rejected upload consumed body")
			}
		})
	}
	for _, tc := range []struct {
		name        string
		reverse     bool
		size        int
		ingestError error
		want        ProblemType
		retry       string
	}{
		{"part_order", true, 1, nil, TypeMalformedRequest, ""},
		{"live_limit", false, 140000, nil, TypePayloadTooLarge, ""},
		{"quota", false, 1, diagnostics.ErrQuotaExceeded, TypeRateLimited, "60"},
		{"foreign_profile", false, 1, diagnostics.ErrProfileMismatch, TypeMalformedRequest, ""},
		{"child_profile", false, 1, diagnostics.ErrChildProfileForbidden, TypePermissionDenied, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := availableIngress()
			f.status.MaxBundleBytes = 1
			if tc.ingestError != nil {
				f.ingest = func(context.Context, int, *string, []byte, io.Reader) (diagnostics.IngestResult, error) {
					return diagnostics.IngestResult{}, tc.ingestError
				}
			}
			body, ct := ingressBody(t, tc.reverse, tc.size)
			rec := httptest.NewRecorder()
			ingressHandler(f).ServeHTTP(rec, ingressRequest(body, ct, memberToken))
			requireProblem(t, rec, tc.want)
			if rec.Header().Get("Retry-After") != tc.retry {
				t.Fatal(rec.Header())
			}
		})
	}
}

type ingressUnreadBody struct{ read bool }

func (b *ingressUnreadBody) Read([]byte) (int, error) {
	b.read = true
	return 0, errors.New("unexpected read")
}

func TestDiagnosticsIngressSharesBridgeAdmission(t *testing.T) {
	f := availableIngress()
	entered, release := make(chan struct{}), make(chan struct{})
	f.ingest = func(context.Context, int, *string, []byte, io.Reader) (diagnostics.IngestResult, error) {
		close(entered)
		<-release
		return diagnostics.IngestResult{ReportID: "synthetic-report", ShortID: "SILO-TEST"}, nil
	}
	service := handlers.NewDiagnosticsHandler(f)
	deps := parityDeps(false)
	deps.DiagnosticsIngress = service
	h := NewHandler(deps)
	body, ct := ingressBody(t, false, 1)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, ingressRequest(body, ct, memberToken))
		done <- rec
	}()
	defer close(release)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("ingest did not acquire admission")
	}
	unread := &ingressUnreadBody{}
	request := ingressRequest(unread, "multipart/form-data; boundary=synthetic", memberToken)
	request = request.WithContext(apimw.SetClaims(request.Context(), &auth.Claims{UserID: 1, TokenType: auth.TokenTypeAccess}))
	rejected := httptest.NewRecorder()
	service.HandleUpload(rejected, request)
	if rejected.Code != 503 || rejected.Header().Get("Retry-After") != "5" || unread.read {
		t.Fatal(rejected.Code, rejected.Header(), unread.read)
	}
	// Also prove the same admission rejection is surfaced as a v2 problem.
	rejected = httptest.NewRecorder()
	h.ServeHTTP(rejected, ingressRequest(&ingressUnreadBody{}, "multipart/form-data; boundary=synthetic", memberToken))
	requireProblem(t, rejected, TypeDependencyUnavailable)
	if rejected.Header().Get("Retry-After") != "5" {
		t.Fatal(rejected.Header())
	}
	// Closing release is deferred so any assertion failure still frees the request.
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("admitted upload did not finish")
		}
	})
}
