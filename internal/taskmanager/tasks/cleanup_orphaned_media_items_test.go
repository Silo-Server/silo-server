package tasks

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// The orphan sweep's safety check lists the tables that can reference an item,
// so it must stay an explicit admin action. The manager reads the flag through
// an optional interface, so a renamed or dropped method still compiles and
// would quietly let saved triggers schedule the deletion.
func TestCleanupOrphanedMediaItemsTaskIsManualOnly(t *testing.T) {
	task := NewCleanupOrphanedMediaItemsTask(nil)
	manual, ok := any(task).(taskmanager.ManualOnlyTask)
	if !ok || !manual.ManualOnly() {
		t.Fatal("orphaned media cleanup must be manual-only")
	}
	if triggers := task.DefaultTriggers(); len(triggers) != 0 {
		t.Fatalf("default triggers = %v, want none", triggers)
	}
}
