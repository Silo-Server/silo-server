package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPublishEventJobSanitizesStorageTransition(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	job := &models.AdminJob{
		ID: "transition", JobType: "storage_transition", Status: "failed",
		RequestPayload: json.RawMessage(`{"secret":"private request"}`),
		ResultPayload:  json.RawMessage(`{"phase":"failed","verified_objects":3,"failure_category":"target_check_failed","target_identity":"private target"}`),
		Message:        "private object", ErrorMessage: "private endpoint",
	}
	publishEventJob(t.Context(), hub, "job.failed", job)
	select {
	case event := <-events:
		payload := string(event.Data)
		for _, private := range []string{"private request", "private target", "private object", "private endpoint"} {
			if strings.Contains(payload, private) {
				t.Fatalf("published storage job leaked %q: %s", private, payload)
			}
		}
		if !event.AdminOnly || !strings.Contains(payload, `"failure_category":"target_check_failed"`) {
			t.Fatalf("published storage job = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job event")
	}
}
