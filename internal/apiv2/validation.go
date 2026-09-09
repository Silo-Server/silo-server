package apiv2

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
)

// rejectNonNullableNulls enforces the shared omitted-versus-null rule for
// request members. Huma validates syntax and shape; this pass preserves the
// API contract's distinction between an omitted member and an explicit null.
func rejectNonNullableNulls(raw []byte, nullable map[string]bool) *Problem {
	// The framework already judged the syntax and shape; a document that does
	// not decode here has no members to judge.
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members)
	var errs []ProblemError
	for name, value := range members {
		if bytes.Equal(bytes.TrimSpace(value), jsonNull) && !nullable[name] {
			errs = append(errs, ProblemError{Location: locationBody + "." + name, Code: codeInvalidType, Detail: "null is not a value for this member; omit it to leave it unchanged"})
		}
	}
	if len(errs) == 0 {
		return nil
	}
	slices.SortFunc(errs, func(a, b ProblemError) int { return cmp.Compare(a.Location, b.Location) })
	return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").WithErrors(errs...)
}
