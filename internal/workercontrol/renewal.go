package workercontrol

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

const sourceRenewInterval = 45 * time.Second
const sourceRenewTimeout = 2 * time.Second
const sourceRenewParallelism = 8
const maxSourceRenewals = 128
const maxFailedSourceScopes = 1024

type sourceScope struct {
	work       string
	generation uint64
}
type sourceRenewal struct {
	handle *workerinput.Handle
	source workerwire.Source
	next   time.Time
}

// The registry retains the full original signed scope omitted from the local
// FFmpeg handle. Renewal never follows reply URLs or changes source identity.
type sourceRenewals struct {
	runtime *Runtime
	mu      sync.Mutex
	entries map[*workerinput.Handle]*sourceRenewal
	failed  map[sourceScope]bool
	fenced  bool
}

func newSourceRenewals(runtime *Runtime) *sourceRenewals {
	return &sourceRenewals{runtime: runtime, entries: make(map[*workerinput.Handle]*sourceRenewal), failed: make(map[sourceScope]bool)}
}

func (r *Runtime) workAllowed(work string, generation uint64) bool {
	if r.renewals != nil && r.renewals.blocked(work, generation) {
		return false
	}
	return r.authority.Allowed(work, generation)
}

func (s *sourceRenewals) blocked(work string, generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fenced || s.failed[sourceScope{work, generation}]
}

func (r *Runtime) trackSourceRenewals(sources map[string]workerwire.Source, handles map[string]*workerinput.Handle) error {
	s := r.renewals
	if s == nil {
		return errors.New("worker source renewal unavailable")
	}
	s.prune()
	for name, h := range handles {
		original, ok := sources[name]
		if !ok || !sameHandleSource(h.Descriptor(), original) {
			return errors.New("worker source renewal descriptor mismatch")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	newEntries := 0
	for _, h := range handles {
		if s.entries[h] == nil {
			newEntries++
		}
	}
	if len(s.entries)+newEntries > maxSourceRenewals {
		return errors.New("worker source renewal capacity reached")
	}
	for name, h := range handles {
		original := sources[name]
		if s.fenced || s.failed[sourceScope{original.WorkID, original.Generation}] {
			return errors.New("worker source lease fenced")
		}
		if previous := s.entries[h]; previous != nil && previous.source != original {
			return errors.New("worker source descriptor changed")
		}
	}
	for name, h := range handles {
		if s.entries[h] == nil {
			s.entries[h] = &sourceRenewal{handle: h, source: sources[name], next: time.Now().Add(sourceRenewInterval)}
		}
	}
	return nil
}

func sameHandleSource(local workerinput.Source, wire workerwire.Source) bool {
	return local.ID == wire.Ref.ID && local.Revision == wire.Ref.Revision && local.URL == wire.URL && local.Grant == wire.Grant && local.ETag == wire.ETag && local.Size == wire.Size && local.WorkID == wire.WorkID && local.Generation == wire.Generation
}

func (s *sourceRenewals) reconcile(snapshot workerwire.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for scope := range s.failed {
		if snapshot.Active[scope.work] != scope.generation && snapshot.Pending[scope.work] != scope.generation {
			delete(s.failed, scope)
		}
	}
}

func (s *sourceRenewals) revoke(work string, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, entry := range s.entries {
		if entry.source.WorkID == work && entry.source.Generation == generation {
			delete(s.entries, h)
		}
	}
}

func (s *sourceRenewals) fail(entry *sourceRenewal) {
	s.mu.Lock()
	// A completed preparation or replaced source has retired this request.
	// Check ownership and install the fence under the same retirement lock.
	if s.entries[entry.handle] != entry {
		s.mu.Unlock()
		return
	}
	work, generation := entry.source.WorkID, entry.source.Generation
	if len(s.failed) >= maxFailedSourceScopes && !s.failed[sourceScope{work, generation}] {
		s.fenced = true
	} else {
		s.failed[sourceScope{work, generation}] = true
	}
	fenced := s.fenced
	s.mu.Unlock()
	s.runtime.revoked(work, generation)
	if fenced {
		// A core that leaves too many failed jobs active cannot grow permanent
		// fences without bound. Stop all producers and require worker restart.
		s.runtime.mu.Lock()
		scopes := make(map[sourceScope]bool)
		for work, generation := range s.runtime.generations {
			scopes[sourceScope{work, generation}] = true
		}
		for work, pending := range s.runtime.staged {
			for generation := range pending {
				scopes[sourceScope{work, generation}] = true
			}
		}
		s.runtime.mu.Unlock()
		for scope := range scopes {
			s.runtime.revoked(scope.work, scope.generation)
		}
	}
}

func (s *sourceRenewals) loop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.runtime.ctx.Done():
			return
		case now := <-ticker.C:
			s.prune()
			s.renewDue(s.runtime.ctx, now)
		}
	}
}

