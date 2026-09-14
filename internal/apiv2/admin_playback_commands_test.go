package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type fakeAdminPlaybackCommands struct {
	available bool
	calls     []handlers.AdminPlaybackCommandInput
	view      handlers.AdminPlaybackCommandView
	err       error
}

func (f *fakeAdminPlaybackCommands) AdminPlaybackCommandsAvailable() bool { return f.available }
func (f *fakeAdminPlaybackCommands) Command(_ context.Context, in handlers.AdminPlaybackCommandInput) (handlers.AdminPlaybackCommandView, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return handlers.AdminPlaybackCommandView{}, f.err
	}
	view := f.view
	if view.CommandID == "" {
		view = handlers.AdminPlaybackCommandView{CommandID: in.CommandID, Sequence: in.Sequence, Outcome: handlers.AdminPlaybackCommandApplied, Delivery: handlers.AdminPlaybackDeliveryDispatched}
	}
	return view, nil
}

const adminCommandRoot = Prefix + "/admin/sessions/session-7"

func TestAdminPlaybackCommandsTransport(t *testing.T) {
	body := `{"command_id":"3fa85f64-5717-4562-b3fc-2c963f66afa6","sequence":7,"reason":"bedtime","deadline_ms":250}`
	for _, tc := range []struct {
		action string
		name   playback.CommandName
		body   string
	}{
		{"pause", playback.CommandPause, body},
		{"resume", playback.CommandUnpause, body},
		{"stop", playback.CommandStop, body},
		{"message", playback.CommandDisplayMessage, strings.TrimSuffix(body, "}") + `,"title":"Heads up","message":"Movie night ends at nine"}`},
	} {
		t.Run(tc.action, func(t *testing.T) {
			deps := pilotDeps(nil, nil)
			f := &fakeAdminPlaybackCommands{available: true}
			deps.AdminPlaybackCommands = f
			h := NewHandler(deps)
			path := adminCommandRoot + "/" + tc.action
			admin := with(bearer(adminToken), "X-Profile-Id", "p-primary")

			// Authority: a member is refused before the seam is reached.
			requireProblem(t, do(t, h, http.MethodPost, path, tc.body, bearer(memberToken)), TypePermissionDenied)
			if len(f.calls) != 0 {
				t.Fatal("member reached the command seam")
			}

			// Applied: 202 with the receipt, actor bound from the bearer.
			rec := do(t, h, http.MethodPost, path, tc.body, admin)
			if rec.Code != http.StatusAccepted {
				t.Fatal(rec.Code, rec.Body.String())
			}
			var receipt AdminPlaybackCommandReceipt
			if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.CommandID != "3fa85f64-5717-4562-b3fc-2c963f66afa6" || receipt.Sequence != 7 || receipt.Outcome != "applied" || receipt.Delivery != "dispatched" {
				t.Fatalf("receipt = %+v", receipt)
			}
			call := f.calls[len(f.calls)-1]
			if call.SessionID != "session-7" || call.Name != tc.name || call.ActorID != 2 || call.Sequence != 7 || call.Reason != "bedtime" || call.DeadlineMS != 250 {
				t.Fatalf("call = %+v", call)
			}
			if tc.action == "message" && (call.Title != "Heads up" || call.Message != "Movie night ends at nine") {
				t.Fatalf("message call = %+v", call)
			}

			// Replayed: 200 with the recorded receipt.
			f.view = handlers.AdminPlaybackCommandView{CommandID: "3fa85f64-5717-4562-b3fc-2c963f66afa6", Sequence: 7, Outcome: handlers.AdminPlaybackCommandReplayed, Delivery: handlers.AdminPlaybackDeliveryFallbackScheduled}
			rec = do(t, h, http.MethodPost, path, tc.body, admin)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outcome":"replayed"`) || !strings.Contains(rec.Body.String(), `"delivery":"fallback_scheduled"`) {
				t.Fatal(rec.Code, rec.Body.String())
			}

			// Validation refuses a malformed identity before the seam.
			before := len(f.calls)
			for _, invalid := range []string{
				strings.Replace(tc.body, `"sequence":7`, `"sequence":0`, 1),
				strings.Replace(tc.body, `"sequence":7`, `"sequence":9007199254740992`, 1),
				strings.Replace(tc.body, `"3fa85f64-5717-4562-b3fc-2c963f66afa6"`, `"not-a-uuid"`, 1),
				strings.Replace(tc.body, `"deadline_ms":250`, `"deadline_ms":20000`, 1),
				strings.Replace(tc.body, `"reason":"bedtime"`, `"reason":"bedtime","position":5`, 1),
			} {
				rec = do(t, h, http.MethodPost, path, invalid, admin)
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatal(invalid, rec.Code, rec.Body.String())
				}
			}
			if tc.action == "message" {
				rec = do(t, h, http.MethodPost, path, strings.Replace(tc.body, `"message":"Movie night ends at nine"`, `"message":""`, 1), admin)
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatal(rec.Code, rec.Body.String())
				}
			}
			if len(f.calls) != before {
				t.Fatal("invalid body reached the command seam")
			}

			// Seam outcomes map to deterministic Problems.
			for _, mapping := range []struct {
				err  error
				want ProblemType
			}{
				{handlers.ErrAdminPlaybackCommandStale, TypeConflict},
				{handlers.ErrAdminPlaybackCommandConflict, TypeIdempotencyConflict},
				{handlers.ErrAdminPlaybackRealtimeRequired, TypeConflict},
				{playback.ErrSessionNotFound, TypeNotFound},
				{handlers.ErrAdminPlaybackCommandInvalid, TypeValidationFailed},
				{handlers.ErrAdminPlaybackCommandUnavailable, TypeDependencyUnavailable},
			} {
				f.err = mapping.err
				requireProblem(t, do(t, h, http.MethodPost, path, tc.body, admin), mapping.want)
			}
			f.err = nil

			// A stale refusal carries the winning sequence so the client can
			// allocate above it; other conflicts do not.
			f.err = &handlers.AdminPlaybackCommandStaleError{Latest: 12}
			rec = do(t, h, http.MethodPost, path, tc.body, admin)
			requireProblem(t, rec, TypeConflict)
			if got := rec.Header().Get(LatestSequenceHeader); got != "12" {
				t.Fatalf("%s = %q, want 12", LatestSequenceHeader, got)
			}
			f.err = handlers.ErrAdminPlaybackRealtimeRequired
			rec = do(t, h, http.MethodPost, path, tc.body, admin)
			requireProblem(t, rec, TypeConflict)
			if got := rec.Header().Get(LatestSequenceHeader); got != "" {
				t.Fatalf("%s on a lane conflict = %q", LatestSequenceHeader, got)
			}
			f.err = nil

			// Demo mode refuses the mutation for non-owner administrators.
			demo := pilotDeps(nil, nil)
			demo.DemoSettings = fakeSettings{demo: true}
			demo.AdminPlaybackCommands = &fakeAdminPlaybackCommands{available: true}
			requireProblem(t, do(t, NewHandler(demo), http.MethodPost, path, tc.body, with(bearer(otherAdminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
		})
	}
}

func TestAdminPlaybackCommandsCapabilityAndAbsence(t *testing.T) {
	deps := pilotDeps(nil, nil)
	path := Prefix + "/admin/sessions/command-capabilities"
	admin := with(bearer(adminToken), "X-Profile-Id", "p-primary")
	rec := do(t, NewHandler(deps), http.MethodGet, path, "", admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) || !strings.Contains(rec.Body.String(), `"actions":[]`) || !strings.Contains(rec.Body.String(), `"sequenced_commands":false`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// Absent handler: the operation is registered and answers 503, not 404.
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, adminCommandRoot+"/pause", `{"command_id":"3fa85f64-5717-4562-b3fc-2c963f66afa6","sequence":1}`, admin), TypeDependencyUnavailable)

	deps.AdminPlaybackCommands = &fakeAdminPlaybackCommands{available: false}
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, adminCommandRoot+"/stop", `{"command_id":"3fa85f64-5717-4562-b3fc-2c963f66afa6","sequence":1}`, admin), TypeDependencyUnavailable)

	deps.AdminPlaybackCommands = &fakeAdminPlaybackCommands{available: true}
	rec = do(t, NewHandler(deps), http.MethodGet, path, "", admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"actions":["pause","resume","stop","message"]`) || !strings.Contains(rec.Body.String(), `"sequenced_commands":true`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, NewHandler(deps), http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)
}
