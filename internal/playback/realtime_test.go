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
