package watchstate

import (
	"strings"
	"testing"
)

func TestMarkPlayedBatch_SingleUpsert(t *testing.T) {
	sql, _ := buildMarkPlayedBatchSQL()
	if !strings.Contains(sql, "INSERT INTO user_watch_progress") {
		t.Fatalf("expected single INSERT INTO user_watch_progress; got:\n%s", sql)
	}
	if !strings.Contains(sql, "ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE") {
		t.Fatalf("expected ON CONFLICT upsert; got:\n%s", sql)
	}
	if !strings.Contains(sql, "FROM unnest($3::text[])") {
		t.Fatalf("expected UNNEST for batch IDs; got:\n%s", sql)
	}
	if !strings.Contains(sql, "completed = TRUE") {
		t.Fatalf("expected completed flag set; got:\n%s", sql)
	}
}
