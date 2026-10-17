package workersource

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestTransientOpenMustRetainGrant(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{})
	s, e := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(filepath.Dir(path), filepath.Dir(path)+".unavailable"); e != nil {
		t.Fatal(e)
	}
	w := requestSource(a, s, "GET", "")
	if e = os.Rename(filepath.Dir(path)+".unavailable", filepath.Dir(path)); e != nil {
		t.Fatal(e)
	}
	_, recordErr := a.options.Store.Get(t.Context(), s.Grant)
	after := requestSource(a, s, "GET", "")
	t.Logf("temporarily inaccessible path: HTTP%d, recordMissing=%v, restored-path HTTP%d", w.Code, errors.Is(recordErr, ErrNotFound), after.Code)
	if w.Code != 503 || recordErr != nil || after.Code != 200 {
		t.Fatalf("transient path error permanently retired source: HTTP%d recordErr=%v restoredHTTP%d", w.Code, recordErr, after.Code)
	}
}
func TestEqualSizeRestoredMtimeStillChanged(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{})
	s, e := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if e != nil {
		t.Fatal(e)
	}
	initial, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path+".replacement", []byte("abcdefghij"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Chtimes(path+".replacement", initial.ModTime(), initial.ModTime()); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(path+".replacement", path); e != nil {
		t.Fatal(e)
	}
	next, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	if next.Size() != initial.Size() || !next.ModTime().Equal(initial.ModTime()) {
		t.Fatal("fixture changed portable subset")
	}
	w := requestSource(a, s, "GET", "")
	t.Logf("same size=%d and restored mtime=%d; actual HTTP%d", next.Size(), next.ModTime().UnixNano(), w.Code)
	if w.Code != 410 {
		t.Fatalf("replacement accepted: HTTP%d", w.Code)
	}
}

func TestTransientAuthorityChecksWithholdWithoutDeletingGrant(t *testing.T) {
	for _, dependency := range []string{"work", "path", "proof", "renew"} {
		t.Run(dependency, func(t *testing.T) {
			a, path, _ := testAuthority(t, "0123456789", Limits{})
			source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
			if err != nil {
				t.Fatal(err)
			}
			unavailable := true
			switch dependency {
			case "work":
				a.options.ActiveStatus = func(context.Context, int, string, uint64) (bool, error) {
					if unavailable {
						return false, ErrUnavailable
					}
					return true, nil
				}
			case "path", "renew":
				a.options.PathApprover = func(context.Context, string, string) error {
					if unavailable {
						return ErrUnavailable
					}
					return nil
				}
			case "proof":
				a.options.VerifyRequest = func(*http.Request, int) error {
					if unavailable {
						return ErrUnavailable
					}
					return nil
				}
			}
			if dependency == "renew" {
				if _, err = a.Renew(t.Context(), 7, "work", 3, source.Grant); !errors.Is(err, ErrUnavailable) {
					t.Fatalf("renew unavailable=%v", err)
				}
			} else {
				w := requestSource(a, source, "GET", "")
				if w.Code != 503 || w.Header().Get("Retry-After") == "" {
					t.Fatalf("unknown HTTP%d", w.Code)
				}
			}
			if _, err = a.options.Store.Get(t.Context(), source.Grant); err != nil {
				t.Fatalf("unknown deleted record: %v", err)
			}
			unavailable = false
			if w := requestSource(a, source, "GET", ""); w.Code != 200 || w.Body.String() != "0123456789" {
				t.Fatalf("healthy recovery HTTP%d", w.Code)
			}
		})
	}
}

func TestRenewUnavailableOpenDoesNotExtendOrDeleteGrant(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	before, err := a.options.Store.Get(t.Context(), source.Grant)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Dir(path), filepath.Dir(path)+".offline"); err != nil {
		t.Fatal(err)
	}
	_, err = a.Renew(t.Context(), 7, "work", 3, source.Grant)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable open: %v", err)
	}
	after, err := a.options.Store.Get(t.Context(), source.Grant)
	if err != nil || !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatalf("unknown changed grant: %v", err)
	}
	if err = os.Rename(filepath.Dir(path)+".offline", filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Renew(t.Context(), 7, "work", 3, source.Grant); err != nil {
		t.Fatal(err)
	}
}

type unavailableDeleteStore struct{ Store }

func (s unavailableDeleteStore) Delete(context.Context, string) error { return ErrUnavailable }

func TestConfirmedMutationRemainsDefinitiveWhenCleanupUnavailable(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	a.options.Store = unavailableDeleteStore{a.options.Store}
	if err = os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := requestSource(a, source, "GET", ""); w.Code != 410 {
		t.Fatalf("known mutation became retryable HTTP%d", w.Code)
	}
	if _, err = a.Renew(t.Context(), 7, "work", 3, source.Grant); !errors.Is(err, ErrChanged) {
		t.Fatalf("known mutation became retryable renewal: %v", err)
	}
}
