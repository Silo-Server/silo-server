package apiv2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

func TestStoredInstantOnlyOmitsAbsentValues(t *testing.T) {
	if got, p := storedInstant(""); got != nil || p != nil {
		t.Fatalf("absent timestamp = %v, %v", got, p)
	}
	for _, raw := range []string{"2026-01-02T03:04:05.678Z", "2026-01-02 03:04:05.678+00", "2026-01-02 03:04:05.678+00:00", "2026-01-02 03:04:05.678"} {
		got, p := storedInstant(raw)
		if p != nil || got == nil {
			t.Fatalf("valid stored timestamp %q = %v, %v", raw, got, p)
		}
		wire, err := json.Marshal(got)
		if err != nil || string(wire) != `"2026-01-02T03:04:05.678Z"` {
			t.Fatalf("timestamp %q encoded as %s: %v", raw, wire, err)
		}
	}
	for _, raw := range []string{"PRIVATE_CORRUPT_TIMESTAMP", " ", "2026-02-30T00:00:00Z"} {
		got, p := storedInstant(raw)
		if got != nil || p == nil || p.Status != 500 || strings.Contains(p.Detail, "PRIVATE_") {
			t.Fatalf("corrupt stored timestamp %q = %v, %v", raw, got, p)
		}
	}
}

func TestCorruptSettingTimestampFailsAllReadProjections(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := deps.SettingValues.(*fakeSettingValuesSeam)
	row := handlers.SettingValueView{Key: "ui.theme", Scope: "profile", ProfileID: "p-owner", Value: json.RawMessage(`"cinema-light"`), UpdatedAt: "PRIVATE_CORRUPT_TIMESTAMP"}
	f.values[settingRowKey(row)] = row
	h := newTestHandler(t, deps)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/settings/values/ui.theme?scope=profile", ""},
		{"GET", "/settings/values?keys=ui.theme&scope=profile", ""},
		{"GET", "/settings/values/effective?keys=ui.theme", ""},
		{"POST", "/settings/values/effective", `{"keys":["ui.theme"],"contexts":[{"context_id":"one","library_id":"3"}]}`},
	} {
		rec := do(t, h, tc.method, Prefix+tc.path, tc.body, settingsOwner())
		requireProblem(t, rec, TypeInternalError)
		if strings.Contains(rec.Body.String(), "PRIVATE_") {
			t.Fatal("stored corruption leaked to response")
		}
	}
}

type corruptHistorySplit struct{ fakeAdminSplit }

func (f *corruptHistorySplit) SplitAdminItem(ctx context.Context, id string, req handlers.AdminSplitRequest) (handlers.AdminSplitResult, error) {
	result, err := f.fakeAdminSplit.SplitAdminItem(ctx, id, req)
	result.Reattribution.AmbiguousHistory[0].WatchedAt = "PRIVATE_CORRUPT_TIMESTAMP"
	return result, err
}

func TestSplitRejectsCorruptHistoryTimestamp(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.AdminCatalogSplit = &corruptHistorySplit{}
	rec := do(t, newTestHandler(t, deps), "POST", Prefix+"/admin/items/source/split", `{"file_ids":["42"],"target":{"content_id":"target"},"dry_run":true}`, bearer(adminToken))
	requireProblem(t, rec, TypeInternalError)
}
