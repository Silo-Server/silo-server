package triggers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

func TestStartupTriggerClearsNextRunAfterFire(t *testing.T) {
	trigger := NewStartupTrigger(taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeStartup})
	trigger.delay = time.Millisecond

	trigger.Start(nil)
	if trigger.NextRunTime().IsZero() {
		t.Fatal("expected startup trigger to report next run before firing")
	}

	select {
	case <-trigger.C():
	case <-time.After(time.Second):
		t.Fatal("startup trigger did not fire")
	}

	if next := trigger.NextRunTime(); !next.IsZero() {
		t.Fatalf("next run after fire = %s, want zero", next.Format(time.RFC3339Nano))
	}

	trigger.Start(nil)
	if next := trigger.NextRunTime(); !next.IsZero() {
		t.Fatalf("next run after rearm = %s, want zero", next.Format(time.RFC3339Nano))
	}
}

func TestStartupTriggerClearsNextRunWhenStopped(t *testing.T) {
	trigger := NewStartupTrigger(taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeStartup})
	trigger.delay = time.Second

	trigger.Start(nil)
	if trigger.NextRunTime().IsZero() {
		t.Fatal("expected startup trigger to report next run before stop")
	}

	trigger.Stop()
	if next := trigger.NextRunTime(); !next.IsZero() {
		t.Fatalf("next run after stop = %s, want zero", next.Format(time.RFC3339Nano))
	}
}
