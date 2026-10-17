package workerregistration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

type testEnrollmentRepository struct {
	mu       sync.Mutex
	ticket   Enrollment
	public   ed25519.PublicKey
	calls    int
	consumed bool
}

func (r *testEnrollmentRepository) Redeem(_ context.Context, e Enrollment, key ed25519.PublicKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if enrollmentDigest(e) != enrollmentDigest(r.ticket) || r.consumed {
		return errors.New("ticket rejected")
	}
	r.consumed = true
	r.public = append(ed25519.PublicKey(nil), key...)
	return nil
}

func enrollmentFixture(t *testing.T) (Enrollment, ed25519.PrivateKey, ed25519.PrivateKey, *testEnrollmentRepository, *httptest.Server) {
	t.Helper()
	corePublic, corePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, nodePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler
	var requestBytes, responseBytes []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBytes = body
		r.Body = io.NopCloser(bytes.NewReader(body))
		record := httptest.NewRecorder()
		handler.ServeHTTP(record, r)
		responseBytes = append([]byte(nil), record.Body.Bytes()...)
		for key, values := range record.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(record.Code)
		_, _ = w.Write(responseBytes)
	}))
	t.Cleanup(server.Close)
	e := Enrollment{Version: 1, NodeID: 17, CoreURL: server.URL, CorePublicKey: corePublic, NodeURL: "http://worker.example.test", AllowHTTP: true, Ticket: strings.Repeat("12", 32), ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
	repo := &testEnrollmentRepository{ticket: e}
	handler, err = NewHandler(Options{Repository: repo, Origin: server.URL, PrivateKey: corePrivate, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, secret := range [][]byte{[]byte(base64.StdEncoding.EncodeToString(corePrivate)), []byte(base64.StdEncoding.EncodeToString(nodePrivate))} {
			if bytes.Contains(requestBytes, secret) || bytes.Contains(responseBytes, secret) {
				t.Error("enrollment sent private key")
			}
		}
	})
	return e, corePrivate, nodePrivate, repo, server
}

