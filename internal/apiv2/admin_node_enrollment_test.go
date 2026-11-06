package apiv2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeenrollment"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type fakeNodeEnrollment struct {
	calls   int
	input   nodepool.CreateNodeInput
	handoff nodeenrollment.Input
	err     error
}

func (f *fakeNodeEnrollment) EnrollAdminNode(_ context.Context, in nodepool.CreateNodeInput, handoff nodeenrollment.Input) (*nodepool.Node, error) {
	f.calls++
	f.input = in
	f.handoff = handoff
	if f.err != nil {
		return nil, f.err
	}
	return &nodepool.Node{ID: 17, AdminRevision: 1, Name: in.Name, Type: in.Type, URL: in.URL, CreatedAt: time.Unix(1, 0)}, nil
}
func TestAdminNodeEnrollmentAuthorityAndSafeReceipt(t *testing.T) {
	f := &fakeNodeEnrollment{}
	deps := pilotDeps(nil, nil)
	deps.AdminNodeEnrollment = f
	h := NewHandler(deps)
	path := Prefix + "/admin/nodes/enroll"
	body := `{"name":"Sold node","type":"transcode","url":"http://node.internal:8082","enrollment_token":"provider-enrollment-token-32-chars","worker_database_url":"postgres://worker:password@db.internal/silo","worker_redis_url":"redis://cache.internal:6379/0","max_jobs":12}`
	requireProblem(t, do(t, h, "POST", path, body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "POST", path, body, bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized handoff")
	}
	rec := do(t, h, "POST", path, body, bearer(adminToken))
	if rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var node AdminNode
	if err := json.Unmarshal(rec.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if node.ConfigETag == "" || node.ConfigETag != rec.Header().Get("ETag") || f.handoff.Token != "provider-enrollment-token-32-chars" || f.handoff.RedisURL != "redis://cache.internal:6379/0" || f.input.Type != "transcode" {
		t.Fatal("handoff or receipt missing")
	}
	for _, s := range []string{"provider-enrollment", "password", "worker_database_url", "worker_redis_url"} {
		if strings.Contains(rec.Body.String(), s) {
			t.Fatal("credential echoed")
		}
	}
	rec = do(t, h, "POST", path, strings.Replace(body, `"transcode"`, `"proxy"`, 1), bearer(adminToken))
	if rec.Code != 422 || f.calls != 1 {
		t.Fatal("proxy enrollment accepted", rec.Code, f.calls)
	}
	f.err = nodeenrollment.ErrStartupPending
	rec = do(t, h, "POST", path, body, bearer(adminToken))
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
		t.Fatal("pending startup not retryable", rec.Code, rec.Body.String())
	}
	f.err = nodeenrollment.ErrOtherCustomer
	rec = do(t, h, "POST", path, body, bearer(adminToken))
	if rec.Code != 409 {
		t.Fatal("other customer did not conflict", rec.Code)
	}
}
