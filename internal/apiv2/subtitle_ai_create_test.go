package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

type fakeSubtitleAICreate struct {
	calls   int
	command handlers.SubtitleAICreateCommand
	filter  catalogpkg.AccessFilter
	failure error
}

func (f *fakeSubtitleAICreate) CreateSubtitleAIJob(_ context.Context, filter catalogpkg.AccessFilter, command handlers.SubtitleAICreateCommand) (handlers.SubtitleAICreateResult, error) {
	f.calls++
	f.command = command
	f.filter = filter
	return handlers.SubtitleAICreateResult{Job: &ai.Job{ID: 9007199254740993, MediaFileID: command.MediaFileID, Kind: command.Kind, SourceIndex: command.SourceIndex, SourceLanguage: command.SourceLanguage, TargetLanguage: command.TargetLanguage, CreatedAt: time.Unix(100, 0), UpdatedAt: time.Unix(100, 0), Status: ai.JobStatusPending}, LiveDeliveryAttached: true}, f.failure
}
func TestSubtitleAICreateV2(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := new(fakeSubtitleAICreate)
	deps.SubtitleAICreate = fake
	h := newTestHandler(t, deps)
	path := Prefix + "/subtitles/ai/translate"
	body := `{"media_file_id":"42","kind":"translate","source_index":3,"source_language":"en","target_language":"fr","session_id":"session","start_position":12.5}`
	response := do(t, h, http.MethodPost, path, body, viewerHeaders())
	if response.Code != 202 {
		t.Fatalf("create %d %s", response.Code, response.Body.String())
	}
	var got struct {
		Job struct {
			ID   string `json:"id"`
			File string `json:"media_file_id"`
		} `json:"job"`
		Live bool `json:"live_delivery_attached"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.Job.ID != "9007199254740993" || got.Job.File != "42" || !got.Live {
		t.Fatal("invalid exact-ID response", err)
	}
	if fake.calls != 1 || fake.filter.UserID != 1 || fake.filter.ProfileID != "p-owner" || fake.command.SourceIndex != 3 || fake.command.StartPosition != 12.5 {
		t.Fatal("wrong request authority/input")
	}
	for _, invalid := range []string{`{"media_file_id":"0"}`, `{"media_file_id":42}`, `{"media_file_id":"42","kind":"unknown"}`, `{"media_file_id":"42","kind":"translate","source_index":-2,"source_language":"","target_language":"fr","start_position":0}`} {
		requireProblem(t, do(t, h, http.MethodPost, path, invalid, viewerHeaders()), TypeValidationFailed)
	}
	requireProblem(t, do(t, h, http.MethodPost, path, body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(memberToken), "X-Profile-Id", "p-other")), TypeNotFound)
	if fake.calls != 1 {
		t.Fatal("invalid request reached create")
	}
	fake.failure = errors.New("PRIVATE provider details")
	requireProblem(t, do(t, h, http.MethodPost, path, body, viewerHeaders()), TypeInternalError)
	deps.SubtitleAICreate = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path, body, viewerHeaders()), TypeDependencyUnavailable)
}
