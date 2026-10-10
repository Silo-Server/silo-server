package main

import "testing"

func TestParseAuthCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want authCommand
		ok   bool
	}{
		{"enable", []string{"local-login", "enable"}, authCommand{envFile: ".env"}, true},
		{"env file", []string{"local-login", "enable", "-env", "/etc/silo.env"}, authCommand{envFile: "/etc/silo.env"}, true},
		{"one account", []string{"local-login", "enable", "-user", "alice"}, authCommand{envFile: ".env", user: "alice"}, true},
		{"one account with a temporary password", []string{"local-login", "enable", "-user", "alice@example.test", "-temporary-password"},
			authCommand{envFile: ".env", user: "alice@example.test", temporaryPassword: true}, true},
		{"temporary password needs an account", []string{"local-login", "enable", "-temporary-password"}, authCommand{}, false},
		{"no subcommand", nil, authCommand{}, false},
		{"no action", []string{"local-login"}, authCommand{}, false},
		{"disable is not offered", []string{"local-login", "disable"}, authCommand{}, false},
		{"unknown subcommand", []string{"owner", "enable"}, authCommand{}, false},
		{"extra argument", []string{"local-login", "enable", "now"}, authCommand{}, false},
		{"unknown flag", []string{"local-login", "enable", "-force"}, authCommand{}, false},
	} {
		got, err := parseAuthCommand(tc.args)
		if tc.ok != (err == nil) || got != tc.want {
			t.Errorf("%s: got %+v err %v", tc.name, got, err)
		}
	}
}

func TestRandomTemporaryPassword(t *testing.T) {
	a, err := randomTemporaryPassword()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := randomTemporaryPassword()
	if len(a) != 24 || a == b {
		t.Fatalf("passwords %q %q", a, b)
	}
}
