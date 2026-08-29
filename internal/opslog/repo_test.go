package opslog

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestNormalizeLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "nil stays nil", input: nil, want: nil},
		{name: "blank entries are dropped", input: []string{"", "  "}, want: nil},
		{name: "trimmed and lowercased", input: []string{" Error ", "WARN"}, want: []string{"error", "warn"}},
		{name: "duplicates collapse", input: []string{"error", "Error", "error"}, want: []string{"error"}},
		{name: "order is preserved", input: []string{"warn", "error", "info"}, want: []string{"warn", "error", "info"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := NormalizeLevels(tt.input); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("NormalizeLevels(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildListQueryLevelFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		opts          ListOptions
		wantPredicate string
		wantArg       any
	}{
		{
			name:          "no level filter",
			opts:          ListOptions{},
			wantPredicate: "",
		},
		{
			name:          "single level keeps the equality predicate",
			opts:          ListOptions{Level: "ERROR"},
			wantPredicate: "level = $1",
			wantArg:       "error",
		},
		{
			name:          "several levels use ANY",
			opts:          ListOptions{Levels: []string{"error", "warn"}},
			wantPredicate: "level = ANY($1)",
			wantArg:       []string{"error", "warn"},
		},
		{
			name:          "levels win over level",
			opts:          ListOptions{Level: "info", Levels: []string{"error"}},
			wantPredicate: "level = ANY($1)",
			wantArg:       []string{"error"},
		},
		{
			name:          "an all-blank levels list falls back to level",
			opts:          ListOptions{Level: "info", Levels: []string{"", " "}},
			wantPredicate: "level = $1",
			wantArg:       "info",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			query, args, _, err := buildListQuery(tt.opts)
			if err != nil {
				t.Fatalf("buildListQuery: %v", err)
			}
			if tt.wantPredicate == "" {
				if strings.Contains(query, "level =") {
					t.Fatalf("query filters on level without a filter being set:\n%s", query)
				}
				return
			}
			if !strings.Contains(query, tt.wantPredicate) {
				t.Fatalf("query missing %q:\n%s", tt.wantPredicate, query)
			}
			if len(args) == 0 {
				t.Fatal("no arguments bound")
			}
			if !reflect.DeepEqual(args[0], tt.wantArg) {
				t.Fatalf("arg[0] = %#v, want %#v", args[0], tt.wantArg)
			}
		})
	}
}

// The placeholder numbering has to stay in step with the argument slice
// whichever level filter is used, or a multi-filter query binds the wrong
// values.
func TestBuildListQueryPlaceholdersMatchArguments(t *testing.T) {
	t.Parallel()

	userID := 42
	opts := ListOptions{
		Levels:    []string{"error", "warn"},
		Component: "playback",
		UserID:    &userID,
		Query:     "timeout",
		Limit:     25,
	}

	query, args, limit, err := buildListQuery(opts)
	if err != nil {
		t.Fatalf("buildListQuery: %v", err)
	}
	if limit != 25 {
		t.Fatalf("limit = %d, want 25", limit)
	}
	for i := range args {
		placeholder := "$" + strconv.Itoa(i+1)
		if !strings.Contains(query, placeholder) {
			t.Fatalf("query does not bind %s:\n%s", placeholder, query)
		}
	}
	// levels, component, user_id, message, limit
	if len(args) != 5 {
		t.Fatalf("args = %#v, want 5 entries", args)
	}
	if args[len(args)-1] != opts.Limit+1 {
		t.Fatalf("limit arg = %v, want %d (limit + 1 for the cursor probe)", args[len(args)-1], opts.Limit+1)
	}
}
