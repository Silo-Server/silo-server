package jellycompat

import "testing"

// TestBareNameSelectsProfile: what an app is told about signing in with the
// bare account name matches what sign-in does. It catches the answer
// drifting from selectProfile after a profile set adds a profile.
func TestBareNameSelectsProfile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profiles []SignInProfile
		want     bool
	}{
		{"one profile named after the account", []SignInProfile{{Name: "Sam"}}, true},
		{"one profile with another name and no PIN", []SignInProfile{{Name: "Main"}}, true},
		{"a second unprotected profile added", []SignInProfile{{Name: "Main"}, {Name: "Kid"}}, false},
		{"a second unprotected profile, the first named after the account", []SignInProfile{{Name: "sam"}, {Name: "Kid"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := BareNameSelectsProfile("Sam", tc.profiles); got != tc.want {
				t.Fatalf("BareNameSelectsProfile = %v, want %v", got, tc.want)
			}
		})
	}
}
