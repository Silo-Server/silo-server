package events

import (
	"context"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type TaskObserver struct {
	Hub   *Hub
	mu    sync.Mutex
	state map[string]time.Time
}

func NewTaskObserver(hub *Hub) *TaskObserver {
	return &TaskObserver{
		Hub:   hub,
		state: make(map[string]time.Time),
	}
}

func (o *TaskObserver) TaskUpdated(info taskmanager.TaskInfo) {
	if o == nil || o.Hub == nil || info.Key == "" {
		return
	}

	now := time.Now().UTC()
	immediate := info.State != taskmanager.TaskStateRunning

	o.mu.Lock()
	lastSent := o.state[info.Key]
	if !immediate && !lastSent.IsZero() && now.Sub(lastSent) < 250*time.Millisecond {
		o.mu.Unlock()
		return
	}
	o.state[info.Key] = now
	o.mu.Unlock()

	_ = o.Hub.PublishJSON(
		context.Background(),
		ChannelTasks,
		"task.updated",
		info,
		PublishOptions{AdminOnly: true},
	)
}

type HistoryImportObserver struct {
	Hub   *Hub
	mu    sync.Mutex
	state map[string]time.Time
}

func NewHistoryImportObserver(hub *Hub) *HistoryImportObserver {
	return &HistoryImportObserver{
		Hub:   hub,
		state: make(map[string]time.Time),
	}
}

func (o *HistoryImportObserver) RunUpdated(run historyimport.Run) {
	if o == nil || o.Hub == nil || run.ID == "" {
		return
	}

	now := time.Now().UTC()
	eventName, immediate := historyImportEvent(run)

	o.mu.Lock()
	lastSent := o.state[run.ID]
	if !immediate && !lastSent.IsZero() && now.Sub(lastSent) < 250*time.Millisecond {
		o.mu.Unlock()
		return
	}
	if historyImportTerminal(run.Status) {
		// Run IDs are unique per import, so throttle state for finished runs
		// is dead weight the map would otherwise carry forever.
		delete(o.state, run.ID)
	} else {
		o.state[run.ID] = now
	}
	o.mu.Unlock()

	_ = o.Hub.PublishJSON(
		context.Background(),
		ChannelHistoryImport,
		eventName,
		run,
		PublishOptions{UserID: run.UserID, ProfileID: run.ProfileID},
	)
}

func historyImportTerminal(status string) bool {
	switch status {
	case historyimport.RunStatusCompleted, historyimport.RunStatusFailed, historyimport.RunStatusCancelled:
		return true
	default:
		return false
	}
}

func historyImportEvent(run historyimport.Run) (string, bool) {
	switch run.Status {
	case historyimport.RunStatusQueued:
		return "history_import.created", true
	case historyimport.RunStatusCompleted:
		return "history_import.completed", true
	case historyimport.RunStatusFailed:
		return "history_import.failed", true
	case historyimport.RunStatusCancelled:
		return "history_import.cancelled", true
	default:
		return "history_import.updated", false
	}
}
