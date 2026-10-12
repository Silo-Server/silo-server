package nodeenrollment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const testToken = "provider-test-token-only-01234567890123456789"

func testBinding() Binding {
	return Binding{DatabaseURL: "postgres://customer.invalid/silo", RedisURL: "redis://customer.invalid:6379/2", SecretKey: strings.Repeat("k", 48), NodeURL: "https://node.example", NodeName: "Sold node"}
}
func bindingStore(t *testing.T) Store {
	t.Helper()
	return Store{Path: filepath.Join(t.TempDir(), "private", "binding.json")}
}
func requestBinding(t *testing.T, s *Server, token string, b Binding) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, EnrollmentPath, strings.NewReader(string(data)))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestBindingPersistsPrivatelyAndCannotBeReplaced(t *testing.T) {
	store := bindingStore(t)
	b := testBinding()
	if err := store.Save(b); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got == nil || *got != b {
		t.Fatalf("binding did not survive reload: %+v %v", got, err)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("secret file permissions: %v", info.Mode())
	}
	other := b
	other.SecretKey = strings.Repeat("z", 48)
	if err := store.Save(other); !errors.Is(err, ErrAlreadyBound) {
		t.Fatalf("binding replacement allowed: %v", err)
	}
	if Identity(b.SecretKey) == Identity(other.SecretKey) {
		t.Fatal("different customers share enrollment identity")
	}
}
func TestBindingRejectsCorruptOrPublicFiles(t *testing.T) {
	store := bindingStore(t)
	if err := store.Save(testBinding()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("public secret file accepted")
	}
	if err := os.WriteFile(store.Path, []byte(`{"secret_key":"incomplete"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("corrupt binding accepted")
	}
}
func TestCompetingStoresHaveOneDurableWinner(t *testing.T) {
	store := bindingStore(t)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := store.Save(testBinding()); err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrAlreadyBound) {
				t.Errorf("save: %v", err)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("%d bindings won", winners.Load())
	}
}
func TestEnrollmentAuthenticatesBeforeValidationAndCanRetry(t *testing.T) {
	var calls int
	fail := true
	store := bindingStore(t)
	s, err := NewServer(testToken, store, func(context.Context, Binding) error {
		calls++
		if fail {
			return errors.New("secret DSN must never escape")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := requestBinding(t, s, "wrong", testBinding()); w.Code != 401 || calls != 0 {
		t.Fatalf("wrong token admitted: %d calls=%d", w.Code, calls)
	}
	if w := requestBinding(t, s, testToken, testBinding()); w.Code != 422 || strings.Contains(w.Body.String(), "secret DSN") {
		t.Fatalf("validation failure leaked or bound: %d %s", w.Code, w.Body.String())
	}
	if b, err := store.Load(); err != nil || b != nil {
		t.Fatalf("failed enrollment persisted: %+v %v", b, err)
	}
	fail = false
	w := requestBinding(t, s, testToken, testBinding())
	if w.Code != 200 {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), testBinding().SecretKey) || strings.Contains(w.Body.String(), "postgres") {
		t.Fatal("credentials echoed")
	}
	select {
	case b := <-s.Done():
		if b != testBinding() {
			t.Fatal("startup got different credentials")
		}
	default:
		t.Fatal("bound worker not released to start")
	}
	if w := requestBinding(t, s, testToken, testBinding()); w.Code != 409 {
		t.Fatalf("used enrollment token accepted: %d", w.Code)
	}
}
func TestEnrollmentCannotSetProviderCapacity(t *testing.T) {
	s, err := NewServer(testToken, bindingStore(t), func(context.Context, Binding) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(testBinding())
	body := strings.TrimSuffix(string(data), "}") + `,"max_jobs":999}`
	r := httptest.NewRequest("POST", EnrollmentPath, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("customer capacity override accepted: %d %s", w.Code, w.Body.String())
	}
}
