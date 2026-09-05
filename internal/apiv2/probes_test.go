package apiv2

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Probe operations exercise every operation class and every framework rule
// through the real router. They exist only in tests.

type probeBody struct {
	Name    string          `json:"name" minLength:"1" doc:"A name"`
	Count   int             `json:"count,omitempty" minimum:"0" maximum:"10"`
	Kind    string          `json:"kind,omitempty" enum:"movie,series"`
	When    *Instant        `json:"when,omitempty"`
	Cleared NullableInstant `json:"cleared"`
	Note    Patch[string]   `json:"note,omitzero"`
}

type probeInput struct {
	LimitParam
	Tags   []string `query:"tags,explode" doc:"Repeated tags"`
	Flag   bool     `query:"flag"`
	Sort   string   `query:"sort"`
	Cursor string   `query:"cursor"`
	Body   probeBody
}

type probeEcho struct {
	Name     string          `json:"name"`
	Tags     []string        `json:"tags"`
	Labels   map[string]int  `json:"labels"`
	Flag     bool            `json:"flag"`
	When     *Instant        `json:"when,omitempty"`
	Cleared  NullableInstant `json:"cleared"`
	NoteSet  bool            `json:"note_set"`
	NoteNull bool            `json:"note_null"`
	Note     string          `json:"note"`
	UserID   int             `json:"user_id"`
	Profile  string          `json:"profile_id"`
	HasScope bool            `json:"has_scope"`
	Cursor   string          `json:"cursor"`
}

type probeOutput struct {
	Body probeEcho
}

// probeItemInput carries the two input kinds the framework validates outside
// the body: a typed path parameter and a typed header.
type probeItemInput struct {
	ID    int64 `path:"id" doc:"Numeric probe item id"`
	Count int   `header:"x-probe-count" doc:"Optional numeric header"`
}

// ProbeSmallBodyLimit is the override the small-body probe declares.
const ProbeSmallBodyLimit int64 = 256

type probeCursor struct {
	Offset int `json:"o"`
}

var probeCursorScope = CursorScope{OperationID: "listProbes", Security: "public", Sort: "name", Tiebreaker: "id"}

type probeListOutput struct {
	Body Collection[probeEcho]
}