func (s *sourceRenewals) renewDue(ctx context.Context, now time.Time) {
	s.mu.Lock()
	entries := make([]*sourceRenewal, 0, len(s.entries))
	for _, entry := range s.entries {
		if !now.Before(entry.next) {
			entries = append(entries, entry)
		}
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	slots := make(chan struct{}, sourceRenewParallelism)
	for _, entry := range entries {
		if !s.runtime.inputs.Allowed(entry.handle.URL()) {
			s.remove(entry)
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Go(func() {
			defer func() { <-slots }()
			requestCtx, cancel := context.WithTimeout(ctx, sourceRenewTimeout)
			defer cancel()
			if err := s.runtime.renewSource(requestCtx, entry.source); err != nil {
				if ctx.Err() == nil {
					s.fail(entry)
				}
				return
			}
			s.mu.Lock()
			if current := s.entries[entry.handle]; current == entry {
				current.next = now.Add(sourceRenewInterval)
			}
			s.mu.Unlock()
		})
	}
	wg.Wait()
}

func (s *sourceRenewals) remove(entry *sourceRenewal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[entry.handle] == entry {
		delete(s.entries, entry.handle)
	}
}

// A prepared artifact no longer needs source authority. Retain its generation
// for locally verified output grants without retaining source handles/pins.
func (r *Runtime) completeSourceWork(work string, generation uint64, handles map[string]*workerinput.Handle) {
	// Retire in-flight source responses before promoting output-only work.
	r.renewals.revoke(work, generation)
	r.mu.Lock()
	pending := r.takeStagedLocked(work, generation)
	if current := r.generations[work]; current <= generation {
		if current == generation {
			delete(r.handles, work)
		}
		r.generations[work] = generation
	}
	r.mu.Unlock()
	closeHandles(pending)
	closeHandles(handles)
}

func (s *sourceRenewals) prune() {
	s.mu.Lock()
	entries := make([]*sourceRenewal, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	// Allowed calls workAllowed, so never hold the registry lock here.
	for _, entry := range entries {
		if !s.runtime.inputs.Allowed(entry.handle.URL()) {
			s.remove(entry)
		}
	}
}

func (r *Runtime) renewSource(ctx context.Context, original workerwire.Source) error {
	path, err := sourceRenewPath(original.Grant)
	if err != nil {
		return err
	}
	var next workerwire.Source
	var scope workeridentity.Grant
	if err := r.callGrant(ctx, http.MethodPost, path, map[string]any{"work_id": original.WorkID, generationField: original.Generation}, workeridentity.DomainSourceGrant, &next, &scope); err != nil {
		return err
	}
	if scope.WorkID != original.WorkID || scope.Generation != original.Generation || next != original {
		return errors.New("worker source renewal scope or revision mismatch")
	}
	return nil
}

func sourceRenewPath(grant string) (string, error) {
	if grant == "" || len(grant) > 160 {
		return "", errors.New("invalid source renewal grant")
	}
	for _, char := range grant {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return "", errors.New("invalid source renewal grant")
		}
	}
	return "/sources/" + url.PathEscape(grant) + "/renew", nil
}

func sourceRenewHeader(path string) (string, error) {
	grant := strings.TrimSuffix(strings.TrimPrefix(path, "/sources/"), "/renew")
	validated, err := sourceRenewPath(grant)
	if err != nil || validated != path {
		return "", errors.New("invalid source renewal path")
	}
	return grant, nil
}
