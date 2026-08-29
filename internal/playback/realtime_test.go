package playback

import (
	"encoding/json"
	"testing"
)

func TestNewChapterThumbnailReadyEvent(t *testing.T) {
	event, err := NewChapterThumbnailReadyEvent(
		"session-1",
		42,
		3,
		"https://example.com/thumb.jpg",
		"thumbhash",
	)
	if err != nil {
		t.Fatalf("NewChapterThumbnailReadyEvent() error = %v", err)
	}

	if event.Type != RealtimeMessageTypeEvent {
		t.Fatalf("event.Type = %q, want %q", event.Type, RealtimeMessageTypeEvent)
	}
	if event.Name != RealtimeEventChapterThumbnailReady {
		t.Fatalf("event.Name = %q, want %q", event.Name, RealtimeEventChapterThumbnailReady)
	}

	var payload ChapterThumbnailReadyPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.SessionID != "session-1" || payload.FileID != 42 || payload.ChapterIndex != 3 {
		t.Fatalf("payload = %#v, want session/file/chapter identifiers", payload)
	}
	if payload.ThumbnailURL != "https://example.com/thumb.jpg" {
		t.Fatalf("payload.ThumbnailURL = %q, want thumbnail URL", payload.ThumbnailURL)
	}
}

func TestNewMarkersUpdatedEvent(t *testing.T) {
	event, err := NewMarkersUpdatedEvent(
		"session-1",
		42,
		&TimeRangePayload{Start: 12, End: 75},
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("NewMarkersUpdatedEvent() error = %v", err)
	}
	if event.Name != RealtimeEventMarkersUpdated {
		t.Fatalf("event.Name = %q, want %q", event.Name, RealtimeEventMarkersUpdated)
	}

	var payload MarkersUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.SessionID != "session-1" || payload.FileID != 42 {
		t.Fatalf("payload = %#v, want session/file identifiers", payload)
	}
	if payload.Intro == nil || payload.Intro.Start != 12 || payload.Intro.End != 75 {
		t.Fatalf("payload.Intro = %#v, want intro range", payload.Intro)
	}
	if payload.Credits != nil {
		t.Fatalf("payload.Credits = %#v, want nil", payload.Credits)
	}
}

func TestParseCommandEnvelopeStillWorks(t *testing.T) {
	command, err := ParseCommandEnvelope([]byte(`{
		"type":"command",
		"command_id":"cmd-1",
		"session_id":"session-1",
		"name":"pause",
		"payload":{"reason":"test"}
	}`))
	if err != nil {
		t.Fatalf("ParseCommandEnvelope() error = %v", err)
	}
	if command.Type != RealtimeMessageTypeCommand {
		t.Fatalf("command.Type = %q, want %q", command.Type, RealtimeMessageTypeCommand)
	}
	if command.Name != CommandPause {
		t.Fatalf("command.Name = %q, want %q", command.Name, CommandPause)
	}
}

func TestNewPlanInvalidatedCommand(t *testing.T) {
	command, err := NewPlanInvalidatedCommand("session-1", "cmd-1", "plan-1", PlanInvalidatedVideoCopyUnsafe)
	if err != nil {
		t.Fatalf("NewPlanInvalidatedCommand() error = %v", err)
	}
	if command.Type != RealtimeMessageTypeCommand || command.Name != CommandPlanInvalidated {
		t.Fatalf("command = %#v, want a plan_invalidated command envelope", command)
	}
	var payload PlanInvalidatedPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.PlanID != "plan-1" || payload.Reason != PlanInvalidatedVideoCopyUnsafe {
		t.Fatalf("payload = %#v, want the invalidated plan and reason", payload)
	}
}

// The plan id is what lets a client ignore a command for a plan it has already
// replanned past, so an envelope without one must never be built.
func TestNewPlanInvalidatedCommandRequiresPlanAndReason(t *testing.T) {
	if _, err := NewPlanInvalidatedCommand("session-1", "cmd-1", "", PlanInvalidatedVideoCopyUnsafe); err == nil {
		t.Fatal("NewPlanInvalidatedCommand() with no plan id = nil error, want a rejection")
	}
	if _, err := NewPlanInvalidatedCommand("session-1", "cmd-1", "plan-1", ""); err == nil {
		t.Fatal("NewPlanInvalidatedCommand() with no reason = nil error, want a rejection")
	}
}

// A client advertising the command in its hello must validate: the closed
// command enum is the negotiation surface for the realtime channel.
func TestHelloAcceptsPlanInvalidatedCapability(t *testing.T) {
	hello := HelloEnvelope{
		Type:         RealtimeMessageTypeHello,
		SessionID:    "session-1",
		Client:       HelloClientInfo{Name: "silo-web", Version: "1.0.0"},
		Capabilities: HelloCapabilities{Commands: []CommandName{CommandPause, CommandPlanInvalidated}},
	}
	if err := hello.Validate(); err != nil {
		t.Fatalf("hello.Validate() = %v, want the plan_invalidated capability accepted", err)
	}
}