func registerProbes(reg *Registry) {
	for _, class := range []Class{ClassPublic, ClassAuthenticated, ClassProfileScoped, ClassActingAdmin, ClassPermissionGated} {
		class := class
		// The probe echoes its body and stores nothing, so a duplicate
		// submission converges on the same (absent) state.
		op := Operation{
			Operation:   humaOp(http.MethodPost, Prefix+"/probe/"+string(class), "probe"+string(class)+"Op", "probe", "probe"),
			Class:       class,
			RetrySafety: RetrySafetyNaturalIdempotent,
		}
		op.OperationID = "probe" + lowerCamelFrom(string(class))
		if class == ClassPermissionGated {
			op.Permission = "marker_edit"
		}
		// Inert on a public operation, and Register refuses it there.
		op.DemoRestricted = class != ClassPublic
		Register(reg, op, probeHandler)
	}
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/probe/list", "listProbes", "probe", "list"),
		Class:     ClassPublic,
	}, func(ctx context.Context, in *struct {
		LimitParam
		Sort   string `query:"sort"`
		Cursor string `query:"cursor"`
	}) (*probeListOutput, error) {
		if _, p := ParseSort(in.Sort, []string{"name", "added_at"}); p != nil {
			return nil, p
		}
		if in.Cursor != "" {
			var pos probeCursor
			if p := cursors.Decode(probeCursorScope, in.Cursor, &pos); p != nil {
				return nil, p
			}
		}
		var items []probeEcho // deliberately nil: the envelope must still serialize []
		return &probeListOutput{Body: NewCollection(items)}, nil
	})
	// A typed path parameter and a typed header, so framework validation of
	// each has an operation to fail on.
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/probe/item/{id}", "getProbeItem", "probe", "typed path and header"),
		Class:     ClassPublic,
	}, func(_ context.Context, in *probeItemInput) (*probeOutput, error) {
		return &probeOutput{Body: probeEcho{Name: strconv.FormatInt(in.ID, 10), Tags: []string{}, Labels: map[string]int{}, Flag: in.Count > 0}}, nil
	})
	// An operation-level body cap below the package default.
	Register(reg, Operation{
		Operation:    humaOp(http.MethodPost, Prefix+"/probe/smallbody", "createProbeSmallBody", "probe", "small body"),
		Class:        ClassPublic,
		MaxBodyBytes: ProbeSmallBodyLimit,
		RetrySafety:  RetrySafetyNaturalIdempotent,
	}, probeHandler)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/probe/panic", "probePanic", "probe", "panic"),
		Class:     ClassPublic,
	}, func(context.Context, *struct{}) (*probeOutput, error) { panic("boom: secret detail") })
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/probe/slow", "probeSlow", "probe", "slow"),
		Class:     ClassPublic,
	}, func(ctx context.Context, _ *struct{}) (*probeOutput, error) {
		<-ctx.Done()
		return &probeOutput{Body: probeEcho{Name: "late", Tags: []string{}, Labels: map[string]int{}}}, nil
	})
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/probe/apperror", "probeAppError", "probe", "app error"),
		Class:     ClassPublic,
	}, func(context.Context, *struct{}) (*probeOutput, error) {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "body.name", Code: "required", Detail: "expected required property name to be present"})
	})
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/probe/ratelimited", "probeRateLimited", "probe", "429"),
		Class:     ClassPublic,
	}, func(context.Context, *struct{}) (*probeOutput, error) {
		return nil, NewProblem(TypeRateLimited, "Slow down.").WithRetryAfter(7)
	})
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/probe/unavailable", "probeUnavailable", "probe", "503"),
		Class:     ClassPublic,
	}, func(context.Context, *struct{}) (*probeOutput, error) {
		return nil, NewProblem(TypeDependencyUnavailable, "The database is unavailable.").WithRetryAfter(3)
	})
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/probe/zero", "probeZeroInstant", "probe", "zero"),
		Class:     ClassPublic,
	}, func(context.Context, *struct{}) (*probeOutput, error) {
		z := Instant{}
		return &probeOutput{Body: probeEcho{Tags: []string{}, Labels: map[string]int{}, When: &z}}, nil
	})
	registerGuardedProbes(newGuardedProbeStore())(reg)
}

// The guarded probe: an in-memory versioned resource behind the real router,
// exercising every optimistic-concurrency outcome the contract ratifies.

// guardedProbeScope is the representation scope the probe's ETag carries.
const guardedProbeScope = "probe.guarded"

// guardedProbeReservedName is the stored name that makes a PUT conflict with
// domain state after its precondition passed (the 409 case).
const guardedProbeReservedName = "reserved"

type guardedProbeRow struct {
	Name    string
	Version int64
}

// guardedProbeStore is the storage side of the probe. Update and Delete are
// the compare-and-update: they return ErrStaleVersion when the expected
// version is no longer current, the way a `WHERE version = $expected` write
// reports zero rows.
type guardedProbeStore struct {
	mu   sync.Mutex
	rows map[string]guardedProbeRow
	// lastVersion remembers the version a deleted id reached, so a
	// recreation continues the sequence instead of restarting at 1: an
	// ETag minted for the old resource must never validate the new one.
	// A real store gets the same guarantee from a monotonic version source
	// that survives the row (a sequence, or a generation column).
	lastVersion map[string]int64
	// afterGet runs after each of the next afterGetRemaining Gets return
	// their row and before the caller's compare-and-update: the test's way
	// to land a concurrent writer in the window the precondition exists to
	// cover, once or several times in a row.
	afterGet          func()
	afterGetRemaining int
}

