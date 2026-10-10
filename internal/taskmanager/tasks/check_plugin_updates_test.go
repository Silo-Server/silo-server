package tasks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type pluginUpdateCheckerStub struct {
	summary plugins.AutoUpdateSummary
}

func (s pluginUpdateCheckerStub) Check(context.Context, plugins.AutoUpdateOptions) (plugins.AutoUpdateSummary, error) {
	return s.summary, nil
}

type pluginUpdateProgressStub struct {
	resultData json.RawMessage
}

func (p *pluginUpdateProgressStub) Report(float64, string)          {}
func (p *pluginUpdateProgressStub) SetResultData(d json.RawMessage) { p.resultData = d }

func TestCheckPluginUpdatesTaskFailsWhenAnUpdateIsRefused(t *testing.T) {
	task := NewCheckPluginUpdatesTask(pluginUpdateCheckerStub{summary: plugins.AutoUpdateSummary{
		CatalogEntries:   2,
		FailedOperations: 1,
		Failures:         []string{"process plugin update silo.example: binary checksum mismatch"},
	}})
	progress := &pluginUpdateProgressStub{}

	err := task.Execute(context.Background(), progress)
	if err == nil {
		t.Fatal("Execute returned nil, want the refused update reported")
	}
	if !strings.Contains(err.Error(), "silo.example: binary checksum mismatch") {
		t.Fatalf("Execute error = %q, want the plugin and reason", err)
	}
	if len(progress.resultData) == 0 {
		t.Fatal("result data not recorded for a failed run")
	}
}

func TestCheckPluginUpdatesTaskSucceedsWithoutFailures(t *testing.T) {
	task := NewCheckPluginUpdatesTask(pluginUpdateCheckerStub{summary: plugins.AutoUpdateSummary{
		CatalogEntries: 2,
		UpdatesApplied: 1,
	}})
	if err := task.Execute(context.Background(), &pluginUpdateProgressStub{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}
