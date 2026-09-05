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
		op := Operation{
			Operation: humaOp(http.MethodPost, Prefix+"/probe/"+string(class), "probe"+string(class)+"Op", "probe", "probe"),
			Class:     class,
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
	// afterGet, when set, runs once after the next Get returns its row and
	// before the caller's compare-and-update: the test's way to land a
	// concurrent writer in the window the precondition exists to cover.
	afterGet func()
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
	hook := s.afterGet
	s.afterGet = nil
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
	row := guardedProbeRow{Name: name, Version: 1}
	s.rows[id] = row
	return row, nil
}

// Upsert is the unconditional create-or-replace: it never fails on a race,
// because a request that supplied no precondition has none to lose.
func (s *guardedProbeStore) Upsert(id string, name string) guardedProbeRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[id]
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
	delete(s.rows, id)
	return nil
}

type guardedProbeReadInput struct {
	ID          string `path:"id" doc:"Probe resource id"`
	IfNoneMatch string `header:"If-None-Match" doc:"Entity tags the client already holds"`
}

type guardedProbeWriteInput struct {
	ID      string `path:"id" doc:"Probe resource id"`
	IfMatch string `header:"If-Match" doc:"The resource's current ETag"`
	Body    struct {
		Name string `json:"name" minLength:"1"`
	}
}

type createOnlyProbeInput struct {
	ID          string `path:"id" doc:"Client-selected probe resource id"`
	IfNoneMatch string `header:"If-None-Match" doc:"\"*\" to refuse overwriting an existing resource"`
	Body        struct {
		Name string `json:"name" minLength:"1"`
	}
}

type guardedProbeDeleteInput struct {
	ID      string `path:"id" doc:"Probe resource id"`
	IfMatch string `header:"If-Match" doc:"The resource's current ETag"`
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
			current := RenderETag(row.Version, guardedProbeScope)
			out := &guardedProbeReadOutput{ETag: current.String(), Body: guardedProbeBody{ID: in.ID, Name: row.Name, Version: row.Version}}
			matched, p := EvaluateIfNoneMatch(in.IfNoneMatch, current)
			if p != nil {
				return nil, p
			}
			if matched {
				return NotModified(out, current), nil
			}
			return out, nil
		})
		Register(reg, Operation{
			Operation: humaOp(http.MethodPut, Prefix+"/probe/guarded/{id}", "putGuardedProbe", "probe", "guarded replace"),
			Class:     ClassPublic,
			Guarded:   true,
		}, func(_ context.Context, in *guardedProbeWriteInput) (*guardedProbeWriteOutput, error) {
			// Load first: a missing resource is 404 before any precondition.
			row, ok := store.Get(in.ID)
			if !ok {
				return nil, NewProblem(TypeNotFound, "No probe resource has this id.")
			}
			current := RenderETag(row.Version, guardedProbeScope)
			if p := EvaluateIfMatch(in.IfMatch, current); p != nil {
				return nil, p
			}
			// The precondition passed; domain state may still refuse.
			if row.Name == guardedProbeReservedName {
				return nil, NewProblem(TypeConflict, "A reserved probe resource cannot be replaced.")
			}
			updated, err := store.Update(in.ID, row.Version, in.Body.Name)
			for err != nil {
				// The compare-and-update lost a race with another writer.
				latest, exists := store.Get(in.ID)
				if !exists {
					// The winner deleted the row: nothing current to
					// advertise, so the 412 carries no ETag.
					return nil, StaleVersionProblem(EntityTag{})
				}
				if !IsOverwrite(in.IfMatch) {
					return nil, StaleVersionProblem(RenderETag(latest.Version, guardedProbeScope))
				}
				// "*" asked for a deliberate overwrite and the resource still
				// exists, so the precondition still holds: apply against the
				// latest version.
				updated, err = store.Update(in.ID, latest.Version, in.Body.Name)
			}
			return &guardedProbeWriteOutput{
				ETag: RenderETag(updated.Version, guardedProbeScope).String(),
				Body: guardedProbeBody{ID: in.ID, Name: updated.Name, Version: updated.Version},
			}, nil
		})
		Register(reg, Operation{
			Operation: humaOp(http.MethodDelete, Prefix+"/probe/guarded/{id}", "deleteGuardedProbe", "probe", "guarded delete"),
			Class:     ClassPublic,
			Guarded:   true,
		}, func(_ context.Context, in *guardedProbeDeleteInput) (*guardedProbeDeleteOutput, error) {
			row, ok := store.Get(in.ID)
			if !ok {
				return nil, NewProblem(TypeNotFound, "No probe resource has this id.")
			}
			current := RenderETag(row.Version, guardedProbeScope)
			if p := EvaluateIfMatch(in.IfMatch, current); p != nil {
				return nil, p
			}
			if err := store.Delete(in.ID, row.Version); err != nil {
				latest, exists := store.Get(in.ID)
				if !exists {
					return nil, StaleVersionProblem(EntityTag{})
				}
				if !IsOverwrite(in.IfMatch) {
					return nil, StaleVersionProblem(RenderETag(latest.Version, guardedProbeScope))
				}
				// "*": the resource still exists, so delete whatever is there.
				if err := store.Delete(in.ID, latest.Version); err != nil {
					return nil, StaleVersionProblem(EntityTag{})
				}
			}
			// The deleted representation has no ETag: 204 with no validator.
			return &guardedProbeDeleteOutput{}, nil
		})
		// The create-only probe: PUT at a client-selected id. Without
		// If-None-Match it creates or replaces; with "*" it refuses to
		// overwrite. It shares the store so a created row is then readable
		// and guardable through the other probes.
		Register(reg, Operation{
			Operation:  humaOp(http.MethodPut, Prefix+"/probe/created/{id}", "putCreatedProbe", "probe", "create-only put"),
			Class:      ClassPublic,
			CreateOnly: true,
		}, func(_ context.Context, in *createOnlyProbeInput) (*guardedProbeWriteOutput, error) {
			var existing *EntityTag
			row, ok := store.Get(in.ID)
			if ok {
				tag := RenderETag(row.Version, guardedProbeScope)
				existing = &tag
			}
			if p := EvaluateCreateOnly(in.IfNoneMatch, existing); p != nil {
				return nil, p
			}
			var updated guardedProbeRow
			if in.IfNoneMatch == "" {
				// No precondition: an ordinary create-or-replace that no
				// concurrent writer can make fail.
				updated = store.Upsert(in.ID, in.Body.Name)
			} else {
				// The precondition passed against the row read above; the
				// write re-checks it atomically, and a writer that landed in
				// between is the 412 the precondition exists to report.
				var err error
				if ok {
					updated, err = store.Update(in.ID, row.Version, in.Body.Name)
				} else {
					updated, err = store.Create(in.ID, in.Body.Name)
				}
				if err != nil {
					latest, _ := store.Get(in.ID)
					return nil, StaleVersionProblem(RenderETag(latest.Version, guardedProbeScope))
				}
			}
			return &guardedProbeWriteOutput{
				ETag: RenderETag(updated.Version, guardedProbeScope).String(),
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
