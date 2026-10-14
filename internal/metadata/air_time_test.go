package metadata

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
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

func TestItemToMetadataResult_CarriesAirTime(t *testing.T) {
	airTime := "20:00"
	airTimezone := "America/New_York"
	releaseDate := "2024-01-02"
	result := itemToMetadataResult(&models.MediaItem{
		ContentID:   "series-1",
		Type:        "series",
		Title:       "Series",
		AirTime:     &airTime,
		AirTimezone: &airTimezone,
		ReleaseDate: &releaseDate,
	})

	if result.ReleaseDate != releaseDate {
		t.Fatalf("release_date = %q, want %q", result.ReleaseDate, releaseDate)
	}
	if got := result.AirTime; got != airTime {
		t.Fatalf("expected metadata air_time %q, got %q", airTime, got)
	}
	if result.AirTimezone != airTimezone {
		t.Fatalf("expected metadata air_timezone %q, got %q", airTimezone, result.AirTimezone)
	}
}

func TestMergeMetadata_CarriesAirTime(t *testing.T) {
	source := &MetadataResult{ReleaseDate: "2024-01-02"}
	target := &MetadataResult{}
	source.AirTime = "20:00"

	MergeMetadata(source, target, nil, MergeFillEmpty)

	if target.ReleaseDate != "2024-01-02" {
		t.Fatalf("release_date = %q, want 2024-01-02", target.ReleaseDate)
	}
	if got := target.AirTime; got != "20:00" {
		t.Fatalf("expected merged air_time 20:00, got %q", got)
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

func TestMergeGlobalMetadata_CarriesAirTime(t *testing.T) {
	source := &MetadataResult{}
	target := &MetadataResult{}
	source.AirTime = "20:00"

	MergeGlobalMetadata(source, target, nil, MergeFillEmpty)

	if got := target.AirTime; got != "20:00" {
		t.Fatalf("expected merged global air_time 20:00, got %q", got)
	}
}
