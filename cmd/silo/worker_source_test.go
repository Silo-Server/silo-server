package main

import "testing"

func TestWorkerSourceOwnerURL(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		bad       bool
	}{
		{"", "", false},
		{"http://core-one.example:8080/", "http://core-one.example:8080", false},
		{"https://CORE-ONE.example:443", "https://core-one.example", false},
		{"https://public.example", "", true},
		{"http://core-one.example/prefix", "", true},
		{"http://user:secret@core-one.example", "", true},
		{"ftp://core-one.example", "", true},
		{"http://core-one.example?x=1", "", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := workerSourceOwnerURL(tc.raw, "https://public.example")
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("origin=%q err=%v", got, err)
			}
		})
	}
}
