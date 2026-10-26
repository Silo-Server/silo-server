package apiv2

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type autoscanDeliveryFixture struct {
	calls int
	body  string
	fail  error
}

func (f *autoscanDeliveryFixture) DeliverAutoscanWebhook(_ http.ResponseWriter, r *http.Request, token string) error {
	if token != "synthetic-capability" {
		return &handlers.AutoscanDeliveryFailure{Status: 404, Message: "Not found"}
	}
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	data, err := io.ReadAll(r.Body)
	f.body = string(data)
	return err
}
func TestAutoscanDeliveryRejectsBeforeBodyAndPreservesBucket(t *testing.T) {
	f := new(autoscanDeliveryFixture)
	h := NewHandler(Dependencies{AutoscanDelivery: f})
	unread := &ingressUnreadBody{}
	r := httptest.NewRequest("POST", Prefix+"/autoscan/webhooks/unknown", unread)
	r.Header.Set("Content-Type", mediaTypeJSON)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireProblem(t, rec, TypeNotFound)
	if unread.read || f.calls != 0 {
		t.Fatal("pre-read provider bytes before capability resolution")
	}
	r = httptest.NewRequest("POST", Prefix+"/autoscan/webhooks/synthetic-capability", unread)
	r.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireProblem(t, rec, TypeUnsupportedMediaType)
	bucket := ""
	deps := Dependencies{AutoscanDelivery: f, BucketRateLimit: func(name string) func(http.Handler) http.Handler {
		bucket = name
		return func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(429)
				_, _ = w.Write([]byte(`{"error":"rate_limit_exceeded","message":"Too many requests."}`))
			})
		}
	}}
	rec = do(t, NewHandler(deps), "POST", Prefix+"/autoscan/webhooks/synthetic-capability", `{}`, nil)
	requireProblem(t, rec, TypeRateLimited)
	if bucket != "autoscan_webhook" || f.calls != 0 || rec.Header().Get("Retry-After") != "3" {
		t.Fatal(bucket, f.calls, rec.Header())
	}
}
