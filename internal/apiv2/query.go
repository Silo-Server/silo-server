package apiv2

import (
	"net/url"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
)

// queryGuard enforces the query grammar Huma does not: invalid percent
// encoding and a scalar repeated more than once are malformed (400); a
// boolean is only the lowercase literals true and false (422, since Huma
// would otherwise accept 1, t, TRUE through strconv.ParseBool).
func queryGuard(ctx huma.Context, next func(huma.Context)) {
	r, w := humachi.Unwrap(ctx)
	raw := r.URL.RawQuery
	if raw == "" {
		next(ctx)
		return
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		writeProblem(w, r, NewProblem(TypeMalformedRequest, "The query string is not valid percent-encoded form data."))
		return
	}
	params := queryParams(ctx.Operation())
	var errs []ProblemError
	for name, vs := range values {
		p, known := params[name]
		if !known {
			continue // RejectUnknownQueryParameters reports it as 422
		}
		if len(vs) > 1 && !p.array {
			writeProblem(w, r, NewProblem(TypeMalformedRequest, "A scalar query parameter was repeated."))
			return
		}
		if p.boolean {
			for _, v := range vs {
				if v != "true" && v != "false" { //nolint:goconst // the JSON boolean literals
					errs = append(errs, ProblemError{Location: "query." + name, Code: codeInvalidType, Detail: "expected the lowercase literal true or false"})
				}
			}
		}
	}
	if len(errs) > 0 {
		p := NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").WithErrors(errs...)
		writeProblem(w, r, p)
		return
	}
	next(ctx)
}

type queryParam struct {
	array   bool
	boolean bool
}

// queryParams reads the declared query parameters off the OpenAPI operation.
func queryParams(op *huma.Operation) map[string]queryParam {
	out := map[string]queryParam{}
	for _, p := range op.Parameters {
		if p.In != "query" || p.Schema == nil {
			continue
		}
		out[p.Name] = queryParam{array: p.Schema.Type == huma.TypeArray, boolean: p.Schema.Type == huma.TypeBoolean}
	}
	return out
}
