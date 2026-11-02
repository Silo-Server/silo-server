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

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/backup"
)

type fakeBackups struct {
	calls []string
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
func (f *fakeBackups) DownloadLinks(_ context.Context, id string, _ time.Duration) ([]backup.ObjectLink, error) {
	f.calls = append(f.calls, "links "+id)
	return []backup.ObjectLink{{Name: backup.DumpName, Size: 4}}, nil
}
func (f *fakeBackups) OpenObject(_ context.Context, id, name string) (io.ReadCloser, int64, error) {
	f.calls = append(f.calls, "open "+id+" "+name)
	return io.NopCloser(strings.NewReader("dump")), 4, nil
}

const (
	testBackupID = "20261031T040000Z-manual"
)

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
	ops := []struct{ method, path, body string }{
		{http.MethodGet, base + "/capabilities", ""},
		{http.MethodGet, base, ""},
		{http.MethodPost, base, ""},
		{http.MethodDelete, base + "/" + testBackupID, ""},
		{http.MethodPost, base + "/" + testBackupID + "/download-links", `{"scope":"full"}`},
	}
	for _, op := range ops {
		requireProblem(t, do(t, h, op.method, op.path, op.body, nil), TypeAuthenticationRequired)
		requireProblem(t, do(t, h, op.method, op.path, op.body, with(bearer(memberToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	}
	if len(f.calls) != 0 {
		t.Fatalf("refused requests reached the service: %v", f.calls)
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
