package apiv2

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAdminCollectionUpdateCommandPreservesNullableGroup(t *testing.T) {
	for _, tc := range []struct {
		body  string
		set   bool
		group string
	}{
		{`{"library_ids":["12"],"title":"Edit"}`, false, ""},
		{`{"library_ids":["12"],"group_id":null}`, true, ""},
		{`{"group_id":"group-1"}`, true, "group-1"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var dto AdminCollectionUpdate
			if err := json.Unmarshal([]byte(tc.body), &dto); err != nil {
				t.Fatal(err)
			}
			cmd, p := dto.command([]byte(tc.body))
			if p != nil {
				t.Fatal(p)
			}
			if cmd.GroupID.Set() != tc.set {
				t.Fatalf("group set=%v want %v", cmd.GroupID.Set(), tc.set)
			}
			if tc.group == "" {
				if cmd.GroupID.Value() != nil {
					t.Fatalf("unexpected group: %v", cmd.GroupID.Value())
				}
			} else if cmd.GroupID.Value() == nil || *cmd.GroupID.Value() != tc.group {
				t.Fatal("group value lost")
			}
			if dto.LibraryIDs != nil && (cmd.LibraryIDs == nil || !reflect.DeepEqual(*cmd.LibraryIDs, []int{12})) {
				t.Fatalf("library IDs not lowered: %v", cmd.LibraryIDs)
			}
		})
	}
	// The raw bytes carry only null-presence information, never artwork or extra definition members.
	cmd, p := (AdminCollectionUpdate{Title: new("Typed")}).command([]byte(`{"title":"Raw","poster_source_url":"https://example.invalid/image"}`))
	if p != nil || cmd.Title == nil || *cmd.Title != "Typed" || cmd.PosterSourceURL != nil {
		t.Fatalf("raw body overrode typed input: %+v %v", cmd, p)
	}
}

func TestAdminCollectionResultSanitizesDiagnosticsAndDates(t *testing.T) {
	stamp := time.Date(2026, 9, 5, 12, 34, 56, 123456789, time.FixedZone("offset", 3600))
	run := adminCollectionSyncRunOf(&models.LibraryCollectionSyncRun{ID: "run", CollectionID: "collection", Status: "failed", Message: "PRIVATE_DIAGNOSTIC", Warnings: json.RawMessage(`["PRIVATE_WARNING"]`), CreatedAt: stamp, StartedAt: &stamp, CompletedAt: new(time.Time{})})
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"PRIVATE_", "warnings", "completed_at", "0001-"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("unexpected %s in %s", forbidden, data)
		}
	}
	if !strings.Contains(string(data), `"created_at":"2026-09-05T11:34:56.123Z"`) {
		t.Fatalf("timestamp not normalized: %s", data)
	}
	var source handlers.AdminCollectionTemplateResult
	if err = json.Unmarshal([]byte(`{"bundle_id":"bundle","failed":[{"template_id":"a","library_id":9,"reason":"Collection already exists"}],"featured_failed":[{"surface":"home","template_id":"a","reason":"Library has no featured slot"}]}`), &source); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(adminTemplateResultOf(source))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PRIVATE_") || strings.Contains(string(data), `"library_id":"0"`) {
		t.Fatalf("unsafe template result: %s", data)
	}
	// The per-entry reason is the only explanation a caller gets for a skipped
	// or failed template, and v1 has always returned it.
	if !strings.Contains(string(data), `"reason":"operation_failed"`) || strings.Contains(string(data), `Collection already exists`) {
		t.Fatalf("template entry reason dropped: %s", data)
	}
	if !strings.Contains(string(data), `"library_id":"9"`) || !strings.Contains(string(data), `"created":[]`) {
		t.Fatalf("lost identifiers or empty arrays: %s", data)
	}
	view := adminCollectionOf(handlers.AdminCollection{ID: "collection", LibraryID: 9, LibraryIDs: []int{9}, CreatedAt: stamp.Format(time.RFC3339Nano), UpdatedAt: stamp.Format(time.RFC3339Nano), LastSyncStatus: "failed", LastSyncMessage: "PRIVATE_MESSAGE"})
	data, err = json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PRIVATE_") || strings.Contains(string(data), "last_sync_at") || strings.Contains(string(data), "next_sync_at") {
		t.Fatalf("invalid optional timestamps/diagnostic: %s", data)
	}
}
