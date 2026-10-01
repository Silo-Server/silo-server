package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type fakeAdminTerminate struct {
	available bool
	calls     []handlers.AdminTerminateInput
	view      handlers.AdminTerminateView
	err       error
}

func (f *fakeAdminTerminate) AdminTerminateAvailable() bool { return f.available }
func (f *fakeAdminTerminate) Terminate(_ context.Context, in handlers.AdminTerminateInput) (handlers.AdminTerminateView, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return handlers.AdminTerminateView{}, f.err
	}
	view := f.view
	view.SessionID = in.SessionID
	return view, nil
}

func TestAdminTerminateTransport(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminTerminate{available: true, view: handlers.AdminTerminateView{AuthorityRevoked: true, DurableState: handlers.AdminTerminateDurableStopped, ClientNotified: false, Delivery: handlers.AdminTerminateDeliveryUnavailable, CommandID: "3fa85f64-5717-4562-b3fc-2c963f66afa6"}}
	deps.AdminPlaybackTerminate = f
	h := NewHandler(deps)
	path := adminCommandRoot + "/terminate"
	admin := with(bearer(adminToken), "X-Profile-Id", "p-primary")

	requireProblem(t, do(t, h, http.MethodPost, path, `{}`, bearer(memberToken)), TypePermissionDenied)
	if len(f.calls) != 0 {
		t.Fatal("member reached the seam")
	}

	// Client offline: 200 with authority_revoked true and client_notified false.
	rec := do(t, h, http.MethodPost, path, `{"reason":"policy"}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"session_id":"session-7"`, `"authority_revoked":true`, `"already_revoked":false`, `"durable_state":"stopped"`, `"client_notified":false`, `"delivery":"unavailable"`, `"command_id":"3fa85f64-5717-4562-b3fc-2c963f66afa6"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %s: %s", want, body)
		}
	}
	call := f.calls[len(f.calls)-1]
	if call.SessionID != "session-7" || call.ActorID != 2 || call.Reason != "policy" {
		t.Fatalf("call = %+v", call)
	}

	// The body fields are optional but the JSON body itself is required, as
	// on every v2 operation with a declared body: a body-less POST is 415.
	if rec := do(t, h, http.MethodPost, path, ``, admin); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// Client online: both facts true.
	f.view.ClientNotified, f.view.Delivery = true, handlers.AdminTerminateDeliveryDispatched
	rec = do(t, h, http.MethodPost, path, `{}`, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"client_notified":true`) || !strings.Contains(rec.Body.String(), `"delivery":"dispatched"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// Repeat: converged.
	f.view = handlers.AdminTerminateView{AuthorityRevoked: true, AlreadyRevoked: true, DurableState: handlers.AdminTerminateDurableStopped, Delivery: handlers.AdminTerminateDeliveryNone}
	rec = do(t, h, http.MethodPost, path, `{}`, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"already_revoked":true`) || !strings.Contains(rec.Body.String(), `"delivery":"none"`) || strings.Contains(rec.Body.String(), `"command_id"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// Unknown field is refused before the seam.
	before := len(f.calls)
	if rec := do(t, h, http.MethodPost, path, `{"reason":"x","force":true}`, admin); rec.Code != http.StatusUnprocessableEntity {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if len(f.calls) != before {
		t.Fatal("invalid body reached the seam")
	}

	for _, mapping := range []struct {
		err  error
		want ProblemType
	}{
		{playback.ErrSessionNotFound, TypeNotFound},
		{handlers.ErrAdminPlaybackCommandInvalid, TypeValidationFailed},
		{handlers.ErrAdminTerminateUnavailable, TypeDependencyUnavailable},
		{&handlers.PlaybackOperationError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Playback authority is temporarily unavailable"}, TypeConflict},
	} {
		f.err = mapping.err
		requireProblem(t, do(t, h, http.MethodPost, path, `{}`, admin), mapping.want)
	}
	f.err = nil

	demo := pilotDeps(nil, nil)
	demo.DemoSettings = fakeSettings{demo: true}
	demo.AdminPlaybackTerminate = &fakeAdminTerminate{available: true}
	requireProblem(t, do(t, NewHandler(demo), http.MethodPost, path, `{}`, with(bearer(otherAdminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
}

func TestAdminTerminateCapabilityAndAbsence(t *testing.T) {
	capability := Prefix + "/admin/sessions/command-capabilities"
	admin := with(bearer(adminToken), "X-Profile-Id", "p-primary")
	deps := pilotDeps(nil, nil)
	deps.AdminPlaybackCommands = &fakeAdminPlaybackCommands{available: true}
	rec := do(t, NewHandler(deps), http.MethodGet, capability, "", admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"actions":["pause","resume","stop","message"]`) || !strings.Contains(rec.Body.String(), `"terminate_revokes_authority":false`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, adminCommandRoot+"/terminate", `{}`, admin), TypeDependencyUnavailable)
	deps.AdminPlaybackTerminate = &fakeAdminTerminate{available: false}
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, adminCommandRoot+"/terminate", `{}`, admin), TypeDependencyUnavailable)
	rec = do(t, NewHandler(deps), http.MethodGet, capability, "", admin)
	if !strings.Contains(rec.Body.String(), `"terminate_revokes_authority":false`) || strings.Contains(rec.Body.String(), `"terminate"`) {
		t.Fatal(rec.Body.String())
	}
	deps.AdminPlaybackTerminate = &fakeAdminTerminate{available: true}
	rec = do(t, NewHandler(deps), http.MethodGet, capability, "", admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"actions":["pause","resume","stop","message","terminate"]`) || !strings.Contains(rec.Body.String(), `"terminate_revokes_authority":true`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
