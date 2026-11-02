package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeBackups struct {
	mu         sync.Mutex
	calls      []string
	restoreErr error
	restoreReq backup.RestoreRequest
	uploadErr  error
	upload     backup.UploadSession
}

func (f *fakeBackups) UploadsAvailable() bool { return true }

// session is the upload the fake answers with: f.upload, or one taking
// parts.
func (f *fakeBackups) session() backup.UploadSession {
	if f.upload.ID != "" {
		return f.upload
	}
	at := time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)
	return backup.UploadSession{ID: testUploadID, Status: backup.UploadUploading, SizeBytes: 10, PartSize: backup.DefaultUploadPartSize, PartCount: 1,
		CreatedAt: at, UpdatedAt: at, Location: "/data/backups/uploads/" + testUploadID + "/"}
}
func (f *fakeBackups) Uploads(context.Context) ([]backup.UploadSession, error) {
	f.calls = append(f.calls, "uploads")
	return []backup.UploadSession{f.session()}, f.uploadErr
}
func (f *fakeBackups) CreateUpload(_ context.Context, _ int, size int64, _ string, _ int64) (backup.UploadSession, error) {
	f.calls = append(f.calls, "create upload")
	return f.session(), f.uploadErr
}
func (f *fakeBackups) PutUploadPart(_ context.Context, id string, part int, body io.Reader, _ int64) (backup.UploadSession, error) {
	data, err := io.ReadAll(body)
	f.mu.Lock()
	f.calls = append(f.calls, "part "+id+" "+strconv.Itoa(part)+" "+string(data))
	f.mu.Unlock()
	if err != nil {
		return backup.UploadSession{}, fmt.Errorf("storing part %d: %w", part, err)
	}
	return f.session(), f.uploadErr
}
func (f *fakeBackups) CompleteUpload(_ context.Context, id string) (backup.UploadSession, error) {
	f.calls = append(f.calls, "complete "+id)
	return f.session(), f.uploadErr
}
func (f *fakeBackups) DeleteUpload(_ context.Context, id string) error {
	f.calls = append(f.calls, "delete upload "+id)
	return f.uploadErr
}
func (f *fakeBackups) PreviewUpload(_ context.Context, id string) (*backup.RestorePreview, error) {
	f.calls = append(f.calls, "preview upload "+id)
	return &backup.RestorePreview{UploadID: id, Digest: testDigest, CreatedAt: time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)}, f.uploadErr
}

func (f *fakeBackups) Configured() bool { return true }
func (f *fakeBackups) Destinations() []string {
	return []string{backup.DestinationDefault}
}
func (f *fakeBackups) List(context.Context) (backup.Listing, error) {
	f.calls = append(f.calls, "list")
	return backup.Listing{ServerID: "s", Destination: backup.DestinationDefault, DestinationAvailable: true, Enabled: true,
		Backups: []backup.Summary{{ID: testBackupID, Kind: backup.KindManual, Status: backup.StatusComplete, CreatedAt: time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)}}}, nil
}
func (f *fakeBackups) StartManual(context.Context) (string, error) {
	f.calls = append(f.calls, "start")
	return testBackupID, nil
}
func (f *fakeBackups) Delete(_ context.Context, id string) error {
	f.calls = append(f.calls, "delete "+id)
	return nil
}
func (f *fakeBackups) DownloadLinks(_ context.Context, id, scope string, _ time.Duration) ([]backup.ObjectLink, error) {
	f.calls = append(f.calls, "links "+id+" "+scope)
	return []backup.ObjectLink{{Name: backup.DumpName, Size: 4}}, nil
}
func (f *fakeBackups) Preview(_ context.Context, id string) (*backup.RestorePreview, error) {
	f.calls = append(f.calls, "preview "+id)
	return &backup.RestorePreview{BackupID: id, Digest: testDigest, CreatedAt: time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)}, nil
}
func (f *fakeBackups) StartRestore(_ context.Context, userID int, req backup.RestoreRequest) (*models.AdminJob, error) {
	f.calls = append(f.calls, "restore "+req.BackupID)
	f.restoreReq = req
	if f.restoreErr != nil {
		return nil, f.restoreErr
	}
	payload, _ := json.Marshal(req)
	return &models.AdminJob{ID: "job-1", JobType: adminjob.JobTypeBackupRestore, Status: adminjob.StatusQueued, CreatedByUserID: userID, RequestPayload: payload,
		RequestedAt: time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)}, nil
}
func (f *fakeBackups) OpenObject(_ context.Context, id, name string) (io.ReadCloser, int64, error) {
	f.calls = append(f.calls, "open "+id+" "+name)
	return io.NopCloser(strings.NewReader("dump")), 4, nil
}

const (
	testBackupID = "20261031T040000Z-manual"
	testUploadID = "0f8fad5b-d9cb-469f-a165-70867728950e"
)

var testDigest = strings.Repeat("ab", 32)

func backupsHandler(f *fakeBackups) http.Handler { return backupsHandlerWith(f, nil) }