func TestEnrollProvesNodeKeyAndReturnsOnlyPublicBinding(t *testing.T) {
	e, _, key, repo, _ := enrollmentFixture(t)
	binding, err := Enroll(t.Context(), e, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if binding.NodeID != 17 || binding.CoreURL != e.CoreURL || binding.NodeURL != e.NodeURL || !bytes.Equal(binding.NodePrivateKey, key) || !bytes.Equal(repo.public, key.Public().(ed25519.PublicKey)) {
		t.Fatal("binding/key ownership changed during enrollment")
	}
	if _, err = Enroll(t.Context(), e, key, nil); err == nil {
		t.Fatal("lost-response retry consumed ticket again")
	}
}

func TestEnrollmentRequiresProofBeforeTicketConsumption(t *testing.T) {
	e, _, key, repo, _ := enrollmentFixture(t)
	body, _ := json.Marshal(enrollmentRequest{Enrollment: e, NodePublicKey: key.Public().(ed25519.PublicKey)})
	request, _ := http.NewRequest(http.MethodPost, e.CoreURL+enrollmentPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response, err := workeridentity.NewHTTPClient(true, time.Second).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || repo.calls != 0 {
		t.Fatal("unproved key consumed enrollment ticket")
	}
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	request, _ = http.NewRequest(http.MethodPost, e.CoreURL+enrollmentPath, bytes.NewReader(body))
	if err = workeridentity.Sign(request, e.CoreURL, domainEnrollmentRequest, otherKey, 17, true); err != nil {
		t.Fatal(err)
	}
	response, err = workeridentity.NewHTTPClient(true, time.Second).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || repo.calls != 0 {
		t.Fatal("unowned node key consumed enrollment ticket")
	}
	if _, err = Enroll(t.Context(), e, key, nil); err != nil {
		t.Fatal("unproved attempt consumed ticket")
	}
}

func TestEnrollmentRejectsAlteredCoreContextBeforeConsume(t *testing.T) {
	e, _, key, repo, _ := enrollmentFixture(t)
	changed := e
	changed.AllowHTTP = false
	if _, err := Enroll(t.Context(), changed, key, nil); err == nil {
		t.Fatal("HTTP used without explicit ticket allowance")
	}
	changed = e
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	changed.CorePublicKey = pub
	if _, err := Enroll(t.Context(), changed, key, nil); err == nil {
		t.Fatal("wrong expected core public key accepted")
	}
	if repo.calls != 0 {
		t.Fatal("altered core context consumed ticket")
	}
}

func TestEnrollmentTicketStrictProtectedPersistence(t *testing.T) {
	e, _, _, _, _ := enrollmentFixture(t)
	path := t.TempDir() + "/ticket.json"
	if err := SaveEnrollment(path, e); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEnrollment(path)
	if err != nil || enrollmentDigest(loaded) != enrollmentDigest(e) {
		t.Fatal("ticket persistence changed context")
	}
	bad, _ := json.Marshal(e)
	bad = append(bytes.TrimSuffix(bad, []byte("}")), []byte(`,"database_url":"legacy"}`)...)
	if _, err := DecodeEnrollment(bytes.NewReader(bad)); err == nil {
		t.Fatal("legacy credentials accepted in ticket")
	}
	if _, err := DecodeEnrollment(strings.NewReader(string(mustJSON(t, e)) + "{}")); err == nil {
		t.Fatal("trailing ticket JSON accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnrollment(path); err == nil {
		t.Fatal("publicly readable ticket accepted")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEnrollmentReplyContainsOnlyPublicBindingFields(t *testing.T) {
	e, coreKey, nodeKey, _, _ := enrollmentFixture(t)
	data := mustJSON(t, enrollmentRequest{Enrollment: e, NodePublicKey: nodeKey.Public().(ed25519.PublicKey)})
	req, err := http.NewRequest(http.MethodPost, e.CoreURL+enrollmentPath, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err = workeridentity.Sign(req, e.CoreURL, domainEnrollmentRequest, nodeKey, e.NodeID, true); err != nil {
		t.Fatal(err)
	}
	response, err := workeridentity.NewHTTPClient(true, time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("enrollment status=%d", response.StatusCode)
	}
	var envelope struct {
		Grant string `json:"grant"`
	}
	if err = json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	grant, err := workeridentity.DecodeGrant(envelope.Grant, domainBindingGrant, e.NodeURL, e.CorePublicKey, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(grant.Body, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"version": true, "node_id": true, "core_url": true, "core_public_key": true, "node_url": true, "node_public_key": true, "allow_http": true}
	if len(fields) != len(allowed) {
		t.Fatal("binding reply contains unexpected fields")
	}
	for field := range fields {
		if !allowed[field] {
			t.Fatal("binding reply contains a private or legacy field")
		}
	}
	for _, key := range []ed25519.PrivateKey{coreKey, nodeKey} {
		if bytes.Contains(grant.Body, []byte(base64.StdEncoding.EncodeToString(key))) {
			t.Fatal("signed payload disclosed a private key")
		}
	}
}

func TestHTTPEnrollmentConcurrentConsumeUsesSQLAuthority(t *testing.T) {
	repo := registrationDB(t)
	coreKey, err := repo.SigningKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer server.Close()
	e, err := repo.Issue(t.Context(), 17, server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	handler, err = NewHandler(Options{Repository: repo, Origin: server.URL, PrivateKey: coreKey, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	_, nodeKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Enroll(t.Context(), e, nodeKey, nil); err == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("HTTP ticket accepted %d times", accepted.Load())
	}
	node, found, err := repo.ByID(t.Context(), 17)
	if err != nil || !found || !bytes.Equal(node.PublicKey, nodeKey.Public().(ed25519.PublicKey)) {
		t.Fatal("HTTP enrollment did not install proved public key")
	}
}

func TestHTTPSEnrollmentRequiresTrustAndVerifiesSignedBinding(t *testing.T) {
	repo := registrationDB(t)
	coreKey, err := repo.SigningKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer server.Close()
	e, err := repo.Issue(t.Context(), 17, server.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	handler, err = NewHandler(Options{Repository: repo, Origin: server.URL, PrivateKey: coreKey})
	if err != nil {
		t.Fatal(err)
	}
	_, nodeKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Enroll(t.Context(), e, nodeKey, nil); err == nil {
		t.Fatal("untrusted core TLS accepted")
	}
	client := server.Client()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS fixture disabled verification")
	}
	if _, err = Enroll(t.Context(), e, nodeKey, client); err != nil {
		t.Fatal(err)
	}
}

func TestVerifiedHTTPSCoreEnrollsExplicitHTTPWorker(t *testing.T) {
	repo := registrationDB(t)
	if _, err := repo.pool.Exec(t.Context(), "UPDATE stream_nodes SET url='http://worker.example.test' WHERE id=17"); err != nil {
		t.Fatal(err)
	}
	coreKey, err := repo.SigningKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer server.Close()
	if _, err = repo.Issue(t.Context(), 17, server.URL, false); err == nil {
		t.Fatal("HTTP worker received ticket without operator opt-in")
	}
	e, err := repo.Issue(t.Context(), 17, server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	handler, err = NewHandler(Options{Repository: repo, Origin: server.URL, PrivateKey: coreKey})
	if err != nil {
		t.Fatal(err)
	}
	_, nodeKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Enroll(t.Context(), e, nodeKey, nil); err == nil {
		t.Fatal("HTTP node opt-in bypassed core TLS verification")
	}
	client := server.Client()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS fixture disables verification")
	}
	binding, err := Enroll(t.Context(), e, nodeKey, client)
	if err != nil {
		t.Fatal(err)
	}
	if !binding.AllowHTTP || binding.CoreURL != server.URL || binding.NodeURL != "http://worker.example.test" {
		t.Fatal("explicit HTTP worker policy missing from signed binding")
	}
	node, ok, err := repo.ByID(t.Context(), 17)
	if err != nil || !ok || !node.AllowHTTP {
		t.Fatal("per-node HTTP policy did not survive persistence")
	}
	// The repository's full ticket digest prevents policy changes even when both origins use TLS.
	if _, err = repo.pool.Exec(t.Context(), "UPDATE stream_nodes SET url='https://worker.example.test' WHERE id=17"); err != nil {
		t.Fatal(err)
	}
	strict, err := repo.Issue(t.Context(), 17, server.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	changed := strict
	changed.AllowHTTP = true
	if _, err = Enroll(t.Context(), changed, nodeKey, client); err == nil {
		t.Fatal("changed operator HTTP policy was accepted")
	}
	if _, err = Enroll(t.Context(), strict, nodeKey, client); err != nil {
		t.Fatal("changed policy consumed strict ticket")
	}
	node, ok, err = repo.ByID(t.Context(), 17)
	if err != nil || !ok || node.AllowHTTP {
		t.Fatal("re-enrollment did not restore strict HTTPS policy")
	}
}
