package triggers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

func TestIntervalTriggerFiresAfterStopStart(t *testing.T) {
	trigger := NewIntervalTrigger(taskmanager.TriggerConfig{
		Type:       taskmanager.TriggerTypeInterval,
		IntervalMs: 10,
	})

	trigger.Start(nil)
	trigger.Stop()
	trigger.Start(&taskmanager.ExecutionResult{CompletedAt: time.Now()})

	select {
	case <-trigger.C():
	case <-time.After(time.Second):
		t.Fatal("interval trigger did not fire after stop/start")
	}
}
