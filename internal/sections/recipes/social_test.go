package recipes

import (
	"encoding/json"
	"testing"
)

func TestTrendingValidatesWindow(t *testing.T) {
	rec, _ := Get("trending_on_server")
	for _, good := range []string{`{}`, `{"window":""}`, `{"window":"24h"}`, `{"window":"7d"}`, `{"window":"30d"}`} {
		if err := rec.Validate(json.RawMessage(good)); err != nil {
			t.Errorf("good params %s rejected: %v", good, err)
		}
	}
	if err := rec.Validate(json.RawMessage(`{"window":"forever"}`)); err == nil {
		t.Error("expected error for invalid window")
	}
}

func TestMostWatchedValidatesWindow(t *testing.T) {
	rec, _ := Get("most_watched")
	for _, good := range []string{`{}`, `{"window":""}`, `{"window":"week"}`, `{"window":"month"}`} {
		if err := rec.Validate(json.RawMessage(good)); err != nil {
			t.Errorf("good params %s rejected: %v", good, err)
		}
	}
	if err := rec.Validate(json.RawMessage(`{"window":"year"}`)); err == nil {
		t.Error("expected error for invalid window")
	}
}

func TestProfileActivityFeedAcceptsEmptyOrPinned(t *testing.T) {
	rec, _ := Get("profile_activity_feed")
	if err := rec.Validate(json.RawMessage(`{}`)); err != nil {
		t.Errorf("empty config rejected: %v", err)
	}
	if err := rec.Validate(json.RawMessage(`{"profile_id":"abc-123"}`)); err != nil {
		t.Errorf("pinned profile rejected: %v", err)
	}
}

func TestNewToLibraryAcceptsLookback(t *testing.T) {
	rec, _ := Get("new_to_library")
	if err := rec.Validate(json.RawMessage(`{"lookback_days":30}`)); err != nil {
		t.Errorf("good params rejected: %v", err)
	}
	if err := rec.Validate(json.RawMessage(`{}`)); err != nil {
		t.Errorf("empty config rejected: %v", err)
	}
}
