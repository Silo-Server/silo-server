package nodesource

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const testGrantSecret = "grant-test-secret"

func testGrants() *Grants { return NewGrants(func() string { return testGrantSecret }) }

func writeSource(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGrantRoundTripCarriesTheStatRevision(t *testing.T) {
	path := writeSource(t, "source bytes")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	grants := testGrants()
	token, err := grants.ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := grants.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	want := Grant{V: 1, Node: 7, Kind: KindFile, ID: 42, Path: path, Size: info.Size(), Mtime: info.ModTime().Unix(), Exp: claims.Exp}
	if claims != want {
		t.Fatalf("claims = %+v, want %+v", claims, want)
	}
	if got, want := claims.ETag(), fmt.Sprintf(`"file-42-%d-%d"`, info.Size(), info.ModTime().Unix()); got != want {
		t.Fatalf("etag = %s, want %s", got, want)
	}
	if until := time.Until(claims.Expires()); until <= 59*time.Minute || until > time.Hour {
		t.Fatalf("grant lasts %v, want an hour", until)
	}
	if peeked, err := peek(token); err != nil || peeked != claims {
		t.Fatalf("peek = %+v, %v", peeked, err)
	}
}

func TestGrantVerifyRefusesTamperingExpiryAndOtherKeys(t *testing.T) {
	path := writeSource(t, "source")
	grants := testGrants()
	token, err := grants.ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	payload, mac, _ := strings.Cut(token, ".")
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	otherNode := strings.Replace(string(decoded), `"node":7`, `"node":8`, 1)
	tampered := base64.RawURLEncoding.EncodeToString([]byte(otherNode)) + "." + mac
	for name, candidate := range map[string]string{
		"tampered payload": tampered,
		"truncated mac":    payload + "." + mac[:10],
		"no mac":           payload,
		"empty":            "",
		"garbage":          "not.a.grant",
	} {
		if _, err := grants.Verify(candidate); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: err = %v, want ErrInvalidGrant", name, err)
		}
	}
	if _, err := NewGrants(func() string { return "another secret" }).Verify(token); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("another key: err = %v", err)
	}
	if _, err := NewGrants(func() string { return "" }).Verify(token); !errors.Is(err, ErrNoGrantKey) {
		t.Fatalf("no key: err = %v", err)
	}

	expired := testGrants()
	expired.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	old, err := expired.ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Verify(old); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("expired: err = %v", err)
	}
}

func TestGrantMintRefusesWhatIsNotARegularCatalogFile(t *testing.T) {
	grants := testGrants()
	dir := t.TempDir()
	for name, file := range map[string]*models.MediaFile{
		"missing":   {ID: 1, FilePath: filepath.Join(dir, "missing.mkv")},
		"directory": {ID: 1, FilePath: dir},
	} {
		if _, err := grants.ForFile(t.Context(), 7, file); !errors.Is(err, ErrSourceGone) {
			t.Errorf("%s: err = %v, want ErrSourceGone", name, err)
		}
	}
	for name, file := range map[string]*models.MediaFile{
		"relative": {ID: 1, FilePath: "movie.mkv"},
		"no id":    {FilePath: writeSource(t, "x")},
		"nil":      nil,
	} {
		if _, err := grants.ForFile(t.Context(), 7, file); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: err = %v, want ErrInvalidGrant", name, err)
		}
	}
	if _, err := grants.ForFile(t.Context(), 0, &models.MediaFile{ID: 1, FilePath: writeSource(t, "x")}); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("no node: err = %v", err)
	}
}

// The revision names one version of one catalog file wherever it is
// mounted: two paths to the same file, as two API replicas mount a shared
// library, grant the same revision and ETag.
func TestGrantRevisionIsIndependentOfThePath(t *testing.T) {
	path := writeSource(t, "shared library file")
	other := filepath.Join(t.TempDir(), "elsewhere.mkv")
	if err := os.Link(path, other); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}
	grants := testGrants()
	first, err := grants.ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	second, err := grants.ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: other})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := grants.Verify(first)
	b, _ := grants.Verify(second)
	if a.Path == b.Path || a.Revision() != b.Revision() || a.ETag() != b.ETag() {
		t.Fatalf("revisions differ: %+v vs %+v", a, b)
	}
}

