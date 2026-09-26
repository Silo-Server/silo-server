package apiv2

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

func TestPersonalHistoryMonitorConditionalLifecycle(t *testing.T) {
	fake := fixtureHistoryImports()
	h := newTestHandler(t, historyImportDeps(fake))
	path := "/api/v2/history-imports/runs/run-3"
	first := do(t, h, http.MethodGet, path, "", bearer(memberToken))
	if first.Code != http.StatusOK || first.Header().Get("ETag") == "" || first.Header().Get("Location") != path || first.Header().Get("Retry-After") != "2" {
		t.Fatalf("monitor headers: %d %v", first.Code, first.Header())
	}
	var body HistoryImportRun
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Terminal || body.Cancelable {
		t.Fatalf("running personal state=%+v", body)
	}
	tag := first.Header().Get("ETag")
	same := do(t, h, http.MethodGet, path, "", with(bearer(memberToken), "If-None-Match", tag))
	if same.Code != http.StatusNotModified || same.Body.Len() != 0 || same.Header().Get("ETag") != tag || same.Header().Get("Retry-After") != "2" {
		t.Fatalf("304: %d %v %s", same.Code, same.Header(), same.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(bearer(memberToken), "If-Match", `"stale"`)), TypePreconditionFailed)
	// Ownership precedes both conditional validators, including wildcard reads.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history-imports/runs/run-other", "", with(bearer(memberToken), "If-None-Match", "*")), TypeNotFound)
	fake.runs[0].CancelRequested = true
	pending := do(t, h, http.MethodGet, path, "", with(bearer(memberToken), "If-None-Match", tag))
	if err := json.Unmarshal(pending.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if pending.Code != http.StatusOK || body.Status != "canceling" || body.Terminal || body.Cancelable || pending.Header().Get("ETag") == tag {
		t.Fatalf("pending cancel: %s", pending.Body.String())
	}
	fake.runs[0].Status = historyimport.RunStatusCancelled
	terminal := do(t, h, http.MethodGet, path, "", bearer(memberToken))
	if err := json.Unmarshal(terminal.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Terminal || body.Cancelable || terminal.Header().Get("Retry-After") != "" {
		t.Fatalf("terminal: %s %v", terminal.Body.String(), terminal.Header())
	}
	notModified := do(t, h, http.MethodGet, path, "", with(bearer(memberToken), "If-None-Match", terminal.Header().Get("ETag")))
	if notModified.Code != http.StatusNotModified || notModified.Header().Get("Retry-After") != "" {
		t.Fatalf("terminal304: %d %v", notModified.Code, notModified.Header())
	}
}

func TestPersonalHistoryProjectionAndAdmissionErrorsAreSafe(t *testing.T) {
	fake := fixtureHistoryImports()
	fake.runs[0].Warnings = []string{"upstream URL contains token=do-not-publish"}
	fake.runs[0].ErrorMessage = "password=do-not-publish"
	fake.runs[0].UnmatchedSamples = []historyimport.UnmatchedSample{{Title: "Movie", Reason: "private-token=do-not-publish"}}
	h := newTestHandler(t, historyImportDeps(fake))
	for _, path := range []string{"/api/v2/history-imports/runs", "/api/v2/history-imports/runs/run-3"} {
		response := do(t, h, http.MethodGet, path, "", bearer(memberToken))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "do-not-publish") {
			t.Fatalf("unsafe projection: %d %s", response.Code, response.Body.String())
		}
	}
	for _, test := range []struct {
		err  error
		want ProblemType
	}{
		{errors.Join(historyimport.ErrPersonalAdmissionUncertain, errors.New("private storage token=do-not-publish")), TypeDependencyUnavailable},
		{historyimport.ErrPersonalCredentialsUnavailable, TypeDependencyUnavailable},
		{historyimport.ErrPersonalSessionChanged, TypeConflict},
		{historyimport.ErrConnectSessionUsed, TypeConflict},
		{historyimport.ErrPlexSessionUsed, TypeConflict},
		{historyimport.ErrRunConfigurationChanged, TypeConflict},
	} {
		fake.createErr = test.err
		response := do(t, h, http.MethodPost, "/api/v2/history-imports/runs", `{"profile_id":"p-owner","source":"plex"}`, bearer(memberToken))
		p := requireProblem(t, response, test.want)
		if strings.Contains(response.Body.String(), "do-not-publish") || response.Header().Get("Retry-After") != "" {
			t.Fatalf("unsafe admission error: %s", response.Body.String())
		}
		if errors.Is(test.err, historyimport.ErrPersonalAdmissionUncertain) && p.Detail != historyimport.ErrPersonalAdmissionUncertain.Error() {
			t.Fatalf("missing uncertainty advisory: %s", p.Detail)
		}
	}
}

func personalHistoryImportFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "personal_history_run_accepted", operationID: "createHistoryImportRun", scenario: "Durable personal acceptance identifies the account-owned monitor; submitting again is not automatically safe.", method: http.MethodPost, path: "/api/v2/history-imports/runs", body: `{"profile_id":"p-owner","source":"plex","plex_base_url":"https://plex.example.test","plex_token":"fixture-token"}`, headers: bearer(memberToken), status: http.StatusAccepted, assertHeaders: []string{"Content-Type", "Cache-Control", "Location", "Retry-After"}, schema: "#/components/schemas/HistoryImportRun"},
		{name: "personal_history_run_monitor", operationID: "getHistoryImportRun", scenario: "A running personal monitor supplies a strong validator, polling delay, and no personal cancellation command.", method: http.MethodGet, path: "/api/v2/history-imports/runs/run-3", headers: bearer(memberToken), status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag", "Location", "Retry-After"}, schema: "#/components/schemas/HistoryImportRun"},
		{name: "personal_history_run_not_modified", operationID: "getHistoryImportRun", scenario: "An account-owned conditional monitor can return no body while retaining its polling delay and validator.", method: http.MethodGet, path: "/api/v2/history-imports/runs/run-3", headers: with(bearer(memberToken), "If-None-Match", "*"), status: http.StatusNotModified, assertHeaders: []string{"Cache-Control", "ETag", "Location", "Retry-After"}},
		{name: "personal_history_run_terminal", operationID: "getHistoryImportRun", scenario: "Terminal personal imports stop polling and expose safe diagnostic summaries.", method: http.MethodGet, path: "/api/v2/history-imports/runs/run-1", headers: bearer(memberToken), status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag", "Location"}, schema: "#/components/schemas/HistoryImportRun"},
	}
}
