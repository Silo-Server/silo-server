package queryattr

import (
	"strings"
	"testing"
)

func TestFingerprintGrammar(t *testing.T) {
	for _, tc := range []struct {
		a, b   string
		same   bool
		na, nb int32
	}{
		{"SELECT $1", " /* x /* y */ z */ select 'private';", true, 0, 0},
		{"a=$1 OR b=$1", "a=$1 OR b=$2", true, 0, 0},
		{"x IN ($1,$2)", "x in ($1,$2,$3,$4)", true, 2, 4},
		{"x IN ($1)", "x = ANY($1)", false, 1, 0},
		{"x IN ($1::int,$2::int)", "x IN ($1::uuid)", false, 2, 1},
		{"x IN ($1::uuid,$2::uuid)", "x IN ($1::uuid)", true, 2, 1},
		{"x IN (1,NULL)", "x IN (1)", false, 0, 1},
		{"$1::int", "$1::bigint", false, 0, 0},
		{"$1::uuid", "'private'::uuid", true, 0, 0},
		{"'{}'::text[]", "$1::text[]", true, 0, 0},
		{"$$a'b$$", "$fn$other$fn$", true, 0, 0},
		{`"Title"`, `title`, false, 0, 0},
		{"MixedCase", "mixedcase", true, 0, 0},
		{"x", "y", false, 0, 0},
		{"x IS NULL", "x = $1", false, 0, 0},
		{"LIMIT 10", "LIMIT $1", true, 0, 0},
		{"SELECT -1", "SELECT 1", true, 0, 0},
		{"SELECT -$1", "SELECT $1", false, 0, 0},
		{"x IN (-1,-2)", "x IN (1)", true, 2, 1},
		{"x IN (-$1)", "x IN ($1)", false, 0, 1},
		{"TRUE", "FALSE", false, 0, 0},
		{"ASC", "DESC", false, 0, 0},
		{"x IN (SELECT x)", "x IN ($1)", false, 0, 1},
		{"VALUES ($1,$2),($3,$4)", "VALUES ($1,$2),($3,$4),($5,$6)", true, 2, 3},
		{"VALUES ($1,$2)", "VALUES ($1,$2),($3,$4)", true, 1, 2},
		{"VALUES ($1,$2),($3::int,$4)", "VALUES ($1,$2),($3,$4)", false, 0, 2},
		{"ARRAY[$1,$2]", "ARRAY[$1,$2,$3]", false, 0, 0},
		{"SELECT $1 UNION ALL SELECT $2", "SELECT $1", false, 0, 0},
		{"SET LOCAL pg_trgm.strict_word_similarity_threshold = 0.3", "SET LOCAL pg_trgm.strict_word_similarity_threshold = 0.25", true, 0, 0},
		{`SELECT E'a\'b'`, "SELECT B'0101'", true, 0, 0},
		{"SELECT X'ff'", "SELECT U&'hello'", true, 0, 0},
		{"SELECT 1.5e-12", "SELECT $99", true, 0, 0},
		{"ab c", "a bc", false, 0, 0},
		{"x IN (f($1), f($2))", "x IN ($1,$2)", false, 0, 2},
		{"x IN ($1, $2::int)", "x IN ($1)", false, 0, 1},
		{"SELECT " + strings.Repeat("a,", 40000) + "x", "SELECT " + strings.Repeat("a,", 40000) + "y", false, 0, 0},
	} {
		t.Run(tc.a[:min(len(tc.a), 60)], func(t *testing.T) {
			a, na := fingerprint(tc.a)
			b, nb := fingerprint(tc.b)
			if (a == b) != tc.same || na != tc.na || nb != tc.nb {
				t.Errorf("hash equality=%v sizes=%d,%d; want equality=%v sizes=%d,%d", a == b, na, nb, tc.same, tc.na, tc.nb)
			}
		})
	}
}

func TestMalformedFingerprintDeterministic(t *testing.T) {
	for _, s := range []string{"'unterminated", `"unterminated`, "/* /*", "$tag$body", "x IN (", "VALUES ((", "1e+", "\xff\x00"} {
		a, _ := fingerprint(s)
		b, _ := fingerprint(s)
		if a != b {
			t.Errorf("nondeterministic fingerprint for %q", s)
		}
	}
}

func FuzzFingerprint(f *testing.F) {
	for _, s := range []string{"SELECT 1", "x in ($1,$2)", "VALUES (1),(2)", "/* nested /* */ */", "$x$body"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a, n := fingerprint(s)
		b, m := fingerprint(s)
		if a != b || n != m {
			t.Fatal("nondeterministic")
		}
	})
}
