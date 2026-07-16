package main

import "testing"

func TestInternalBaseURLFromListen(t *testing.T) {
	tests := []struct {
		listen string
		want   string
	}{
		{listen: ":8080", want: "http://127.0.0.1:8080"},
		{listen: "[::]:1234", want: "http://127.0.0.1:1234"},
		{listen: ":0", want: "http://127.0.0.1:8080"},
		{listen: ":abc", want: "http://127.0.0.1:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.listen, func(t *testing.T) {
			if got := internalBaseURLFromListen(tt.listen); got != tt.want {
				t.Fatalf("internalBaseURLFromListen(%q) = %q, want %q", tt.listen, got, tt.want)
			}
		})
	}
}
