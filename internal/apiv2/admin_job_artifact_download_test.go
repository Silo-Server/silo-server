package apiv2

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
)

type jobArtifactStub struct {
	calls int
	id    string
	body  *diagnosticTestBody
	err   error
}

func (s *jobArtifactStub) OpenAdminJobArtifact(_ context.Context, id string) (handlers.AdminJobArtifactDownload, error) {
	s.calls++
	s.id = id
	if s.err != nil {
		return handlers.AdminJobArtifactDownload{}, s.err
	}
	return handlers.AdminJobArtifactDownload{Filename: "job.json.gz", Size: new(int64(6)), Body: s.body}, nil
}

func jobArtifactHandler(t *testing.T, stub *jobArtifactStub) (http.Handler, *artworkurl.Signer) {
	t.Helper()
	signer := artworkurl.NewJobArtifactSigner("test-secret", 15*time.Minute)
	deps := requestDeps(fixtureRequests())
	deps.AdminJobArtifacts = stub
	deps.AdminJobArtifactSigner = signer
	return NewHandler(deps), signer
}

// The route replaces a presigned S3 URL, which the browser opens in a new tab
// with no Authorization header. It must therefore serve on the signature alone,
// with no session of any kind.
func TestAdminJobArtifactDownloadServesSignedCapabilityWithoutSession(t *testing.T) {
	stub := &jobArtifactStub{body: &diagnosticTestBody{Reader: strings.NewReader("bundle")}}
	h, signer := jobArtifactHandler(t, stub)
	path, _ := signer.SignFor("job-abc", time.Now(), 15*time.Minute)

	rec := do(t, h, "GET", path, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "bundle" || stub.id != "job-abc" {
		t.Fatalf("body=%q id=%q", rec.Body.String(), stub.id)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/gzip" {
		t.Fatalf("content type = %q", got)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "job.json.gz") {
		t.Fatalf("disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	if !stub.body.closed {
		t.Fatal("body not closed")
	}
}

// Every rejection is a 404 so the route never tells an unauthorized caller
// whether a job exists, and none of them reaches storage.
func TestAdminJobArtifactDownloadRejectsBadCapabilities(t *testing.T) {
	stub := &jobArtifactStub{body: &diagnosticTestBody{Reader: strings.NewReader("bundle")}}
	h, signer := jobArtifactHandler(t, stub)
	valid, _ := signer.SignFor("job-abc", time.Now(), 15*time.Minute)
	_, query, _ := strings.Cut(valid, "?")

	expired := artworkurl.NewJobArtifactSigner("test-secret", time.Minute)
	expiredPath, _ := expired.SignFor("job-abc", time.Now().Add(-48*time.Hour), time.Minute)

	// An artwork capability for the same key must not open an artifact.
	artworkSigned, _ := artworkurl.NewSigner("test-secret", 15*time.Minute).SignFor("job-abc", time.Now(), 15*time.Minute)
	_, artworkQuery, _ := strings.Cut(artworkSigned, "?")

	for name, path := range map[string]string{
		"no capability":     Prefix + "/admin/jobs/job-abc/artifact",
		"missing signature": Prefix + "/admin/jobs/job-abc/artifact?exp=99999999999",
		"bad expiry":        Prefix + "/admin/jobs/job-abc/artifact?exp=soon&sig=x",
		"tampered":          Prefix + "/admin/jobs/job-abc/artifact?" + strings.Replace(query, "sig=", "sig=x", 1),
		"another job":       Prefix + "/admin/jobs/job-xyz/artifact?" + query,
		"expired":           expiredPath,
		"artwork domain":    Prefix + "/admin/jobs/job-abc/artifact?" + artworkQuery,
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, "GET", path, "", nil)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	if stub.calls != 0 {
		t.Fatalf("storage reached %d times by unauthorized callers", stub.calls)
	}
}

func TestAdminJobArtifactDownloadHidesMissingArtifacts(t *testing.T) {
	stub := &jobArtifactStub{err: handlers.ErrJobArtifactNotFound}
	h, signer := jobArtifactHandler(t, stub)
	path, _ := signer.SignFor("job-abc", time.Now(), 15*time.Minute)

	rec := do(t, h, "GET", path, "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if stub.calls != 1 {
		t.Fatalf("calls = %d", stub.calls)
	}
}

// Once the capability verifies, the caller has proven it holds a URL for this
// job. Reporting an outage as 404 from there would tell an administrator their
// artifact is gone when storage is only unreachable.
func TestAdminJobArtifactDownloadReportsStorageOutagesAsUnavailable(t *testing.T) {
	stub := &jobArtifactStub{err: errors.New("storage unreachable")}
	h, signer := jobArtifactHandler(t, stub)
	path, _ := signer.SignFor("job-abc", time.Now(), 15*time.Minute)

	rec := do(t, h, "GET", path, "", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// Clients need to know whether downloading and public links work before they
// fetch a job, because both depend on the configured backend.
func TestAdminJobCapabilitiesReportBackendSupport(t *testing.T) {
	for name, tc := range map[string]struct {
		publicLinks bool
		artifacts   bool
		wantPublic  string
		wantDownloa string
	}{
		"s3":    {publicLinks: true, artifacts: false, wantPublic: `"public_links":true`, wantDownloa: `"artifact_download":true`},
		"local": {publicLinks: false, artifacts: true, wantPublic: `"public_links":false`, wantDownloa: `"artifact_download":true`},
		"none":  {publicLinks: false, artifacts: false, wantPublic: `"public_links":false`, wantDownloa: `"artifact_download":false`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeAdminTasks()
			f.publicLinks = tc.publicLinks
			deps, _ := libraryDeps(t)
			deps.AdminTaskJobs = f
			if tc.artifacts {
				deps.AdminJobArtifacts = &jobArtifactStub{}
			}
			h := newTestHandler(t, deps)
			rec := do(t, h, "GET", Prefix+"/admin/jobs/capabilities", "", bearer(adminToken))
			if rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.wantPublic) || !strings.Contains(rec.Body.String(), tc.wantDownloa) {
				t.Fatalf("body = %s", rec.Body)
			}
		})
	}
	// Administrator-only, like every other admin job operation.
	f := newFakeAdminTasks()
	deps, _ := libraryDeps(t)
	deps.AdminTaskJobs = f
	requireProblem(t, do(t, newTestHandler(t, deps), "GET", Prefix+"/admin/jobs/capabilities", "", bearer(memberToken)), TypePermissionDenied)
}

// slowArtifactReader yields one chunk per read after a delay, standing in for
// a large export on a slow link: the whole body takes longer than the server's
// WriteTimeout even though it never stalls.
type slowArtifactReader struct {
	chunks []string
	delay  time.Duration
}

func (r *slowArtifactReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]
	return n, nil
}

// The API server sets an absolute WriteTimeout, and the route answers
// Accept-Ranges: none, so a download cut off there cannot resume. The stream
// must roll its write deadline forward while it makes progress.
func TestAdminJobArtifactDownloadOutlastsServerWriteTimeout(t *testing.T) {
	body := &slowArtifactReader{chunks: []string{"ab", "cd", "ef"}, delay: 150 * time.Millisecond}
	stub := &jobArtifactStub{body: &diagnosticTestBody{Reader: body}}
	h, signer := jobArtifactHandler(t, stub)
	srv := httptest.NewUnstartedServer(h)
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	path, _ := signer.SignFor("job-abc", time.Now(), 15*time.Minute)
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	if err != nil || string(got) != "abcdef" {
		t.Fatalf("body = %q, err = %v", got, err)
	}
}
