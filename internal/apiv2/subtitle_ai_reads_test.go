package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

type fakeSubtitleAIReads struct {
	filter    catalogpkg.AccessFilter
	jobID     int64
	userID    int
	profileID string
	jobError  string
}

func (f *fakeSubtitleAIReads) job() *ai.Job {
	message := f.jobError
	if message == "" {
		message = "PRIVATE upstream URL"
	}
	return &ai.Job{ID: 9007199254740993, MediaFileID: 42, Status: ai.JobStatusFailed, ProgressMessage: "Transcribing", ErrorMessage: message, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}
}

func TestSubtitleAIQuotaFailureExplainsRecoveryWithoutProviderDetails(t *testing.T) {
	deps, _ := catalogDeps(t)
	deps.SubtitleAIReads = &fakeSubtitleAIReads{jobError: llm.ErrQuotaExhausted.Error() + ": PRIVATE provider response"}
	response := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/subtitles/ai/jobs/9007199254740993", "", viewerHeaders())
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Ask a server administrator to check AI Services.") || strings.Contains(response.Body.String(), "PRIVATE") {
		t.Fatalf("quota failure response: %d %s", response.Code, response.Body.String())
	}
}
func (f *fakeSubtitleAIReads) ListSubtitleAIJobs(_ context.Context, filter catalogpkg.AccessFilter, _ int) ([]ai.Job, error) {
	f.filter = filter
	return []ai.Job{*f.job()}, nil
}
func (f *fakeSubtitleAIReads) GetSubtitleAIJob(_ context.Context, filter catalogpkg.AccessFilter, id int64) (*ai.Job, error) {
	f.filter = filter
	f.jobID = id
	return f.job(), nil
}
func (f *fakeSubtitleAIReads) SubtitleAIQuota(_ context.Context, user int, profile string, _ bool) (ai.QuotaStatus, error) {
	f.userID = user
	f.profileID = profile
	return ai.QuotaStatus{Limited: true, Limit: 5, Used: 2, Remaining: 3, Period: "daily"}, nil
}
func TestSubtitleAIReadsV2KeepIdentityAndRedactErrors(t *testing.T) {
	deps, _ := catalogDeps(t)
	f := new(fakeSubtitleAIReads)
	deps.SubtitleAIReads = f
	h := newTestHandler(t, deps)
	for _, path := range []string{"/subtitles/ai/jobs?media_file_id=42", "/subtitles/ai/jobs/9007199254740993"} {
		out := do(t, h, http.MethodGet, Prefix+path, "", viewerHeaders())
		if out.Code != 200 || !strings.Contains(out.Body.String(), `"id":"9007199254740993"`) || strings.Contains(out.Body.String(), "PRIVATE") || !strings.Contains(out.Body.String(), `"result_subtitle_id":null`) {
			t.Fatalf("job: %d %s", out.Code, out.Body.String())
		}
	}
	if f.jobID != 9007199254740993 || f.filter.ProfileID != "p-owner" {
		t.Fatalf("identity changed: %+v", f)
	}
	quota := do(t, h, http.MethodGet, Prefix+"/subtitles/ai/quota", "", viewerHeaders())
	if quota.Code != 200 || f.userID != 1 || f.profileID != "p-owner" {
		t.Fatalf("quota identity: %d %+v", quota.Code, f)
	}
	before := f.jobID
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/subtitles/ai/jobs/0", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/subtitles/ai/jobs/42", "", with(bearer(memberToken), "X-Profile-Id", "p-other")), TypeNotFound)
	if f.jobID != before {
		t.Fatal("invalid viewer or ID reached service")
	}
	deps.SubtitleAIReads = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/subtitles/ai/quota", "", viewerHeaders()), TypeDependencyUnavailable)
}
