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
	wrongVersion.V = 1
	noIssueTime := claims
	noIssueTime.Iat = 0
	for name, token := range map[string]string{
		"tampered":      base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(decoded), `"node":7`, `"node":8`, 1))) + "." + mac,
		"another key":   mintFile(t, NewGrants(func() string { return "another secret" }), 7, path),
		"oversized":     grant + strings.Repeat("A", maxGrantBytes),
		"wrong version": resign(t, grants, wrongVersion),
		"no issue time": resign(t, grants, noIssueTime),
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

// A grant's whole life is bounded: renewal keeps the issue time, never signs
// an expiry past MaxGrantAge from it, and refuses once it is that old, and
// Verify refuses a grant that old even if its expiry says otherwise.
func TestGrantWholeLifeIsCapped(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	now := start
	grants := NewGrants(func() string { return testSecret })
	grants.now = func() time.Time { return now }
	grant := mintFile(t, grants, 7, writeSource(t, "movie.mkv", "movie"))
	claims, err := grants.Verify(grant)
	if err != nil || claims.Iat != start.Unix() {
		t.Fatalf("minted: %+v %v", claims, err)
	}
	// Renew every 20 minutes, as a node does, until the cap.
	for now = start.Add(RenewInterval); now.Before(start.Add(MaxGrantAge)); now = now.Add(RenewInterval) {
		if grant, err = grants.Renew(claims); err != nil {
			t.Fatalf("renew at %s: %v", now.Sub(start), err)
		}
		if claims, err = grants.Verify(grant); err != nil {
			t.Fatalf("verify renewed at %s: %v", now.Sub(start), err)
		}
		if claims.Iat != start.Unix() || claims.Expires().After(start.Add(MaxGrantAge)) {
			t.Fatalf("renewal at %s moved the issue time or the cap: %+v", now.Sub(start), claims)
		}
	}
	now = start.Add(MaxGrantAge)
	if _, err := grants.Renew(claims); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("renew at the cap: %v, want ErrGrantExpired", err)
	}
	if _, err := grants.Verify(grant); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("verify at the cap: %v, want ErrGrantExpired", err)
	}
	// A grant whose expiry reaches past the cap (signed by hand) is still
	// refused at the cap.
	forged := claims
	forged.Exp = now.Add(time.Hour).Unix()
	if _, err := grants.Verify(resign(t, grants, forged)); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("expiry past the cap: %v, want ErrGrantExpired", err)
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

// A grant from a build before the issue time existed still verifies and
// renews, so a job that holds one survives the update.
func TestALegacyGrantVerifiesAndRenews(t *testing.T) {
	grants := NewGrants(func() string { return testSecret })
	path := writeSource(t, "movie.mkv", "movie")
	current, err := grants.Verify(mintFile(t, grants, 7, path))
	if err != nil {
		t.Fatal(err)
	}
	legacy := current
	legacy.V, legacy.Iat = legacyGrantVersion, 0
	payload, _ := json.Marshal(legacy)
	key, _ := grants.key()
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(grantMAC(key, payload))

	claims, err := grants.Verify(token)
	if err != nil {
		t.Fatalf("legacy grant: %v", err)
	}
	if claims.V != grantVersion || claims.Iat != legacy.Exp-int64(GrantLifetime/time.Second) || claims.Revision() != current.Revision() {
		t.Fatalf("legacy grant read as %+v", claims)
	}
	renewed, err := grants.Renew(claims)
	if err != nil {
		t.Fatalf("renew a legacy grant: %v", err)
	}
	if again, err := grants.Verify(renewed); err != nil || again.V != grantVersion {
		t.Fatalf("renewed legacy grant: %+v %v", again, err)
	}
	// Another version, or a current one without its issue time, is refused.
	for _, bad := range []Grant{{V: 3}, {V: grantVersion}} {
		forged := current
		forged.V, forged.Iat = bad.V, 0
		payload, _ := json.Marshal(forged)
		token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(grantMAC(key, payload))
		if _, err := grants.Verify(token); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("version %d without an issue time: %v, want ErrInvalidGrant", forged.V, err)
		}
	}
}
