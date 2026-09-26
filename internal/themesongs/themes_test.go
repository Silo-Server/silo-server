package themesongs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/golang-jwt/jwt/v5"
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
	token, expiry, err := svc.Mint(identity, "movie", file, DeliveryOriginal, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(expiry) > time.Minute {
		t.Fatal("grant exceeds login lifetime")
	}
	grant, err := svc.Validate(token, "movie", "1")
	if err != nil || grant.Identity != identity || grant.Size != file.Size || grant.Modified != file.Modified.UnixNano() || grant.Delivery != DeliveryOriginal {
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
	if _, _, err := svc.Mint(identity, "movie", file, DeliveryOriginal, time.Now().Add(-time.Second)); !errors.Is(err, ErrGrant) {
		t.Fatal("expired identity accepted")
	}
	converted, _, err := svc.Mint(identity, "movie", file, DeliveryConverted, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if grant, err := svc.Validate(converted, "movie", "1"); err != nil || grant.Delivery != DeliveryConverted {
		t.Fatalf("converted grant=%+v err=%v", grant, err)
	}
	// An API that predates conversion validates only the original audience, so
	// it must refuse a converted grant rather than serve the original bytes.
	if _, err := jwt.ParseWithClaims(converted, &Grant{}, func(*jwt.Token) (any, error) { return svc.key, nil }, jwt.WithAudience(grantAudienceOriginal)); err == nil {
		t.Fatal("converted grant carries the original audience")
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

func TestServeReplacesAbsoluteWriteDeadline(t *testing.T) {
	file := testFile(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := Open(file)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer func() { _ = f.Close() }()
		// Simulate the listener's absolute timeout having elapsed before the
		// next body write, without waiting for the production timeout.
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
			t.Error(err)
			return
		}
		Serve(w, r, file, f)
	}))
	defer server.Close()
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "0123456789" {
		t.Fatalf("status=%d body=%q err=%v", response.StatusCode, body, err)
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

func TestThemeAudioExtensionsExcludeVideo(t *testing.T) {
	for _, ext := range []string{"mp3", "m4a", "m4b", "flac", "ogg", "opus", "wav", "aac"} {
		for _, path := range []string{"/show/theme." + ext, "/show/theme-music/opening." + strings.ToUpper(ext)} {
			owner, ok := OwnerDirectory(path)
			if !ok || owner != "/show" || Container(path) != ext || !strings.HasPrefix(ContentType(ext), "audio/") {
				t.Errorf("audio theme %q: owner=%q ok=%v container=%q", path, owner, ok, Container(path))
			}
		}
	}
	for _, ext := range []string{"mkv", "mp4", "webm", "srt", "jpg"} {
		for _, path := range []string{"/show/theme." + ext, "/show/theme-music/file." + ext} {
			if owner, ok := OwnerDirectory(path); ok || Container(path) != "" {
				t.Errorf("non-audio %q was classified as theme owned by %q", path, owner)
			}
		}
	}
}
