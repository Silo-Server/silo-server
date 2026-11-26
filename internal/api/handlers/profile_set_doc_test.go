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
// disagreeing on what a set may hold, and an item member becoming optional.
func TestParseProfileSetMembers(t *testing.T) {
	item := func(name, pin string) string {
		return fmt.Sprintf(`{"sync_id":"0f8fad5b-d9cb-469f-a165-70867728950e","name":%q,"picture":"","pin":%s,"is_child":false,"max_content_rating":"","max_advisory_age":null,"require_advisory_age":false,"requests_blocked":false,"settings":{}}`, name, pin)
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
	// Every member an item has today is required, settings included ({} is
	// how an app says it has none to apply): one left out is refused at that
	// member with the code required, never read as false, empty or "no
	// limit". settings_reset is the optional one, and the items above leave
	// it out.
	var whole map[string]json.RawMessage
	if err := json.Unmarshal([]byte(item("Sam", `{"keep":true}`)), &whole); err != nil {
		t.Fatal(err)
	}
	for member := range whole {
		t.Run("without "+member, func(t *testing.T) {
			partial := map[string]json.RawMessage{}
			for k, v := range whole {
				if k != member {
					partial[k] = v
				}
			}
			body, _ := json.Marshal(map[string]any{"version": 1, "items": []any{partial}})
			_, err := parseProfileSet(body, nil, "")
			var field *ProfileSetFieldError
			if !errors.As(err, &field) || field.Location != "set.items[0]."+member || field.Code != "required" {
				t.Fatalf("err = %+v, want a required error at set.items[0].%s", err, member)
			}
		})
	}
}

// TestProfileSetRetryable: a set PostgreSQL aborted as a deadlock victim (a
// writer that breaks the set's lock order) goes round the retry loop instead
// of answering 500. A unit test of the mapping:
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
