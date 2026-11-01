package apiv2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeBackups struct {
	calls      []string
	restoreErr error
	restoreReq backup.RestoreRequest
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
	restore := `{"digest":"` + testDigest + `","selection":{"settings":["general"]}}`
	ops := []struct{ method, path, body string }{
		{http.MethodGet, base + "/capabilities", ""},
		{http.MethodGet, base, ""},
		{http.MethodPost, base, ""},
		{http.MethodDelete, base + "/" + testBackupID, ""},
		{http.MethodPost, base + "/" + testBackupID + "/download-links", `{"scope":"full"}`},
		{http.MethodGet, base + "/" + testBackupID + "/restore-set", ""},
		{http.MethodPost, base + "/" + testBackupID + "/restore", restore},
	}
	for _, op := range ops {
		requireProblem(t, do(t, h, op.method, op.path, op.body, nil), TypeAuthenticationRequired)
		requireProblem(t, do(t, h, op.method, op.path, op.body, with(bearer(memberToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	}
	if len(f.calls) != 0 {
		t.Fatalf("refused requests reached the service: %v", f.calls)
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
