package abs

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/models"
)

// downloadAccounts backs newDownloadService: every account is an enabled
// user who may download unless denied names it.
type downloadAccounts struct {
	denied map[int]bool
}

func (a *downloadAccounts) GetByID(_ context.Context, id int) (*models.User, error) {
	allowed := !a.denied[id]
	return &models.User{ID: id, Role: models.RoleUser, Enabled: true, DownloadAllowed: &allowed}, nil
}

type downloadFiles struct {
	downloads.FileResolver
	byID map[int]*models.MediaFile
}

func (f downloadFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	return f.byID[id], nil
}

type allowAllItems struct{}

func (allowAllItems) EnsureAccessible(context.Context, string, catalog.AccessFilter) error {
	return nil
}

// newDownloadService builds the real download service, so ABS tests apply the
// native download rules rather than a fake's. enabled is the server's Allow
// downloads setting.
func newDownloadService(enabled bool, accounts *downloadAccounts, files ...*models.MediaFile) *downloads.Service {
	byID := map[int]*models.MediaFile{}
	for _, f := range files {
		byID[f.ID] = f
	}
	return downloads.NewService(nil, nil, nil, downloadFiles{byID: byID}, nil, nil, accounts, allowAllItems{}, nil,
		&config.DownloadConfig{Enabled: enabled})
}

// The /download file route follows the account's download permission and the
// server's Allow downloads setting; the bare file route is a stream and keeps
// playing for an account that may not download.
func TestFileRoutes_FollowDownloadPermission(t *testing.T) {
	body := []byte("audiobook-bytes")
	path := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 7, ContentID: "book-1", MediaFolderID: 1, FilePath: path}
	ino := trackInoFor("book-1", 0)

	cases := map[string]struct {
		allowDownloads bool
		denied         bool
		route          string
		want           int
	}{
		"download allowed":                   {true, false, "/download", http.StatusOK},
		"download permission off":            {true, true, "/download", http.StatusForbidden},
		"Allow downloads off":                {false, false, "/download", http.StatusForbidden},
		"stream without download permission": {true, true, "", http.StatusOK},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			accounts := &downloadAccounts{denied: map[int]bool{1: tc.denied}}
			h := New(Dependencies{MediaStore: &accessMediaStore{
				item:  &models.MediaItem{ContentID: "book-1"},
				files: []*models.MediaFile{file},
			}})
			h.SetDownloads(newDownloadService(tc.allowDownloads, accounts, file))

			rec := dispatchABSWithParams(http.MethodGet, "/api/items/book-1/file/"+ino+tc.route,
				map[string]string{"libraryItemId": "book-1", "ino": ino}, nil, "1", "", h.handleFileStream)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want != http.StatusOK {
				return
			}
			if !bytes.Equal(rec.Body.Bytes(), body) {
				t.Fatalf("body = %q, want the file", rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "audio/mp4" {
				t.Errorf("Content-Type = %q, want audio/mp4", got)
			}
			wantAttachment := tc.route == "/download"
			if got := rec.Header().Get("Content-Disposition"); strings.HasPrefix(got, "attachment") != wantAttachment {
				t.Errorf("Content-Disposition = %q, want attachment: %v", got, wantAttachment)
			}
		})
	}
}

// The user object ABS clients read at sign-in and from /me reports the
// account's real download permission, so they hide offline save without it.
func TestUserObject_ReportsDownloadPermission(t *testing.T) {
	cases := map[string]struct {
		allowDownloads bool
		denied         bool
		wire           bool
		want           bool
	}{
		"allowed":                 {true, false, true, true},
		"download permission off": {true, true, true, false},
		"Allow downloads off":     {false, false, true, false},
		"no download service":     {true, false, false, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := New(Dependencies{MediaStore: &stubMediaStore{}})
			if tc.wire {
				h.SetDownloads(newDownloadService(tc.allowDownloads, &downloadAccounts{denied: map[int]bool{42: tc.denied}}))
			}
			env := h.loginEnvelope(t.Context(), time.Unix(100, 0), ctxAuth{UserID: "42"}, "Reader", "", "", false)
			user, _ := env["user"].(map[string]any)
			perms, _ := user["permissions"].(map[string]any)
			if got := perms["download"]; got != tc.want {
				t.Errorf("permissions.download = %v, want %v", got, tc.want)
			}
		})
	}
}
