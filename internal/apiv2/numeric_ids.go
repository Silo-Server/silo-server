package apiv2

import "strconv"

// positive64 retains the full database key range on every server architecture.
func (id ID) positive64(location string) (int64, *Problem) {
	n, err := strconv.ParseInt(string(id), 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != string(id) {
		return 0, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: location, Code: codeInvalid, Detail: "expected a positive integer identifier"})
	}
	return n, nil
}