// raceNextGets arms afterGet for the next n Gets.
func (s *guardedProbeStore) raceNextGets(n int, fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afterGet, s.afterGetRemaining = fn, n
}

func newGuardedProbeStore() *guardedProbeStore {
	return &guardedProbeStore{rows: map[string]guardedProbeRow{
		"a":        {Name: "alpha", Version: 1},
		"reserved": {Name: guardedProbeReservedName, Version: 1},
	}}
}

func (s *guardedProbeStore) Get(id string) (guardedProbeRow, bool) {
	s.mu.Lock()
	row, ok := s.rows[id]
	var hook func()
	if s.afterGetRemaining > 0 {
		s.afterGetRemaining--
		hook = s.afterGet
	}
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return row, ok
}

func (s *guardedProbeStore) Update(id string, expected int64, name string) (guardedProbeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok || row.Version != expected {
		return guardedProbeRow{}, ErrStaleVersion
	}
	row.Name = name
	row.Version++
	s.rows[id] = row
	return row, nil
}

// Create stores a new row at a client-selected id; a row already there is
// left alone and reported as ErrStaleVersion, the same sentinel a
// create-only insert races into when another writer lands first.
func (s *guardedProbeStore) Create(id string, name string) (guardedProbeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[id]; ok {
		return guardedProbeRow{}, ErrStaleVersion
	}
	row := guardedProbeRow{Name: name, Version: s.lastVersion[id] + 1}
	s.rows[id] = row
	return row, nil
}

// Upsert is the unconditional create-or-replace: it never fails on a race,
// because a request that supplied no precondition has none to lose.
func (s *guardedProbeStore) Upsert(id string, name string) guardedProbeRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok {
		row.Version = s.lastVersion[id]
	}
	row.Name = name
	row.Version++
	s.rows[id] = row
	return row
}

func (s *guardedProbeStore) Delete(id string, expected int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok || row.Version != expected {
		return ErrStaleVersion
	}
	if s.lastVersion == nil {
		s.lastVersion = map[string]int64{}
	}
	s.lastVersion[id] = row.Version
	delete(s.rows, id)
	return nil
}

type guardedProbeReadInput struct {
	ID          string `path:"id" doc:"Probe resource id"`
	IfMatch     string `header:"If-Match" doc:"Optional first precondition, evaluated before If-None-Match"`
	IfNoneMatch string `header:"If-None-Match" doc:"Entity tags the client already holds"`
}

type guardedProbeWriteInput struct {
	ID          string `path:"id" doc:"Probe resource id"`
	IfMatch     string `header:"If-Match" doc:"The resource's current ETag"`
	IfNoneMatch string `header:"If-None-Match" doc:"Optional second precondition, evaluated after If-Match"`
	Body        struct {
		Name string `json:"name" minLength:"1"`
	}
}

type createOnlyProbeInput struct {
	ID          string `path:"id" doc:"Client-selected probe resource id"`
	IfMatch     string `header:"If-Match" doc:"Optional first precondition, evaluated before If-None-Match"`
	IfNoneMatch string `header:"If-None-Match" doc:"\"*\" to refuse overwriting an existing resource"`
	Body        struct {
		Name string `json:"name" minLength:"1"`
	}
}

type guardedProbeDeleteInput struct {
	ID          string `path:"id" doc:"Probe resource id"`
	IfMatch     string `header:"If-Match" doc:"The resource's current ETag"`
	IfNoneMatch string `header:"If-None-Match" doc:"Optional second precondition, evaluated after If-Match"`
}

type guardedProbeBody struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

type guardedProbeReadOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   guardedProbeBody
}

type guardedProbeWriteOutput struct {
	ETag string `header:"ETag"`
	Body guardedProbeBody
}

// guardedProbeDeleteOutput declares no ETag: the deleted representation has
// no validator and Register refuses one on a guarded DELETE.
type guardedProbeDeleteOutput struct{}

