package triggers

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// The task manager rearms triggers with Stop and Start when a run ends. A
// startup trigger installed during that run must still fire once.
func TestStartupTriggerRearmedBeforeFiringStillFires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := NewStartupTrigger(taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeStartup})
		tr.Start(nil)
		time.Sleep(startupDelay / 2) //nolint:forbidigo // synctest fake clock
		tr.Stop()
		tr.Start(nil)
		select {
		case <-tr.C():
		case <-time.After(time.Minute):
			t.Fatal("a startup trigger stopped before it fired never fired")
		}
		tr.Stop()
		tr.Start(nil)
		select {
		case <-tr.C():
			t.Fatal("a startup trigger fired a second time")
		case <-time.After(time.Minute):
		}
		tr.Stop()
	})
}
