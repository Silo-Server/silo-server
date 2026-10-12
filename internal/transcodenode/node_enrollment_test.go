package transcodenode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
