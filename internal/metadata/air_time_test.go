package metadata

import (
	"testing"
)

func TestMetadataResultToItem_InfersAirTimezone(t *testing.T) {
	result := &MetadataResult{
		HasMetadata: true,
		Title:       "Series",
		Networks:    []string{"BBC One"},
		ReleaseDate: "2024-01-02",
		ShowStatus:  "Returning Series",
	}
	result.AirTime = "20:00"

	item := metadataResultToItem(result, "series")
	if item.ReleaseDate == nil || *item.ReleaseDate != "2024-01-02" {
		t.Fatalf("release_date = %v, want 2024-01-02", item.ReleaseDate)
	}
	if item.ShowStatus != "returning" {
		t.Fatalf("show_status = %q, want returning", item.ShowStatus)
	}
	if item.AirTime == nil || *item.AirTime != "20:00" {
		t.Fatalf("air_time = %v, want 20:00", item.AirTime)
	}
	if item.AirTimezone == nil {
		t.Fatal("expected item air_timezone to be inferred")
	}
	if got := *item.AirTimezone; got != "Europe/London" {
		t.Fatalf("expected inferred air_timezone Europe/London, got %q", got)
	}
}

func TestMergeMetadata_RespectsAirScheduleLock(t *testing.T) {
	source := &MetadataResult{AirTime: "20:00", AirTimezone: "Europe/London"}
	target := &MetadataResult{AirTime: "21:00", AirTimezone: "America/New_York"}

	MergeMetadata(source, target, []MetadataField{FieldAirSchedule}, MergeReplaceUnlocked)

	if target.AirTime != "21:00" {
		t.Fatalf("expected locked air_time to remain 21:00, got %q", target.AirTime)
	}
	if target.AirTimezone != "America/New_York" {
		t.Fatalf("expected locked air_timezone to remain America/New_York, got %q", target.AirTimezone)
	}
}