func TestGrantRenewKeepsTheRevision(t *testing.T) {
	path := writeSource(t, "source")
	grants := testGrants()
	token, err := grants.ForTheme(t.Context(), 7, 9, path)
	if err != nil {
		t.Fatal(err)
	}
	claims, _ := grants.Verify(token)
	grants.now = func() time.Time { return time.Now().Add(30 * time.Minute) }
	renewed, err := grants.Renew(claims)
	if err != nil {
		t.Fatal(err)
	}
	again, err := grants.Verify(renewed)
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision() != claims.Revision() || again.Path != claims.Path || again.Node != 7 || again.Kind != KindTheme {
		t.Fatalf("renewed %+v from %+v", again, claims)
	}
	if again.Exp <= claims.Exp {
		t.Fatalf("renewal did not extend the grant: %d <= %d", again.Exp, claims.Exp)
	}
}

func TestGrantCheckAndOpenCompareTheRevision(t *testing.T) {
	path := writeSource(t, "source")
	grants := testGrants()
	token, _ := grants.ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: path})
	claims, _ := grants.Verify(token)
	if _, err := claims.Check(); err != nil {
		t.Fatal(err)
	}
	f, _, err := claims.Open()
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if err := os.WriteFile(path, []byte("replaced source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := claims.Check(); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("check after replace: %v", err)
	}
	if _, _, err := claims.Open(); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("open after replace: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := claims.Check(); !errors.Is(err, ErrSourceGone) {
		t.Fatalf("check after remove: %v", err)
	}
	if _, _, err := claims.Open(); !errors.Is(err, ErrSourceGone) {
		t.Fatalf("open after remove: %v", err)
	}
}

func TestGrantFileWithoutAGranterMintsNothing(t *testing.T) {
	var none *Grants
	for _, granter := range []FileGranter{nil, none} {
		if grant, err := GrantFile(t.Context(), granter, 7, &models.MediaFile{ID: 1, FilePath: "/x"}); grant != "" || err != nil {
			t.Fatalf("grant = %q, err = %v", grant, err)
		}
	}
}

// A mount that reports the modification time at another precision sees the
// same revision; another second is another revision.
func TestGrantRevisionIsPreciseToTheSecond(t *testing.T) {
	path := writeSource(t, "source")
	minted := time.Date(2026, 5, 4, 3, 2, 1, 999_999_999, time.UTC)
	if err := os.Chtimes(path, minted, minted); err != nil {
		t.Fatal(err)
	}
	grants := testGrants()
	token, err := grants.ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	claims, _ := grants.Verify(token)
	if claims.Mtime != minted.Unix() || claims.ETag() != fmt.Sprintf(`"file-42-6-%d"`, minted.Unix()) {
		t.Fatalf("claims %+v, etag %s", claims, claims.ETag())
	}
	for _, seen := range []time.Time{minted.Truncate(time.Second), minted.Truncate(time.Millisecond), minted.Truncate(100 * time.Nanosecond)} {
		if err := os.Chtimes(path, seen, seen); err != nil {
			t.Fatal(err)
		}
		if _, err := claims.Check(); err != nil {
			t.Fatalf("check at %v: %v", seen, err)
		}
		f, _, err := claims.Open()
		if err != nil {
			t.Fatalf("open at %v: %v", seen, err)
		}
		_ = f.Close()
	}
	later := minted.Add(time.Nanosecond)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := claims.Check(); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("check in the next second: %v", err)
	}
}

// A grant too long to be presented back is never signed.
func TestGrantSignRefusesAnOversizedGrant(t *testing.T) {
	claims := Grant{V: 1, Node: 7, Kind: KindFile, ID: 42, Path: "/" + strings.Repeat("a", maxGrantBytes), Size: 1, Mtime: 1}
	if token, err := testGrants().Renew(claims); !errors.Is(err, ErrInvalidGrant) || token != "" {
		t.Fatalf("oversized grant: %d bytes, %v", len(token), err)
	}
}
