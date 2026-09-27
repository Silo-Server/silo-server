package apiv2

import (
	"strconv"
	"strings"
)

// MaxBulkItems is the synchronous bulk envelope limit.
const MaxBulkItems = 100

// BulkCorrelation identifies a result by its position and optional caller reference.
type BulkCorrelation struct {
	Index     int     `json:"index" minimum:"0"`
	ClientRef *string `json:"client_ref,omitempty" minLength:"1" maxLength:"128"`
}

// BulkSummary counts every accepted item exactly once.
type BulkSummary struct {
	Total     int `json:"total" minimum:"0"`
	Succeeded int `json:"succeeded" minimum:"0"`
	Failed    int `json:"failed" minimum:"0"`
}

// BulkItemFailure is embedded failure data, not an HTTP Problem response.
type BulkItemFailure struct {
	Type   string         `json:"type" format:"uri"`
	Title  string         `json:"title"`
	Status int            `json:"status" minimum:"400" maximum:"599"`
	Detail string         `json:"detail"`
	Errors []ProblemError `json:"errors,omitempty"`
}

func bulkFailure(kind ProblemType, detail string) *BulkItemFailure {
	return &BulkItemFailure{Type: kind.URI(), Title: kind.Title, Status: kind.Status, Detail: detail}
}

// validateBulkIdentity rejects ambiguous envelopes before a caller starts work.
func validateBulkIdentity[T any](items []T, identity func(T) (string, *string), targetField string) *Problem {
	if len(items) == 0 || len(items) > MaxBulkItems {
		return NewProblem(TypeValidationFailed, "A bulk request must contain between 1 and 100 items.").WithErrors(ProblemError{Location: "body.items", Code: codeInvalid, Detail: "Expected between 1 and 100 items."})
	}
	targets, refs := make(map[string]bool, len(items)), make(map[string]bool, len(items))
	for i, item := range items {
		target, ref := identity(item)
		field := ""
		if strings.TrimSpace(target) == "" || targets[target] {
			field = targetField
		}
		if ref != nil && (strings.TrimSpace(*ref) == "" || refs[*ref]) {
			field = "client_ref"
		}
		if field != "" {
			return NewProblem(TypeValidationFailed, "Bulk targets and client references must be nonblank and unique.").WithErrors(ProblemError{Location: "body.items[" + strconv.Itoa(i) + "]." + field, Code: codeInvalid, Detail: "Expected a unique nonblank value."})
		}
		targets[target] = true
		if ref != nil {
			refs[*ref] = true
		}
	}
	return nil
}
