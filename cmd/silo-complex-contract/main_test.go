package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteFixturesIsDeterministic(t *testing.T) {
	var first, second bytes.Buffer
	if err := writeFixtures(&first); err != nil {
		t.Fatal(err)
	}
	if err := writeFixtures(&second); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("fixture output changed between runs")
	}
	for _, want := range []string{`"api_version": "2.2"`, `"sessions.snapshot.v2"`, `"complete_snapshot"`, `"incomplete_snapshot"`, `"session_generation": "generation-1"`, `"username": "contract-user"`, `"has_playback_control": true`, `"status": "terminated"`, `"command_id": "command-1"`} {
		if !strings.Contains(first.String(), want) {
			t.Fatalf("missing %s", want)
		}
	}
}
