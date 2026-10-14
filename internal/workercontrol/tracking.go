package workercontrol

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type reportRegistration struct {
	tracker    *reportTracker
	work       string
	generation uint64
	info       nodesessions.SessionInfo
}

// PrepareSessionTracking reserves an exact local producer instance before its
// asynchronous monitoring task is scheduled. Runtime ownership, not another
// time-based reporting lease, determines whether the scope can register.
func (t *reportTracker) PrepareSessionTracking(ctx context.Context, info nodesessions.SessionInfo) transcodenode.SessionTracking {
	work, _ := ctx.Value(workContextKey{}).(string)
	generation, _ := ctx.Value(generationContextKey{}).(uint64)
	if work == "" {
		work = info.SessionID
	}
	t.runtime.mu.Lock()
	defer t.runtime.mu.Unlock()
	if generation == 0 {
		generation = t.runtime.generations[work]
	}
	if !t.liveScopeLocked(work, generation) {
		return nil
	}
	registration := &reportRegistration{tracker: t, work: work, generation: generation, info: info}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.registrations == nil {
		t.registrations = make(map[string]*reportRegistration)
	}
	if current := t.registrations[info.SessionID]; current != nil && current.work == work && current.generation > generation {
		return nil
	}
	t.registrations[info.SessionID] = registration
	// A new instance is pending. Its predecessor's monitoring receipt cannot be
	// presented as the new instance while the asynchronous task is still queued.
	delete(t.sessions, info.SessionID)
	delete(t.scopes, info.SessionID)
	return registration
}
func (t *reportTracker) liveScopeLocked(work string, generation uint64) bool {
	current := t.runtime.generations[work]
	return work != "" && generation > 0 && current <= generation && (current == generation || t.runtime.staged[work][generation] != nil)
}
func (p *reportRegistration) Track(context.Context) {
	t := p.tracker
	t.runtime.mu.Lock()
	defer t.runtime.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.registrations[p.info.SessionID] != p || !t.liveScopeLocked(p.work, p.generation) {
		return
	}
	if t.scopes == nil {
		t.scopes = make(map[string]workerwire.SessionReport)
	}
	t.sessions[p.info.SessionID] = p.info
	t.scopes[p.info.SessionID] = workerwire.SessionReport{WorkID: p.work, Generation: p.generation, Info: p.info}
}
func (p *reportRegistration) Remove(context.Context) {
	t := p.tracker
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.registrations[p.info.SessionID] != p {
		return
	}
	delete(t.registrations, p.info.SessionID)
	delete(t.sessions, p.info.SessionID)
	delete(t.scopes, p.info.SessionID)
}

// retireGenerationLocked is called with Runtime.mu held, in the same order as
// preparation and registration. It includes queued tasks that have no report.
func (t *reportTracker) retireGenerationLocked(work string, generation uint64, before bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, registration := range t.registrations {
		matches := registration.generation == generation
		if before {
			matches = registration.generation < generation
		}
		if registration.work == work && matches {
			delete(t.registrations, id)
			delete(t.sessions, id)
			delete(t.scopes, id)
		}
	}
}
