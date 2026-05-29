package metadata

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func metadataAirTimeField(t *testing.T, result *MetadataResult) reflect.Value {
	t.Helper()

	field := reflect.ValueOf(result).Elem().FieldByName("AirTime")
	if !field.IsValid() {
		t.Fatal("MetadataResult is missing AirTime")
	}
	return field
}

func setMetadataAirTime(t *testing.T, result *MetadataResult, value string) {
	t.Helper()

	field := metadataAirTimeField(t, result)
	if !field.CanSet() {
		t.Fatal("MetadataResult.AirTime is not settable")
	}
	field.SetString(value)
}

func getMetadataAirTime(t *testing.T, result *MetadataResult) string {
	t.Helper()

	return metadataAirTimeField(t, result).String()
}

func TestMetadataResultToItem_CarriesAirTime(t *testing.T) {
	result := &MetadataResult{
		HasMetadata: true,
		Title:       "Series",
	}
	setMetadataAirTime(t, result, "20:00")

	item := metadataResultToItem(result, "series")
	if item.AirTime == nil {
		t.Fatal("expected item air_time to be set")
	}
	if got := *item.AirTime; got != "20:00" {
		t.Fatalf("expected item air_time 20:00, got %q", got)
	}
}

func TestMetadataResultToItem_InfersAirTimezone(t *testing.T) {
	result := &MetadataResult{
		HasMetadata: true,
		Title:       "Series",
		Networks:    []string{"BBC One"},
	}
	setMetadataAirTime(t, result, "20:00")

	item := metadataResultToItem(result, "series")
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
	result := itemToMetadataResult(&models.MediaItem{
		ContentID:   "series-1",
		Type:        "series",
		Title:       "Series",
		AirTime:     &airTime,
		AirTimezone: &airTimezone,
	})

	if got := getMetadataAirTime(t, result); got != airTime {
		t.Fatalf("expected metadata air_time %q, got %q", airTime, got)
	}
	if result.AirTimezone != airTimezone {
		t.Fatalf("expected metadata air_timezone %q, got %q", airTimezone, result.AirTimezone)
	}
}

func TestMergeMetadata_CarriesAirTime(t *testing.T) {
	source := &MetadataResult{}
	target := &MetadataResult{}
	setMetadataAirTime(t, source, "20:00")

	MergeMetadata(source, target, nil, MergeFillEmpty)

	if got := getMetadataAirTime(t, target); got != "20:00" {
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
	setMetadataAirTime(t, source, "20:00")

	MergeGlobalMetadata(source, target, nil, MergeFillEmpty)

	if got := getMetadataAirTime(t, target); got != "20:00" {
		t.Fatalf("expected merged global air_time 20:00, got %q", got)
	}
}