func backupsHandlerWith(f *fakeBackups, adjust func(*Dependencies)) http.Handler {
	deps := requestDeps(fixtureRequests())
	if adjust != nil {
		adjust(&deps)
	}
	deps.AdminBackups = f
	deps.AdminBackupSigner = artworkurl.NewBackupObjectSigner("test-secret", time.Minute)
	return NewHandler(deps)
}

// TestAdminBackupEndpointsRequireAdminRole: every backup operation answers
// only an acting administrator. A member account, even on its household's
// primary profile, is refused before the service runs.
func TestAdminBackupEndpointsRequireAdminRole(t *testing.T) {
	f := &fakeBackups{}
	h := backupsHandler(f)
	base := Prefix + "/admin/backups"
	upload := base + "/uploads/" + testUploadID
	restore := `{"digest":"` + testDigest + `","selection":{"settings":["general"]}}`
	ops := []struct{ method, path, body, contentType string }{
		{http.MethodGet, base + "/capabilities", "", ""},
		{http.MethodGet, base, "", ""},
		{http.MethodPost, base, "", ""},
		{http.MethodDelete, base + "/" + testBackupID, "", ""},
		{http.MethodPost, base + "/" + testBackupID + "/download-links", `{"scope":"full"}`, ""},
		{http.MethodGet, base + "/" + testBackupID + "/restore-set", "", ""},
		{http.MethodPost, base + "/" + testBackupID + "/restore", restore, ""},
		{http.MethodGet, base + "/uploads", "", ""},
		{http.MethodPost, base + "/uploads", `{"size_bytes":10}`, ""},
		{http.MethodPut, upload + "/parts/0", "part bytes", "application/octet-stream"},
		{http.MethodPost, upload + "/complete", "", ""},
		{http.MethodGet, upload + "/restore-set", "", ""},
		{http.MethodPost, upload + "/restore", restore, ""},
		{http.MethodDelete, upload, "", ""},
	}
	headers := func(base map[string]string, contentType string) map[string]string {
		if contentType == "" {
			return base
		}
		return with(base, "Content-Type", contentType)
	}
	for _, op := range ops {
		requireProblem(t, do(t, h, op.method, op.path, op.body, headers(nil, op.contentType)), TypeAuthenticationRequired)
		requireProblem(t, do(t, h, op.method, op.path, op.body, headers(with(bearer(memberToken), "X-Profile-Id", "p-owner"), op.contentType)), TypePermissionDenied)
	}
	if len(f.calls) != 0 {
		t.Fatalf("refused requests reached the service: %v", f.calls)
	}
	// A restore from an upload names the upload as its source, never a backup.
	if rec := do(t, h, http.MethodPost, upload+"/restore", restore, actingRequestAdmin); rec.Code != http.StatusAccepted ||
		f.restoreReq.UploadID != testUploadID || f.restoreReq.BackupID != "" {
		t.Fatalf("upload restore = %d %s, request %+v", rec.Code, rec.Body.String(), f.restoreReq)
	}
}

