package playback

import (
	"testing"
)

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
