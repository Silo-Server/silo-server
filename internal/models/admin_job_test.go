package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAdminJobJSONUsesAPIFieldNames(t *testing.T) {
	job := AdminJob{
		ID:              "job-1",
		JobType:         "library_refresh",
		Status:          "running",
		CreatedByUserID: 7,
		RequestPayload:  json.RawMessage(`{"library_id":42}`),
		ResultPayload:   json.RawMessage(`{}`),
		Message:         "Refreshing metadata",
		ProgressCurrent: 3,
		ProgressTotal:   10,
		RequestedAt:     time.Unix(1_700_000_000, 0).UTC(),
		UpdatedAt:       time.Unix(1_700_000_001, 0).UTC(),
	}

	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	for _, key := range []string{
		"id",
		"job_type",
		"created_by_user_id",
		"request_payload",
		"progress_current",
		"progress_total",
		"requested_at",
		"updated_at",
	} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("marshaled job missing key %q in %s", key, data)
		}
	}

	for _, key := range []string{"ID", "JobType", "CreatedByUserID", "ProgressCurrent"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("marshaled job unexpectedly included key %q in %s", key, data)
		}
	}
}
