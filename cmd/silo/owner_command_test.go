package main

import "testing"

func TestParseOwnerCommand(t *testing.T) {
	for _, tc := range []struct {
		name          string
		args          []string
		env, username string
		ok            bool
	}{
		{"username", []string{"set", "alice"}, ".env", "alice", true},
		{"env file", []string{"set", "-env", "/etc/silo.env", "alice"}, "/etc/silo.env", "alice", true},
		{"no subcommand", nil, "", "", false},
		{"unknown subcommand", []string{"show", "alice"}, "", "", false},
		{"no username", []string{"set"}, "", "", false},
		{"two usernames", []string{"set", "alice", "bob"}, "", "", false},
		{"unknown flag", []string{"set", "-force", "alice"}, "", "", false},
	} {
		env, username, err := parseOwnerCommand(tc.args)
		if tc.ok != (err == nil) || env != tc.env || username != tc.username {
			t.Errorf("%s: env %q username %q err %v", tc.name, env, username, err)
		}
	}
}
