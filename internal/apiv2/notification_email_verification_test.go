package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeEmailVerification struct {
	calls, user        int
	profile, id, email string
	err                error
	current, dispatch  bool
}

func (*fakeEmailVerification) EmailVerificationAvailable() bool              { return true }
func (f *fakeEmailVerification) EmailDispatchAvailable(context.Context) bool { return f.dispatch }
func (f *fakeEmailVerification) QueueEmailVerification(_ context.Context, user int, profile, id, email string) (notifications.EmailVerificationReceipt, error) {
	f.calls++
	f.user = user
	f.profile = profile
	f.id = id
	f.email = email
	return notifications.EmailVerificationReceipt{ID: id, ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Current: f.current}, f.err
}

const emailVerificationBody = `{"verification_id":"00000000-0000-4000-8000-000000000001","email":"address@example.test"}`

func (f *fakeEmailVerification) EmailVerificationAllowed(context.Context, int, string) bool {
	return !errors.Is(f.err, notifications.ErrEmailChildProfile)
}

func TestEmailVerificationCapabilityTracksDemoPermission(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		admin       bool
	}{
		{name: "member", token: memberToken},
		{name: "admin", token: adminToken, admin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeEmailVerification{current: true, dispatch: true}
			settings := &fakeSettings{}
			deps := pilotDeps(nil, nil)
			deps.NotificationEmailVerification = service
			deps.DemoSettings = settings
			h := newTestHandler(t, deps)
			path := Prefix + "/notifications/email-preferences/address"
			headers := with(bearer(tc.token), "X-Profile-Id", "p-owner")
			previousTag, previousRevision := "", ""
			previousAllowed := true
			for _, demo := range []bool{false, true, true, false} {
				settings.demo = demo
				allowed := !demo || tc.admin
				response := do(t, h, http.MethodGet, path+"/capabilities", "", headers)
				var body NotificationEmailVerificationCapability
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusOK || body.State != StateAvailable || !body.QueueAvailable || !body.DispatchAvailable || body.Allowed == nil || *body.Allowed != allowed {
					t.Fatalf("demo=%v: %d %s", demo, response.Code, response.Body.String())
				}
				tag := response.Header().Get("ETag")
				if tag == "" || body.Revision == "" {
					t.Fatal("capability lacks revision or ETag")
				}
				if previousTag != "" {
					changed := allowed != previousAllowed
					if (tag != previousTag) != changed || (body.Revision != previousRevision) != changed {
						t.Fatalf("demo=%v: capability identity did not track permission", demo)
					}
					cached := do(t, h, http.MethodGet, path+"/capabilities", "", with(headers, "If-None-Match", previousTag))
					wantStatus := http.StatusNotModified
					if changed {
						wantStatus = http.StatusOK
					}
					if cached.Code != wantStatus || cached.Header().Get("ETag") != tag {
						t.Fatalf("demo=%v: revalidation returned %d %s", demo, cached.Code, cached.Body.String())
					}
					if wantStatus == http.StatusNotModified && cached.Body.Len() != 0 {
						t.Fatal("unchanged capability returned a body")
					}
				}
				before := service.calls
				mutation := do(t, h, http.MethodPut, path, emailVerificationBody, headers)
				if allowed {
					if mutation.Code != http.StatusOK || service.calls != before+1 {
						t.Fatalf("demo=%v: permitted mutation returned %d", demo, mutation.Code)
					}
				} else {
					requireProblem(t, mutation, TypePermissionDenied)
					if service.calls != before {
						t.Fatal("demo-restricted mutation reached the service")
					}
				}
				previousTag, previousRevision, previousAllowed = tag, body.Revision, allowed
			}
		})
	}
}
