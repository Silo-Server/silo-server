package auth

import "strings"

// NormalizeUsername canonicalizes a username for storage and lookup by trimming
// surrounding whitespace. Case is preserved for display; case-insensitive
// matching is enforced by the citext column type in the database.
func NormalizeUsername(username string) string {
	return strings.TrimSpace(username)
}

// NormalizeEmail canonicalizes an email for storage and lookup by trimming
// surrounding whitespace. Case is preserved (not lowercased); case-insensitive
// matching is enforced by the citext column type in the database.
func NormalizeEmail(email string) string {
	return strings.TrimSpace(email)
}
