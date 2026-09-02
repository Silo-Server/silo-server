package migrations

import (
	"strings"
	"testing"
)

// The bigint widening carries three decisions that are cheap to lose in a
// later edit: exactly which parents move (and that no child moves with them,
// because each child is its own rewrite the maintainer schedules separately);
// that the Down narrows honestly, refusing before any ALTER when a stored id
// or a sequence position no longer fits integer, rather than letting Postgres
// fail part-way or truncate; and the smallest-first order so a failing Up
// surfaces before the media_files copy starts.

const widenCoreIDsMigration = "sql/20260901204344_widen_core_ids_to_bigint.sql"

// widenedParents is the audit result: the integer identity parents that newer
// bigint FK columns already reference, in the order the Up alters them.
var widenedParents = []string{"media_folders", "users", "media_files"}

func TestWidenCoreIDsMigrationContract(t *testing.T) {
	up, down := readMigrationSections(t, widenCoreIDsMigration)

	// The Up is exactly three ALTERs, one per parent, all on column id, in
	// smallest-first order.
	if got, want := strings.Count(up, "ALTER TABLE"), len(widenedParents); got != want {
		t.Fatalf("up has %d ALTER TABLE statements, contract lists %d parents: a child was added or a parent dropped", got, want)
	}
	last := -1
	for _, table := range widenedParents {
		stmt := "ALTER TABLE public." + table + " ALTER COLUMN id TYPE bigint;"
		at := strings.Index(up, stmt)
		if at < 0 {
			t.Fatalf("up missing exact statement %q", stmt)
		}
		if at < last {
			t.Errorf("up alters %s out of order; the order is smallest table first", table)
		}
		last = at
	}
	if strings.Contains(up, "TYPE integer") || strings.Contains(up, "TYPE int ") {
		t.Error("up must only widen, never narrow")
	}

	// No child is widened here. Every ALTER in the Up is one of the three
	// parents on column id; any other column name is a child that crept in.
	for _, section := range []struct{ name, body string }{{"up", up}, {"down", down}} {
		for _, frag := range strings.Split(section.body, "ALTER TABLE")[1:] {
			if !strings.Contains(frag, "ALTER COLUMN id TYPE") {
				t.Errorf("%s alters a column other than id: %q", section.name, strings.TrimSpace(frag[:min(len(frag), 80)]))
			}
		}
	}

	// The Down is the mirror: the same three tables back to integer, in the
	// reverse order, after the guard.
	if got, want := strings.Count(down, "ALTER TABLE"), len(widenedParents); got != want {
		t.Fatalf("down has %d ALTER TABLE statements, contract lists %d", got, want)
	}
	guardAt := strings.Index(down, "RAISE EXCEPTION")
	if guardAt < 0 {
		t.Fatal("down missing the out-of-range guard")
	}
	last = len(down)
	for _, table := range widenedParents {
		stmt := "ALTER TABLE public." + table + " ALTER COLUMN id TYPE integer;"
		at := strings.Index(down, stmt)
		if at < 0 {
			t.Fatalf("down missing exact statement %q", stmt)
		}
		if at < guardAt {
			t.Errorf("down narrows %s before the guard runs", table)
		}
		if at > last {
			t.Errorf("down narrows %s out of order; the order is the reverse of the up", table)
		}
		last = at
	}

	// The guard covers the same three tables, checks the stored ids against
	// both ends of the int4 range and the identity sequence position against
	// the ceiling, and names the offender. Silent truncation is the failure
	// mode being pinned out.
	got := targetsArray(t, down, "targets text[]")
	if len(got) != len(widenedParents) {
		t.Fatalf("down guard covers %d tables, contract lists %d", len(got), len(widenedParents))
	}
	for i, want := range widenedParents {
		if got[i] != want {
			t.Errorf("down guard targets[%d] = %q, want %q", i, got[i], want)
		}
	}
	for _, want := range []string{
		"SELECT min(id), max(id) FROM public.%I",
		"IF max_id > 2147483647 THEN RAISE EXCEPTION",
		"IF min_id < -2147483648 THEN RAISE EXCEPTION",
		"SELECT last_value FROM %s",
		"IF seq_pos > 2147483647 THEN RAISE EXCEPTION",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("down guard missing %q", want)
		}
	}
	if strings.Contains(down, "USING") {
		t.Error("down must not cast with USING; an out-of-range value must fail, never be coerced")
	}
}
