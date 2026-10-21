package transcodenode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeenrollment"
)

func TestBoundEnrollmentReceiptRequiresCustomerBearer(t *testing.T) {
	s := newTestServer(t)
	id := nodeenrollment.Identity("customer-master-test-key-only-01234567890123456789")
	s.SetEnrollmentIdentity(id)
	for _, authenticated := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, nodeenrollment.ReceiptPath, nil)
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+testSecret)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if !authenticated {
			if rec.Code != 401 {
				t.Fatalf("receipt exposed without customer bearer: %d", rec.Code)
			}
			continue
		}
		var receipt nodeenrollment.Receipt
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &receipt) != nil || receipt.BindingID != id {
			t.Fatalf("binding receipt unavailable: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestBoundEnrollmentReceiptAcceptsSignedCustomerRequest(t *testing.T) {
	s := newTestServer(t)
	id := nodeenrollment.Identity("customer-master-test-key-only-01234567890123456789")
	s.SetEnrollmentIdentity(id)
	for _, tc := range []struct {
		name, key string
		status    int
	}{
		{"customer", testSecret, http.StatusOK},
		{"other customer", "other-customer-jwt-secret", http.StatusUnauthorized},
		{"no key", "", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, nodeenrollment.ReceiptPath, nil)
			if err := nodeauth.Sign(req, "http://worker", tc.key); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("receipt status = %d, want %d", rec.Code, tc.status)
			}
			if tc.status == http.StatusOK {
				var receipt nodeenrollment.Receipt
				if json.Unmarshal(rec.Body.Bytes(), &receipt) != nil || receipt.BindingID != id {
					t.Fatal("signed receipt lost binding identity")
				}
			}
		})
	}
}

func TestBoundEnrollmentReceiptUsesReloadedJWTSecret(t *testing.T) {
	s := newTestServer(t)
	s.SetEnrollmentIdentity(nodeenrollment.Identity("customer-master-test-key-only-01234567890123456789"))
	check := func(key string, status int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, nodeenrollment.ReceiptPath, nil)
		if err := nodeauth.Sign(req, "http://worker", key); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("receipt status = %d, want %d", rec.Code, status)
		}
	}
	check(testSecret, http.StatusOK)
	replacement := *s.watcher.Config()
	replacement.Auth.JWTSecret = "reloaded-customer-jwt-secret"
	s.watcher.SetConfigForTest(&replacement)
	check(testSecret, http.StatusUnauthorized)
	check(replacement.Auth.JWTSecret, http.StatusOK)
}

func TestEnrollmentClientAuthenticatesBoundWorkerReceipt(t *testing.T) {
	const bindingKey = "customer-master-test-key-only-01234567890123456789"
	for _, prefix := range []string{"", "/worker-one"} {
		t.Run(prefix, func(t *testing.T) {
			s := newTestServer(t)
			s.SetEnrollmentIdentity(nodeenrollment.Identity(bindingKey))
			server := httptest.NewServer(http.StripPrefix(prefix, s.Handler()))
			defer server.Close()
			client := nodeenrollment.Client{Bootstrap: nodeenrollment.Binding{SecretKey: bindingKey}, JWTSecret: testSecret}
			receipt, err := client.Enroll(t.Context(), server.URL+prefix+"/", "node", nodeenrollment.Input{})
			if err != nil || receipt.BindingID != nodeenrollment.Identity(bindingKey) {
				t.Fatalf("bound worker receipt = %+v, %v", receipt, err)
			}
		})
	}
}
