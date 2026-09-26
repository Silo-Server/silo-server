package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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

func TestEmailVerificationTransport(t *testing.T) {
	f := &fakeEmailVerification{current: true}
	deps := pilotDeps(nil, nil)
	deps.NotificationEmailVerification = f
	h := NewHandler(deps)
	path := Prefix + "/notifications/email-preferences/address"
	r := do(t, h, http.MethodPut, path, emailVerificationBody, profileOwner())
	if r.Code != 200 || f.user != 1 || f.profile != "p-owner" || f.email != "address@example.test" || !strings.Contains(r.Body.String(), `"current":true`) || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s %+v", r.Code, r.Body.String(), f)
	}
	f.current = false
	r = do(t, h, http.MethodPut, path, emailVerificationBody, profileOwner())
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"current":false`) {
		t.Fatal("inactive receipt", r.Code)
	}
	before := f.calls
	r = do(t, h, http.MethodPut, path, emailVerificationBody, bearer(memberToken))
	if r.Code != 422 || f.calls != before {
		t.Fatal("profileless", r.Code)
	}
	for _, body := range []string{`{}`, `{"verification_id":"not-uuid","email":"a"}`, `{"verification_id":"00000000-0000-4000-8000-000000000001","email":""}`} {
		r = do(t, h, http.MethodPut, path, body, profileOwner())
		if r.Code != 422 || f.calls != before {
			t.Fatalf("validation=%d %s", r.Code, r.Body.String())
		}
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{notifications.ErrEmailChildProfile, 403}, {notifications.ErrEmailInvalidAddress, 422}, {notifications.ErrEmailVerificationConflict, 409}, {notifications.ErrEmailAddressInUse, 409}, {notifications.ErrEmailNoLinkBase, 409}, {notifications.ErrEmailVerifyRateLimited, 429}, {notifications.ErrEmailVerificationUnavailable, 503}} {
		f.err = tc.err
		r = do(t, h, http.MethodPut, path, emailVerificationBody, profileOwner())
		if r.Code != tc.status {
			t.Fatalf("%v=%d", tc.err, r.Code)
		}
	}
	r = do(t, h, http.MethodGet, path+"/capabilities", "", profileOwner())
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"queue_available":true`) || !strings.Contains(r.Body.String(), `"dispatch_available":false`) {
		t.Fatal("capability conflated queue/dispatch", r.Code)
	}
	f.dispatch = true
	r = do(t, h, http.MethodGet, path+"/capabilities", "", profileOwner())
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"dispatch_available":true`) {
		t.Fatal("capability hides dispatch", r.Code, r.Body.String())
	}
	deps.NotificationEmailVerification = nil
	h = NewHandler(deps)
	r = do(t, h, http.MethodPut, path, emailVerificationBody, profileOwner())
	if r.Code != 503 {
		t.Fatal("absent", r.Code)
	}
}

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
