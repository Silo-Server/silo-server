package apiv2

import (
	"context"
	"net/http"
	"strconv"
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
