package themesongs

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type fixedStore struct{ files []File }

func (s *fixedStore) Resolve(_ context.Context, id string, _ bool, _ catalog.AccessFilter) (string, []File, error) {
	return id, s.files, nil
}

func testFile(t *testing.T) File {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.mp3")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return File{Song: Song{ID: "1", Title: "Theme", Container: "mp3"}, OwnerPath: dir, Path: path, Size: info.Size(), Modified: info.ModTime().Truncate(time.Microsecond)}
}

func TestGrantBindsIdentityOwnerAndFile(t *testing.T) {
	file := testFile(t)
	svc := NewService(&fixedStore{[]File{file}}, "secret")
	identity := Identity{UserID: 7, ProfileID: "profile", SessionID: "session", PolicyRevision: 3}
	token, expiry, err := svc.Mint(context.Background(), identity, "movie", "1", catalog.AccessFilter{}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(expiry) > time.Minute {
		t.Fatal("grant exceeds login lifetime")
	}
	grant, err := svc.Validate(token, "movie", "1")
	if err != nil || grant.Identity != identity || grant.Size != file.Size || grant.Modified != file.Modified.UnixNano() {
		t.Fatalf("grant=%+v err=%v", grant, err)
	}
	for _, pair := range [][2]string{{"other", "1"}, {"movie", "2"}} {
		if _, err := svc.Validate(token, pair[0], pair[1]); !errors.Is(err, ErrGrant) {
			t.Fatal("cross-resource grant accepted")
		}
	}
	if _, err := NewService(&fixedStore{}, "other secret").Validate(token, "movie", "1"); !errors.Is(err, ErrGrant) {
		t.Fatal("wrong key accepted")
	}
	if _, _, err := svc.Mint(context.Background(), identity, "movie", "1", catalog.AccessFilter{}, time.Now().Add(-time.Second)); !errors.Is(err, ErrGrant) {
		t.Fatal("expired identity accepted")
	}
}

func TestOriginalAudioHTTPAndStaleFile(t *testing.T) {
	file := testFile(t)
	for _, tc := range []struct {
		method, header, value string
		status                int
		body                  string
	}{
		{"GET", "Range", "bytes=2-5", 206, "2345"},
		{"HEAD", "", "", 200, ""},
		{"GET", "Range", "bytes=99-", 416, "invalid range: failed to overlap\n"},
	} {
		t.Run(tc.method+tc.value, func(t *testing.T) {
			f, err := Open(file)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			r := httptest.NewRequest(tc.method, "/audio", nil)
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			w := httptest.NewRecorder()
			Serve(w, r, file, f)
			if w.Code != tc.status || w.Body.String() != tc.body {
				t.Fatalf("%d %q", w.Code, w.Body.String())
			}
			if tc.status == 200 && w.Header().Get("Content-Length") != "10" {
				t.Fatal(w.Header())
			}
		})
	}
	f, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	Serve(w, httptest.NewRequest("GET", "/audio", nil), file, f)
	_ = f.Close()
	r := httptest.NewRequest("GET", "/audio", nil)
	r.Header.Set("If-None-Match", w.Header().Get("ETag"))
	f, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	w = httptest.NewRecorder()
	Serve(w, r, file, f)
	if w.Code != 304 {
		t.Fatal(w.Code)
	}
	if err := os.WriteFile(file.Path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if f, err := Open(file); !errors.Is(err, ErrUnavailable) {
		if f != nil {
			_ = f.Close()
		}
		t.Fatal("replacement accepted", err)
	}
}

func TestOpenRefusesEscapingSymlink(t *testing.T) {
	file := testFile(t)
	outside := filepath.Join(t.TempDir(), "secret.mp3")
	if err := os.Rename(file.Path, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, file.Path); err != nil {
		t.Fatal(err)
	}
	if f, err := Open(file); !errors.Is(err, ErrUnavailable) {
		if f != nil {
			_ = f.Close()
		}
		t.Fatal("escaping symlink accepted", err)
	}
}
