package markers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/opslog"
)

type contributionLogWriter struct{ entries []opslog.Entry }

func (w *contributionLogWriter) Write(entry opslog.Entry) { w.entries = append(w.entries, entry) }
func (*contributionLogWriter) Close() error               { return nil }

func TestContributeLogsSanitizedSubmissionFailure(t *testing.T) {
	sub := &fakeSubmitter{id: "introdb", err: errors.New(`rejected {"url":"https://example.test/x","api_key":"FAKE_FIXTURE_VALUE"}`)}
	record := &fakeRecorder{}
	service := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, record)
	var console bytes.Buffer
	writer := &contributionLogWriter{}
	service.logger = slog.New(opslog.NewHandler(slog.NewJSONHandler(&console, nil), writer, slog.LevelInfo, "test"))
	file := newContribFile()
	file.IntroStart, file.IntroEnd = new(0.0), new(60.0)
	file.IntroMarkersSource = new(models.MarkerSourceManual)
	outcomes, err := service.ContributeFile(t.Context(), file, ContributeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != OutcomeStatusError {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if len(record.recorded) != 1 || record.recorded[0].Error == nil {
		t.Fatal("missing audit error")
	}
	if len(writer.entries) != 1 {
		t.Fatalf("logs = %d", len(writer.entries))
	}
	message, ok := writer.entries[0].Attrs["error"].(string)
	if !ok {
		t.Fatalf("error attribute is %T, want string", writer.entries[0].Attrs["error"])
	}
	for _, value := range []string{outcomes[0].Reason, *record.recorded[0].Error, message, console.String()} {
		if strings.Contains(value, "FAKE_FIXTURE_VALUE") {
			t.Fatal("credential survived in an error sink")
		}
		if !strings.Contains(value, "rejected") {
			t.Fatal("useful diagnostic lost")
		}
	}
}

type failingContributionResolver struct{ err error }

func (r failingContributionResolver) ResolveForFile(context.Context, *models.MediaFile) (ExternalIDs, error) {
	return ExternalIDs{}, r.err
}

func TestContributeSanitizesResolverFailure(t *testing.T) {
	service := newContribService(&fakeSubmitter{id: "introdb"}, fakeConfig{}, &fakeRecorder{})
	service.resolver = failingContributionResolver{errors.New("resolve failed: password=FAKE_FIXTURE_VALUE")}
	_, err := service.ContributeFile(t.Context(), newContribFile(), ContributeOptions{})
	if err == nil {
		t.Fatal("missing resolver failure")
	}
	if strings.Contains(err.Error(), "FAKE_FIXTURE_VALUE") || !strings.Contains(err.Error(), "resolve failed") {
		t.Fatal("resolver diagnostic was not sanitized")
	}
}
