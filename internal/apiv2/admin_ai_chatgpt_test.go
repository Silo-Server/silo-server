package apiv2

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
)

type fakeChatGPT struct {
	calls      int
	callback   string
	clientID   string
	err        error
	transition *chatgpt.SettingsTransition
	status     *chatgpt.Status
}

func (f *fakeChatGPT) Status(context.Context) (chatgpt.Status, error) {
	f.calls++
	if f.status != nil {
		return *f.status, f.err
	}
	return chatgpt.Status{Accounts: []chatgpt.AccountStatus{}, LoginResult: "idle"}, f.err
}

func TestChatGPTStatusRetainsFirstLoginOutcome(t *testing.T) {
	deps := requestDeps(fixtureRequests())
	deps.ChatGPT = &fakeChatGPT{status: &chatgpt.Status{
		Accounts: []chatgpt.AccountStatus{}, LoginFirstConnection: true,
		LoginResult: "connected", AttemptID: "first-attempt",
	}}
	res := do(t, NewHandler(deps), "GET", Prefix+"/admin/ai/chatgpt", "", actingRequestAdmin)
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"login_first_connection":true`) || !strings.Contains(res.Body.String(), `"attempt_id":"first-attempt"`) {
		t.Fatalf("first-login reconciliation metadata was lost: %d %s", res.Code, res.Body.String())
	}
}
func (f *fakeChatGPT) StartLogin(context.Context, string) (chatgpt.Login, error) {
	f.calls++
	return chatgpt.Login{AttemptID: "attempt", ExpiresAt: time.Now().Add(time.Minute), AuthorizationURL: "https://auth.openai.com/api/accounts/authorize", CallbackURI: "http://127.0.0.1:51121/auth/callback"}, f.err
}
func (f *fakeChatGPT) CompleteLoginWithSettings(_ context.Context, callback string) (*chatgpt.SettingsTransition, error) {
	f.calls++
	f.callback = callback
	return f.transition, f.err
}
func (f *fakeChatGPT) Models(_ context.Context, clientID string) ([]chatgpt.Model, error) {
	f.calls++
	f.clientID = clientID
	return []chatgpt.Model{}, f.err
}

func TestChatGPTModelsBindRequestedAccount(t *testing.T) {
	f := new(fakeChatGPT)
	deps := requestDeps(fixtureRequests())
	deps.ChatGPT = f
	h := NewHandler(deps)
	for _, clientID := range []string{"", "account-a"} {
		res := do(t, h, "GET", Prefix+"/admin/ai/chatgpt/models?client_id="+clientID, "", actingRequestAdmin)
		if res.Code != 200 || f.clientID != clientID {
			t.Fatal(res.Code, res.Body.String(), f.clientID)
		}
	}
}
func (f *fakeChatGPT) SelectAccountWithSettings(context.Context, string) (*chatgpt.SettingsTransition, error) {
	f.calls++
	return f.transition, f.err
}
func (f *fakeChatGPT) Disconnect(context.Context, string) (bool, error) {
	f.calls++
	return false, f.err
}

func TestChatGPTAdministrationRequiresActingAdmin(t *testing.T) {
	f := new(fakeChatGPT)
	deps := requestDeps(fixtureRequests())
	deps.ChatGPT = f
	h := NewHandler(deps)
	for _, test := range []struct{ method, path, body string }{
		{"GET", "/admin/ai/chatgpt", ""},
		{"GET", "/admin/ai/capabilities", ""},
		{"GET", "/admin/ai/chatgpt/models", ""},
		{"POST", "/admin/ai/chatgpt/login", `{}`},
		{"POST", "/admin/ai/chatgpt/login/complete", `{"callback_url":"callback"}`},
		{"POST", "/admin/ai/chatgpt/accounts/app/select", `{}`},
		{"DELETE", "/admin/ai/chatgpt/accounts/app", ""},
	} {
		requireProblem(t, do(t, h, test.method, Prefix+test.path, test.body, nil), TypeAuthenticationRequired)
		requireProblem(t, do(t, h, test.method, Prefix+test.path, test.body, bearer(memberToken)), TypePermissionDenied)
	}
	if f.calls != 0 {
		t.Fatal("anonymous admin request reached credentials")
	}
	res := do(t, h, "POST", Prefix+"/admin/ai/chatgpt/login", `{}`, actingRequestAdmin)
	if res.Code != 200 || res.Header().Get("Cache-Control") != "no-store" || !strings.Contains(res.Body.String(), `"attempt_id":"attempt"`) {
		t.Fatal(res.Code, res.Body.String())
	}
	f.err = errors.New("private provider-token")
	res = do(t, h, "GET", Prefix+"/admin/ai/chatgpt", "", actingRequestAdmin)
	if res.Code != 503 || strings.Contains(res.Body.String(), "provider-token") {
		t.Fatal(res.Code, res.Body.String())
	}
}

func TestChatGPTCallbackUsesAdminBoundaryAndSafeErrors(t *testing.T) {
	f := new(fakeChatGPT)
	deps := requestDeps(fixtureRequests())
	deps.ChatGPT = f
	h := NewHandler(deps)
	path := Prefix + "/admin/ai/chatgpt/login/complete"
	callbackURL := "http://127.0.0.1:51121/auth/callback?code=auth-code&state=state&client_id=issued-app"
	body := `{"callback_url":"` + callbackURL + `"}`
	res := do(t, h, "POST", path, body, actingRequestAdmin)
	if res.Code != 204 || f.callback != callbackURL {
		t.Fatal(res.Code, res.Body.String(), f)
	}
	f.err = chatgpt.ErrInvalidCallbackURL
	res = do(t, h, "POST", path, `{"callback_url":"invalid"}`, actingRequestAdmin)
	requireProblem(t, res, TypeValidationFailed)
	if strings.Contains(res.Body.String(), "invalid\"") {
		t.Fatal("pasted URL echoed in problem")
	}
	f.err = chatgpt.ErrInvalidLogin
	requireProblem(t, do(t, h, "POST", path, body, actingRequestAdmin), TypeValidationFailed)
	f.err = fmt.Errorf("private-token: %w", chatgpt.ErrIdentity)
	res = do(t, h, "POST", path, body, actingRequestAdmin)
	requireProblem(t, res, TypeValidationFailed)
	if strings.Contains(res.Body.String(), "private-token") {
		t.Fatal("wrapped provider diagnostic exposed")
	}
}

func TestAICapabilitiesAreModelSpecific(t *testing.T) {
	h := NewHandler(requestDeps(fixtureRequests()))
	for _, test := range []struct{ model, want, absent string }{
		{"gpt-4o-mini", `"reasoning_levels":[]`, `"high"`},
		{"gpt-6.1-sol", `"xhigh"`, `"none"`},
	} {
		res := do(t, h, "GET", Prefix+"/admin/ai/capabilities?model="+test.model, "", actingRequestAdmin)
		if res.Code != 200 || !strings.Contains(res.Body.String(), test.want) || strings.Contains(res.Body.String(), test.absent) || res.Header().Get("ETag") == "" {
			t.Fatal(res.Code, res.Body.String())
		}
	}
}

func TestChatGPTCapabilityReflectsServiceAvailability(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(fmt.Sprint(available), func(t *testing.T) {
			deps := requestDeps(fixtureRequests())
			if available {
				deps.ChatGPT = new(fakeChatGPT)
			}
			res := do(t, NewHandler(deps), "GET", Prefix+"/admin/ai/capabilities", "", actingRequestAdmin)
			if res.Code != 200 || !strings.Contains(res.Body.String(), fmt.Sprintf(`"chatgpt_sign_in":%t`, available)) {
				t.Fatal(res.Code, res.Body.String())
			}
		})
	}
}

func TestChatGPTActivationHeadersMatchSettingsInspection(t *testing.T) {
	for _, path := range []string{"/admin/ai/chatgpt/accounts/app/select", "/admin/ai/chatgpt/login/complete"} {
		t.Run(path, func(t *testing.T) {
			before := map[string]string{"ai.chat_model": "old-model", "ai.api_key": "private-secret"}
			after := maps.Clone(before)
			after["ai.chat_model"] = "gpt-4o-mini"
			settings := &fakeAdminSettingsWrite{stored: before}
			deps := requestDeps(fixtureRequests())
			deps.CursorSecret = []byte("synthetic-settings-secret")
			deps.AdminSettingsWrite = settings
			deps.AdminSettingsInspection = settings
			activation := &chatgpt.SettingsTransition{Before: before, After: after, FirstConnection: true}
			deps.ChatGPT = &fakeChatGPT{transition: activation}
			h := NewHandler(deps)
			read := do(t, h, "GET", Prefix+"/admin/settings/effective", "", actingRequestAdmin)
			res := do(t, h, "POST", Prefix+path, `{"callback_url":"callback"}`, actingRequestAdmin)
			wantStatus := 200
			if strings.HasSuffix(path, "/complete") {
				wantStatus = 204
			}
			if res.Code != wantStatus || res.Header().Get("X-Silo-Settings-Before-ETag") != read.Header().Get("ETag") {
				t.Fatalf("before receipt mismatch: %d %s", res.Code, res.Body.String())
			}
			settings.stored = after
			read = do(t, h, "GET", Prefix+"/admin/settings/effective", "", actingRequestAdmin)
			if res.Header().Get("X-Silo-Settings-After-ETag") != read.Header().Get("ETag") {
				t.Fatal("after receipt differs from settings inspection")
			}
			if strings.Contains(res.Body.String(), "private-secret") {
				t.Fatal("receipt exposed raw settings")
			}
			if res.Header().Get("X-Silo-ChatGPT-First-Connection") != "true" {
				t.Fatal("first activation header was lost")
			}
			activation.FirstConnection = false
			res = do(t, h, "POST", Prefix+path, `{"callback_url":"callback"}`, actingRequestAdmin)
			if res.Code != wantStatus || res.Header().Get("X-Silo-ChatGPT-First-Connection") != "false" {
				t.Fatalf("later activation repeated the welcome: %d %s", res.Code, res.Body.String())
			}
		})
	}
}
