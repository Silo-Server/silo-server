package executor

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func assertOnboardingProgressEffect(t *testing.T, e *Env, method string, request scenariocatalog.Request, start, end time.Time, before, after json.RawMessage) {
	t.Helper()
	var old, rows []map[string]any
	if err := json.Unmarshal(before, &old); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &rows); err != nil {
		t.Fatal(err)
	}
	if len(old) > 1 || len(rows) != 1 {
		t.Fatal("expected exactly the original profile's progress row")
	}
	var body struct {
		LastStep  string `json:"last_step"`
		Completed bool   `json:"completed"`
	}
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	got := rows[0]
	bounded := func(value any) {
		t.Helper()
		text, ok := value.(string)
		if !ok {
			t.Fatal("progress timestamp type")
		}
		stamp, err := time.Parse(time.RFC3339Nano, text)
		lo := start
		if method == "POST" {
			lo = lo.Truncate(time.Second)
		}
		if err != nil || stamp.Before(lo) || stamp.After(end) {
			t.Errorf("progress timestamp outside application request bounds: %v", err)
		}
	}
	revision := float64(1)
	var completed any
	if len(old) == 1 {
		prior := old[0]
		if prior["user_id"] != float64(e.users[fixtureMember].ID) || prior["profile_id"] != profileSecondary || prior["tour_id"] != "core-2026-07" {
			t.Fatal("unexpected prior progress owner")
		}
		n, ok := prior["revision"].(float64)
		if !ok {
			t.Fatal("progress revision type")
		}
		revision = n + 1
		completed = prior["completed_at"]
	}
	bounded(got["updated_at"])
	if body.Completed && completed == nil {
		bounded(got["completed_at"])
		completed = got["completed_at"]
	}
	want := map[string]any{"user_id": float64(e.users[fixtureMember].ID), "profile_id": profileSecondary, "tour_id": "core-2026-07", "last_step": body.LastStep, "completed_at": completed, "skipped_at": nil, "updated_at": got["updated_at"], "revision": revision}
	if !reflect.DeepEqual(want, got) {
		t.Error("unexpected progress owner/field/revision/monotonic/column effect")
	}
}
