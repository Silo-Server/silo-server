package events

import (
	"sort"
	"sync"
	"time"
)

type ScanRunResult struct {
	New                    int    `json:"new"`
	Updated                int    `json:"updated"`
	Unchanged              int    `json:"unchanged"`
	Missing                int    `json:"missing"`
	FilesDeleted           int    `json:"files_deleted"`
	MembershipsRemoved     int    `json:"memberships_removed"`
	ItemsDeleted           int    `json:"items_deleted"`
	MatchedFiles           int    `json:"matched_files"`
	RetriedItems           int    `json:"retried_items"`
	StillUnmatchedWarnings int    `json:"still_unmatched_warnings"`
	Skipped                int    `json:"skipped"`
	Errors                 int    `json:"errors"`
	Phase                  string `json:"phase,omitempty"`
	Message                string `json:"message,omitempty"`
	CurrentScope           string `json:"current_scope,omitempty"`
	TotalFiles             int    `json:"total_files,omitempty"`
	FilesDiscovered        int    `json:"files_discovered,omitempty"`
	FilesProcessed         int    `json:"files_processed,omitempty"`
}

type ScanRun struct {
	ID           string         `json:"id"`
	LibraryID    int            `json:"library_id"`
	Mode         string         `json:"mode"`
	Path         string         `json:"path,omitempty"`
	Trigger      string         `json:"trigger"`
	Status       string         `json:"status"`
	StartedAt    *time.Time     `json:"started_at,omitempty"`
	CompletedAt  *time.Time     `json:"completed_at,omitempty"`
	ErrorMessage string         `json:"error_message,omitempty"`
	Result       *ScanRunResult `json:"result,omitempty"`
}

type ScanRegistry struct {
	mu      sync.RWMutex
	entries map[string]ScanRun
}

func NewScanRegistry() *ScanRegistry {
	return &ScanRegistry{entries: make(map[string]ScanRun)}
}

func (r *ScanRegistry) Upsert(run ScanRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[run.ID] = run
}

func (r *ScanRegistry) Get(id string) (ScanRun, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	run, ok := r.entries[id]
	return run, ok
}

func (r *ScanRegistry) ListActive() []ScanRun {
	return r.ListActiveLimit(0)
}

func (r *ScanRegistry) ListActiveLimit(limit int) []ScanRun {
	r.mu.RLock()
	defer r.mu.RUnlock()

	runs := make([]ScanRun, 0, len(r.entries))
	for _, run := range r.entries {
		if run.Status == "accepted" || run.Status == "running" {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		left := runs[i]
		right := runs[j]
		leftStarted := left.StartedAt != nil
		rightStarted := right.StartedAt != nil
		if leftStarted != rightStarted {
			return leftStarted
		}
		if leftStarted && !left.StartedAt.Equal(*right.StartedAt) {
			return left.StartedAt.Before(*right.StartedAt)
		}
		return left.ID < right.ID
	})
	if limit > 0 && len(runs) > limit {
		return runs[:limit]
	}
	return runs
}

func (r *ScanRegistry) MarkTerminal(run ScanRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[run.ID] = run
}

func (r *ScanRegistry) CancelLibrary(libraryID int, completedAt time.Time) []ScanRun {
	r.mu.Lock()
	defer r.mu.Unlock()

	cancelled := make([]ScanRun, 0)
	for id, run := range r.entries {
		if run.LibraryID != libraryID {
			continue
		}
		if run.Status != "accepted" && run.Status != "running" {
			continue
		}
		run.Status = "cancelled"
		run.CompletedAt = &completedAt
		r.entries[id] = run
		cancelled = append(cancelled, run)
	}
	return cancelled
}