// TestAdminBackupRestoreRefusals: a restore whose source changed since its
// preview, or that would run beside another backup or restore, is refused
// before any job is queued.
func TestAdminBackupRestoreRefusals(t *testing.T) {
	body := `{"digest":"` + testDigest + `","selection":{"libraries":["3"],"collections":["collection:c1"]}}`
	path := Prefix + "/admin/backups/" + testBackupID + "/restore"
	f := &fakeBackups{restoreErr: backup.ErrBusy}
	requireProblem(t, do(t, backupsHandler(f), http.MethodPost, path, body, actingRequestAdmin), TypeConflict)
	// A changed restore set has its own type: the client previews again
	// rather than waiting for a running job.
	f = &fakeBackups{restoreErr: backup.ErrDigestMismatch}
	requireProblem(t, do(t, backupsHandler(f), http.MethodPost, path, body, actingRequestAdmin), TypeRestoreSetChanged)
	f = &fakeBackups{restoreErr: &backup.SelectionError{Message: "choose at least one item to restore"}}
	requireProblem(t, do(t, backupsHandler(f), http.MethodPost, path, `{"digest":"`+testDigest+`","selection":{}}`, actingRequestAdmin), TypeValidationFailed)
	f = &fakeBackups{}
	rec := do(t, backupsHandler(f), http.MethodPost, path, body, actingRequestAdmin)
	var job AdminTaskJob
	_ = json.Unmarshal(rec.Body.Bytes(), &job)
	if rec.Code != http.StatusAccepted || rec.Header().Get("Location") != Prefix+"/admin/jobs/job-1" ||
		job.BackupRestoreSource == nil || job.BackupRestoreSource.BackupID != testBackupID {
		t.Fatalf("restore = %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if got := f.restoreReq.Selection; len(got[backup.SectionLibraries]) != 1 || len(got[backup.SectionCollections]) != 1 || len(got) != 2 {
		t.Fatalf("selection = %v", got)
	}
}

// TestAdminBackupObjectDownloadNeedsItsCapability: the streaming route takes
// no session, so only a link minted for that object of that backup opens it.
func TestAdminBackupObjectDownloadNeedsItsCapability(t *testing.T) {
	f := &fakeBackups{}
	h := backupsHandler(f)
	rec := do(t, h, http.MethodPost, Prefix+"/admin/backups/"+testBackupID+"/download-links", `{"scope":"full"}`, actingRequestAdmin)
	var links struct {
		Items []AdminBackupDownloadLink `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &links); err != nil || len(links.Items) != 1 {
		t.Fatalf("links = %d %s", rec.Code, rec.Body.String())
	}
	signed := links.Items[0].URL
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	unsigned := Prefix + "/admin/backups/" + testBackupID + "/objects/" + backup.DumpName
	otherObject := Prefix + "/admin/backups/" + testBackupID + "/objects/" + backup.BlobsName + "?" + u.RawQuery
	otherBackup := Prefix + "/admin/backups/20261030T040000Z-manual/objects/" + backup.DumpName + "?" + u.RawQuery
	for _, path := range []string{unsigned, otherObject, otherBackup} {
		requireProblem(t, do(t, h, http.MethodGet, path, "", actingRequestAdmin), TypeNotFound)
	}
	if len(f.calls) != 0 {
		t.Fatalf("a request without its capability opened an object: %v", f.calls)
	}
	ok := do(t, h, http.MethodGet, signed, "", nil)
	if ok.Code != http.StatusOK || ok.Body.String() != "dump" {
		t.Fatalf("signed download = %d %q", ok.Code, ok.Body.String())
	}
}

// TestAdminBackupUploadRefusals: the refusals a client tells apart by type
// or status, not by detail.
func TestAdminBackupUploadRefusals(t *testing.T) {
	base := Prefix + "/admin/backups/uploads"
	part := with(actingRequestAdmin, "Content-Type", "application/octet-stream")
	cases := []struct {
		err                error
		method, path, body string
		headers            map[string]string
		want               ProblemType
	}{
		{backup.ErrTooManyUploads, http.MethodPost, base, `{"size_bytes":10}`, actingRequestAdmin, TypeUploadLimitReached},
		{&backup.UploadPartError{Message: "part 0 must be 10 bytes", TooLarge: true}, http.MethodPut, base + "/" + testUploadID + "/parts/0", "too long", part, TypePayloadTooLarge},
		{&backup.UploadPartError{Message: "part 9 is outside this upload's parts 0 to 0"}, http.MethodPut, base + "/" + testUploadID + "/parts/9", "x", part, TypeValidationFailed},
		{backup.ErrUploadNotFound, http.MethodGet, base + "/" + testUploadID + "/restore-set", "", actingRequestAdmin, TypeNotFound},
	}
	for _, c := range cases {
		requireProblem(t, do(t, backupsHandler(&fakeBackups{uploadErr: c.err}), c.method, c.path, c.body, c.headers), c.want)
	}
	// A part is bytes, never JSON.
	requireProblem(t, do(t, backupsHandler(&fakeBackups{}), http.MethodPut, base+"/"+testUploadID+"/parts/0", `{"a":1}`, actingRequestAdmin), TypeUnsupportedMediaType)
}

// TestAdminBackupUploadPartReadDeadline: a part's read deadline is its
// operation's own over a real connection. A part slower than the
// listener's read timeout still arrives, and one slower than the part's
// deadline answers 408. It takes about two seconds: the deadlines are a real
// listener's, which no fake clock reaches.
func TestAdminBackupUploadPartReadDeadline(t *testing.T) {
	f := &fakeBackups{}
	srv := httptest.NewUnstartedServer(backupsHandlerWith(f, func(d *Dependencies) { d.uploadPartReadTimeout = 600 * time.Millisecond }))
	srv.Config.ReadTimeout = 100 * time.Millisecond
	srv.Start()
	defer srv.Close()
	send := func(stall time.Duration) (int, string) {
		pr, pw := io.Pipe()
		go func() {
			_, _ = pw.Write([]byte("part "))
			time.Sleep(stall) //nolint:forbidigo // the stall is what the test measures against the deadlines
			_, _ = pw.Write([]byte("bytes"))
			_ = pw.Close()
		}()
		req, err := http.NewRequest(http.MethodPut, srv.URL+Prefix+"/admin/backups/uploads/"+testUploadID+"/parts/0", pr)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range with(actingRequestAdmin, "Content-Type", "application/octet-stream") {
			req.Header.Set(k, v)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if status, body := send(300 * time.Millisecond); status != http.StatusOK {
		t.Fatalf("a part slower than the listener's read timeout = %d %s", status, body)
	}
	if status, body := send(1500 * time.Millisecond); status != http.StatusRequestTimeout || !strings.Contains(body, TypeRequestTimeout.URI()) {
		t.Fatalf("a part slower than its deadline = %d %s", status, body)
	}
}
