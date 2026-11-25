package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestParseProfileSetMembers: both forms read a set with this parser, so
// members a newer app adds to the set or to a PIN are ignored in both, and a
// name is trimmed before its length is checked. It catches the two forms
// disagreeing on what a set may hold.
func TestParseProfileSetMembers(t *testing.T) {
	item := func(name, pin string) string {
		return fmt.Sprintf(`{"sync_id":"0f8fad5b-d9cb-469f-a165-70867728950e","name":%q,"picture":"","pin":%s,"is_child":false,"max_content_rating":"","max_advisory_age":null,"require_advisory_age":false,"requests_blocked":false}`, name, pin)
	}
	for _, tc := range []struct {
		name, set string
		ok        bool
	}{
		{"unknown set member", `{"version":1,"future":1,"items":[` + item("Sam", `{"keep":true}`) + `]}`, true},
		{"unknown pin member", `{"version":1,"items":[` + item("Sam", `{"keep":true,"future":1}`) + `]}`, true},
		{"64 characters once trimmed", `{"version":1,"items":[` + item("  "+strings.Repeat("n", 64)+"  ", `{"keep":true}`) + `]}`, true},
		{"65 characters once trimmed", `{"version":1,"items":[` + item(strings.Repeat("n", 65), `{"keep":true}`) + `]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseProfileSet(json.RawMessage(tc.set), nil, "")
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok %v", err, tc.ok)
			}
		})
	}
}

// TestProfileSetRetryable: a set PostgreSQL aborted as a deadlock victim (an
// account delete running at the same moment locks the other way round) goes
// round the retry loop instead of answering 500. A unit test of the mapping:
// which side PostgreSQL aborts is not deterministic.
func TestProfileSetRetryable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"deadlock victim", fmt.Errorf("committing: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"household changed", errProfileSetRetry, true},
		{"another database error", &pgconn.PgError{Code: "23505"}, false},
		{"stale", ErrProfileSetStale, false},
		{"none", nil, false},
		{"plain error", errors.New("boom"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := profileSetRetryable(tc.err); got != tc.want {
				t.Fatalf("profileSetRetryable = %v, want %v", got, tc.want)
			}
		})
	}
}
