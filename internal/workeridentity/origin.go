package workeridentity

// SameOrigin compares validated authorities using the same canonical form as
// enrollment and request signing. Paths, queries and credentials remain invalid.
func SameOrigin(left, right string, allowHTTP bool) bool {
	a, err := ValidateOrigin(left, allowHTTP)
	if err != nil {
		return false
	}
	b, err := ValidateOrigin(right, allowHTTP)
	return err == nil && a == b
}
