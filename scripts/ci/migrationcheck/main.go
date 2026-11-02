// Command migrationcheck protects merged migrations and encrypted setting keys.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var sqlNoise = regexp.MustCompile(`(?s)(?i:E)'(?:\\.|''|[^'\\])*'|'(?:''|[^'])*'|"(?:""|[^"])*"|--[^\n]*|/\*.*?\*/|\$[a-zA-Z0-9_]*\$`)
var settingsWrite = regexp.MustCompile(`(?is)\b(UPDATE|INSERT\s+INTO|MERGE\s+INTO)\s+(?:ONLY\s*)?\(?\s*(?:[a-z_][a-z0-9_]*\s*\.\s*)?server_settings\b([^;]*)`)
var selectKeyword = regexp.MustCompile(`(?i)\bSELECT\b`)
var valuesInsert = regexp.MustCompile(`(?is)^\s*(?:AS\s+[a-z_][a-z0-9_]*\s*)?(?:\([^)]*\)\s*)?(?:DEFAULT\s+)?VALUES\b`)
var setClause = regexp.MustCompile(`(?is)\bSET\b([^;]*)`)
var keyAssignment = regexp.MustCompile(`(?i)(?:^|,|\()\s*key\b`)

func writesSettingKey(data []byte) bool {
	// Hide comments and values before matching SQL words. Strings mentioning
	// settings may contain executable SQL, so refuse them instead of masking them.
	// Quoted identifiers retain only the protected names.
	ambiguous := false
	sql := sqlNoise.ReplaceAllStringFunc(string(data), func(token string) string {
		if token[0] == '$' || (strings.HasPrefix(token, "/*") && strings.Contains(token[2:], "/*")) {
			ambiguous = true
		}
		if (token[0] == '\'' || token[0] == 'e' || token[0] == 'E') && strings.Contains(strings.ToLower(token), "server_settings") {
			ambiguous = true
		}
		if token[0] == '"' {
			switch token {
			case `"key"`:
				return "key"
			case `"server_settings"`:
				return "server_settings"
			default:
				return "quoted_identifier"
			}
		}
		return " "
	})
	// Nested comments and string bodies exceed this text check; refuse settings
	// migrations rather than mistake partially masked SQL for a safe write.
	if ambiguous && strings.Contains(strings.ToLower(string(data)), "server_settings") {
		return true
	}
	for _, write := range settingsWrite.FindAllStringSubmatch(sql, -1) {
		if !strings.EqualFold(write[1], "UPDATE") {
			// Refuse copies and unfamiliar write forms rather than infer whether
			// their source values are encrypted. Literal VALUES inserts are safe.
			if strings.HasPrefix(strings.ToUpper(write[1]), "MERGE") || !valuesInsert.MatchString(write[2]) || selectKeyword.MatchString(write[2]) {
				return true
			}
		}
		sets := setClause.FindAllStringSubmatch(write[2], -1)
		if strings.EqualFold(write[1], "UPDATE") && len(sets) == 0 {
			return true
		}
		for _, set := range sets {
			// Keep the full clause: a WHERE in a value or subquery must not
			// hide a later assignment. Include tuple assignment targets.
			if keyAssignment.MatchString(set[1]) {
				return true
			}
		}
	}
	return false
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func git(args ...string) ([]byte, error) {
	out, err := exec.CommandContext(context.Background(), "git", args...).Output() //nolint:gosec // fixed git executable with separate arguments, no shell
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return out, nil
}

func run(args []string) error {
	flags := flag.NewFlagSet("migrationcheck", flag.ContinueOnError)
	base := flags.String("base", "", "base revision to compare with the working tree")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *base == "" || flags.NArg() != 0 {
		return errors.New("migrationcheck requires -base <revision>")
	}
	changes, err := git("diff", "--name-status", "-z", "--no-renames", *base, "--", "migrations/sql/")
	if err != nil {
		return err
	}
	var additions []string
	fields := bytes.Split(bytes.TrimSuffix(changes, []byte{0}), []byte{0})
	if len(changes) > 0 {
		for i := 0; i < len(fields); i += 2 {
			if i+1 >= len(fields) {
				return errors.New("incomplete migration diff")
			}
			if string(fields[i]) != "A" {
				return fmt.Errorf("merged migration is immutable: %s (%s)", fields[i+1], fields[i])
			}
			additions = append(additions, string(fields[i+1]))
		}
	}
	untracked, err := git("ls-files", "-z", "--others", "--exclude-standard", "--", "migrations/sql/")
	if err != nil {
		return err
	}
	for _, file := range bytes.Split(bytes.TrimSuffix(untracked, []byte{0}), []byte{0}) {
		if len(file) > 0 {
			additions = append(additions, string(file))
		}
	}
	for _, file := range additions {
		if !strings.HasSuffix(file, ".sql") {
			continue
		}
		data, err := os.ReadFile(file) //nolint:gosec // paths come from git's migrations/sql listing
		if err != nil {
			return fmt.Errorf("read new migration %s: %w", file, err)
		}
		if writesSettingKey(data) {
			return fmt.Errorf("%s: server_settings key rename invalidates encrypted values", file)
		}
	}
	return nil
}
