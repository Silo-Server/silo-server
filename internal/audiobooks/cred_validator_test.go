package audiobooks

import "testing"

func TestSplitUserProfile(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantUser    string
		wantProfile string
	}{
		{
			name:        "plain username",
			input:       "alice",
			wantUser:    "alice",
			wantProfile: "",
		},
		{
			name:        "user#profile",
			input:       "alice#kids",
			wantUser:    "alice",
			wantProfile: "kids",
		},
		{
			name:        "trims surrounding whitespace",
			input:       "  alice#kids  ",
			wantUser:    "alice",
			wantProfile: "kids",
		},
		{
			name:        "trims inner whitespace around hash",
			input:       "alice # kids",
			wantUser:    "alice",
			wantProfile: "kids",
		},
		{
			name:        "trailing hash with no profile collapses to plain user",
			input:       "alice#",
			wantUser:    "alice",
			wantProfile: "",
		},
		{
			name:        "empty input stays empty",
			input:       "",
			wantUser:    "",
			wantProfile: "",
		},
		{
			name: "multiple hashes — split on the LAST one so a profile name " +
				"that legitimately starts with a hash (rare but possible) " +
				"doesn't get misinterpreted as the user portion",
			input:       "alice#kids#beta",
			wantUser:    "alice#kids",
			wantProfile: "beta",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotUser, gotProfile := splitUserProfile(tc.input)
			if gotUser != tc.wantUser {
				t.Errorf("user = %q, want %q", gotUser, tc.wantUser)
			}
			if gotProfile != tc.wantProfile {
				t.Errorf("profile = %q, want %q", gotProfile, tc.wantProfile)
			}
		})
	}
}