func registerGuardedProbes(store *guardedProbeStore) func(*Registry) {
	return func(reg *Registry) {
		Register(reg, Operation{
			Operation:   humaOp(http.MethodGet, Prefix+"/probe/guarded/{id}", "getGuardedProbe", "probe", "conditional read"),
			Class:       ClassPublic,
			Conditional: true,
		}, func(_ context.Context, in *guardedProbeReadInput) (*guardedProbeReadOutput, error) {
			row, ok := store.Get(in.ID)
			if !ok {
				return nil, NewProblem(TypeNotFound, "No probe resource has this id.")
			}
			current := RenderETag(guardedProbeScope, in.ID, row.Version)
			out := &guardedProbeReadOutput{ETag: current.String(), Body: guardedProbeBody{ID: in.ID, Name: row.Name, Version: row.Version}}
			matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, current)
			if p != nil {
				return nil, p
			}
			if matched {
				return NotModified(out, current), nil
			}
			return out, nil
		})
		Register(reg, Operation{
			Operation:   humaOp(http.MethodPut, Prefix+"/probe/guarded/{id}", "putGuardedProbe", "probe", "guarded replace"),
			Class:       ClassPublic,
			Guarded:     true,
			RetrySafety: RetrySafetyNaturalIdempotent,
		}, func(_ context.Context, in *guardedProbeWriteInput) (*guardedProbeWriteOutput, error) {
			// Every attempt runs the whole sequence against the row it
			// loaded: 404 before any precondition, then both preconditions
			// in RFC order, then domain state, then the compare-and-update.
			// A lost race under an exact If-Match is 412; under "*" the
			// resource still existing keeps that precondition true, but the
			// If-None-Match and the domain rule are judged again against
			// the latest row before the write is retried.
			for attempt := 0; ; attempt++ {
				row, ok := store.Get(in.ID)
				if !ok {
					if attempt == 0 {
						return nil, NewProblem(TypeNotFound, "No probe resource has this id.")
					}
					// The race winner deleted the row: nothing current to
					// advertise, so the 412 carries no ETag.
					return nil, StaleVersionProblem(EntityTag{})
				}
				current := RenderETag(guardedProbeScope, in.ID, row.Version)
				if attempt > 0 && !IsOverwrite(in.IfMatch) {
					return nil, StaleVersionProblem(current)
				}
				if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, current); p != nil {
					return nil, p
				}
				if row.Name == guardedProbeReservedName {
					return nil, NewProblem(TypeConflict, "A reserved probe resource cannot be replaced.")
				}
				updated, err := store.Update(in.ID, row.Version, in.Body.Name)
				if err != nil {
					continue
				}
				return &guardedProbeWriteOutput{
					ETag: RenderETag(guardedProbeScope, in.ID, updated.Version).String(),
					Body: guardedProbeBody{ID: in.ID, Name: updated.Name, Version: updated.Version},
				}, nil
			}
		})
		Register(reg, Operation{
			Operation:   humaOp(http.MethodDelete, Prefix+"/probe/guarded/{id}", "deleteGuardedProbe", "probe", "guarded delete"),
			Class:       ClassPublic,
			Guarded:     true,
			RetrySafety: RetrySafetyNaturalIdempotent,
		}, func(_ context.Context, in *guardedProbeDeleteInput) (*guardedProbeDeleteOutput, error) {
			// The same shape as the guarded PUT: every attempt loads the
			// row and runs the whole sequence, so a wildcard retry judges
			// If-None-Match again against the latest row before deleting.
			for attempt := 0; ; attempt++ {
				row, ok := store.Get(in.ID)
				if !ok {
					if attempt == 0 {
						return nil, NewProblem(TypeNotFound, "No probe resource has this id.")
					}
					// The race winner deleted it: nothing current to advertise.
					return nil, StaleVersionProblem(EntityTag{})
				}
				current := RenderETag(guardedProbeScope, in.ID, row.Version)
				if attempt > 0 && !IsOverwrite(in.IfMatch) {
					return nil, StaleVersionProblem(current)
				}
				if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, current); p != nil {
					return nil, p
				}
				if err := store.Delete(in.ID, row.Version); err != nil {
					continue
				}
				// The deleted representation has no ETag: 204 with no validator.
				return &guardedProbeDeleteOutput{}, nil
			}
		})
		// The create-only probe: PUT at a client-selected id. Without
		// If-None-Match it creates or replaces; with "*" it refuses to
		// overwrite. It shares the store so a created row is then readable
		// and guardable through the other probes.
		Register(reg, Operation{
			Operation:   humaOp(http.MethodPut, Prefix+"/probe/created/{id}", "putCreatedProbe", "probe", "create-only put"),
			Class:       ClassPublic,
			CreateOnly:  true,
			RetrySafety: RetrySafetyUniqueConstraint, // client-selected id; the store refuses a second create
		}, func(_ context.Context, in *createOnlyProbeInput) (*guardedProbeWriteOutput, error) {
			var existing *EntityTag
			row, ok := store.Get(in.ID)
			if ok {
				tag := RenderETag(guardedProbeScope, in.ID, row.Version)
				existing = &tag
			}
			if p := EvaluateCreateOnlyPreconditions(in.IfMatch, in.IfNoneMatch, existing); p != nil {
				return nil, p
			}
			var updated guardedProbeRow
			if in.IfNoneMatch == "" && in.IfMatch == "" {
				// No precondition at all: an ordinary create-or-replace that
				// no concurrent writer can make fail.
				updated = store.Upsert(in.ID, in.Body.Name)
			} else {
				// Either precondition passed against the state read above;
				// the write re-checks that state atomically. A writer that
				// landed in between does not by itself falsify the
				// preconditions (If-Match: * still holds while the resource
				// exists; a non-matching If-None-Match may still not match),
				// so both are evaluated again against the latest state and
				// the write retried while they hold.
				for {
					var err error
					if ok {
						updated, err = store.Update(in.ID, row.Version, in.Body.Name)
					} else {
						updated, err = store.Create(in.ID, in.Body.Name)
					}
					if err == nil {
						break
					}
					row, ok = store.Get(in.ID)
					existing = nil
					if ok {
						tag := RenderETag(guardedProbeScope, in.ID, row.Version)
						existing = &tag
					}
					if p := EvaluateCreateOnlyPreconditions(in.IfMatch, in.IfNoneMatch, existing); p != nil {
						return nil, p
					}
				}
			}
			return &guardedProbeWriteOutput{
				ETag: RenderETag(guardedProbeScope, in.ID, updated.Version).String(),
				Body: guardedProbeBody{ID: in.ID, Name: updated.Name, Version: updated.Version},
			}, nil
		})
	}
}

func lowerCamelFrom(snake string) string {
	out := []byte{}
	up := false
	for i := 0; i < len(snake); i++ {
		c := snake[i]
		if c == '_' {
			up = true
			continue
		}
		if up && c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		up = false
		out = append(out, c)
	}
	return string(out)
}

func probeHandler(ctx context.Context, in *probeInput) (*probeOutput, error) {
	claims := claimsFrom(ctx)
	echo := probeEcho{
		Name:     in.Body.Name,
		Tags:     NonNil(in.Tags),
		Labels:   NonNilMap[string, int](nil),
		Flag:     in.Flag,
		When:     in.Body.When,
		Cleared:  in.Body.Cleared,
		NoteSet:  in.Body.Note.Present,
		NoteNull: in.Body.Note.Null,
		Note:     in.Body.Note.Value,
		Profile:  profileFrom(ctx),
		HasScope: hasScope(ctx),
		Cursor:   in.Cursor,
	}
	if claims != nil {
		echo.UserID = claims.UserID
	}
	_ = time.Now
	return &probeOutput{Body: echo}, nil
}
