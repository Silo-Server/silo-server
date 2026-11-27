package auth

import "testing"

// The remedy a refusal names is one that works under the settings: a
// password only while local passwords are on or for a break-glass
// administrator, and linking a provider only while one can sign people in.
func TestWayInRemedy(t *testing.T) {
	for _, tc := range []struct {
		local, breakGlass, provider bool
		want                        string
	}{
		{true, false, true, "Give each a password, or link a sign-in provider account for each"},
		{false, false, true, "Link a sign-in provider account for each"},
		{false, true, true, "Link a sign-in provider account for each, or give a break-glass administrator a password"},
		{true, false, false, "Give each a password, or set up a sign-in provider people can use (an OAuth provider needs the server's public address) and link an account for each"},
		{false, false, false, "Set up a sign-in provider people can use (an OAuth provider needs the server's public address) and link an account for each"},
	} {
		if got := WayInRemedy(tc.local, tc.breakGlass, tc.provider); got != tc.want {
			t.Errorf("WayInRemedy(%v, %v, %v) = %q, want %q", tc.local, tc.breakGlass, tc.provider, got, tc.want)
		}
	}
}
