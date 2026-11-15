package nodesource

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
)

const testSecret = "grant-test-secret"

func writeSource(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mintFile(t *testing.T, grants *Grants, nodeID int, path string) string {
	t.Helper()
	grant, err := grants.ForFile(t.Context(), nodeID, &models.MediaFile{ID: 42, FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

// resign signs claims as a grant with grants' key, whatever they say.
func resign(t *testing.T, grants *Grants, claims any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	key, err := grants.key()
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(grantMAC(key, payload))
}

func TestGrantVerifyRefusesWhatItDidNotMint(t *testing.T) {
	grants := NewGrants(func() string { return testSecret })
	path := writeSource(t, "movie.mkv", "movie")
	grant := mintFile(t, grants, 7, path)
	claims, err := grants.Verify(grant)
	if err != nil || claims.Node != 7 || claims.Kind != KindFile || claims.ID != 42 || claims.Path != path || claims.Size != 5 {
		t.Fatalf("verify = %+v %v", claims, err)
	}

	payload, mac, _ := strings.Cut(grant, ".")
	decoded, _ := base64.RawURLEncoding.DecodeString(payload)
	wrongVersion := claims
	wrongVersion.V = 2
	for name, token := range map[string]string{
		"tampered":      base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(decoded), `"node":7`, `"node":8`, 1))) + "." + mac,
		"another key":   mintFile(t, NewGrants(func() string { return "another secret" }), 7, path),
		"oversized":     grant + strings.Repeat("A", maxGrantBytes),
		"wrong version": resign(t, grants, wrongVersion),
		"malformed":     "not-a-grant",
	} {
		if _, err := grants.Verify(token); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: verify = %v, want ErrInvalidGrant", name, err)
		}
	}

	later := NewGrants(func() string { return testSecret })
	later.now = func() time.Time { return time.Now().Add(GrantLifetime + time.Second) }
	if _, err := later.Verify(grant); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("an hour later: verify = %v, want ErrGrantExpired", err)
	}
	if _, err := NewGrants(func() string { return "" }).Verify(grant); !errors.Is(err, ErrNoGrantKey) {
		t.Fatalf("no secret: verify = %v, want ErrNoGrantKey", err)
	}
}

// Revisions compare at microseconds, the catalog's precision; the grant keeps
// the nanoseconds the stat returned.
func TestRevisionComparesAtMicroseconds(t *testing.T) {
	base := time.Unix(1_700_000_000, 123_456_000)
	revision := Revision{Kind: KindFile, ID: 42, Size: 5, ModTimeUnixNano: base.Add(100 * time.Nanosecond).UnixNano()}
	sub := revision
	sub.ModTimeUnixNano = base.Add(900 * time.Nanosecond).UnixNano()
	if !revision.Same(sub) || revision.ETag() != sub.ETag() {
		t.Fatalf("a sub-microsecond difference is another revision: %s vs %s", revision.ETag(), sub.ETag())
	}
	later := revision
	later.ModTimeUnixNano = base.Add(time.Microsecond).UnixNano()
	if revision.Same(later) || revision.ETag() == later.ETag() {
		t.Fatalf("a microsecond later is the same revision: %s", later.ETag())
	}
	if want := `"file-42-5-1700000000123456"`; revision.ETag() != want {
		t.Fatalf("etag = %s, want %s", revision.ETag(), want)
	}
}

// A library name that is not valid UTF-8 is carried fspath-encoded, survives
// the grant's JSON unchanged, and is stat'ed through fspath.
func TestGrantCarriesAnEncodedPath(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "B\xe5rd.mkv")
	if err := os.WriteFile(raw, []byte("movie"), 0o600); err != nil {
		t.Skipf("file system refuses a non-UTF-8 name: %v", err)
	}
	encoded := fspath.Encode(raw)
	if encoded == raw {
		t.Fatal("the name needed no encoding")
	}
	grants := NewGrants(func() string { return testSecret })
	grant := mintFile(t, grants, 7, encoded)
	claims, err := grants.Verify(grant)
	if err != nil || claims.Path != encoded || claims.Size != 5 {
		t.Fatalf("verify = %+v %v", claims, err)
	}
	if _, err := claims.Check(t.Context()); err != nil {
		t.Fatalf("check: %v", err)
	}
	// JSON would replace the raw byte with U+FFFD, so a raw path is refused
	// rather than granted under another name.
	if _, err := grants.ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: raw}); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("raw path: %v, want ErrInvalidGrant", err)
	}
}

func TestGrantCheck(t *testing.T) {
	grants := NewGrants(func() string { return testSecret })
	path := writeSource(t, "movie.mkv", "movie")
	claims, err := grants.Verify(mintFile(t, grants, 7, path))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replaced"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := claims.Check(t.Context()); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("changed: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := claims.Check(t.Context()); !errors.Is(err, ErrSourceGone) {
		t.Fatalf("removed: %v", err)
	}
	if _, err := grants.ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: filepath.Dir(path)}); !errors.Is(err, ErrSourceGone) {
		t.Fatalf("directory: %v, want ErrSourceGone", err)
	}

	// A stat that fails for another reason says nothing about the file. A
	// symlink loop stands in for an I/O error or a stale mount.
	if err := os.Symlink(filepath.Base(path), path); err != nil {
		t.Fatal(err)
	}
	if _, err := claims.Check(t.Context()); err == nil || errors.Is(err, ErrSourceGone) || errors.Is(err, ErrSourceChanged) {
		t.Fatalf("unexaminable file: %v, want the stat's own error", err)
	}
	if _, err := grants.ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: path}); err == nil || errors.Is(err, ErrSourceGone) {
		t.Fatalf("mint for an unexaminable file: %v, want the stat's own error", err)
	}
}
