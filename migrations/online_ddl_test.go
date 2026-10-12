package migrations

import (
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// onlineDDLCutoff is the newest migration version that predates this check.
// Migrations with a later version must keep locks on existing tables short;
// see "Keep migrations online" in DEVELOPMENT.md. Older migrations may already
// have run on installs and must not be edited to satisfy the check.
const onlineDDLCutoff = 20261011152954

// onlineDDLFilesAtCutoff is how many migrations had a version at or below the
// cutoff when it was set. Goose runs migrations out of order, so a branch can
// merge a migration created before the cutoff; the count catches it, and its
// file name goes in lateMigrations so that it is checked.
const onlineDDLFilesAtCutoff = 551

var lateMigrations = map[string]bool{}

// Rules checked by checkOnlineDDL. An exception is a comment above the
// statement, or at the end of its last line, naming the rule and the reason:
//
//	-- online-ddl allow create-index: the table is created by the previous migration and is empty
const (
	ruleCreateIndex        = "create-index"
	ruleConstraintNotValid = "constraint-not-valid"
	ruleValidateInTx       = "validate-in-transaction"
	ruleInlineConstraint   = "inline-column-constraint"
	ruleAlterColumnType    = "alter-column-type"
	ruleStoredGenerated    = "stored-generated-column"
	ruleConcurrentlyInTx   = "concurrently-in-transaction"
	// ruleAllowComment reports a malformed or unused exception; it cannot be
	// allowed itself.
	ruleAllowComment = "allow-comment"
)

var onlineDDLRules = map[string]string{
	ruleCreateIndex:        "build indexes on an existing table with CREATE INDEX CONCURRENTLY",
	ruleConstraintNotValid: "add CHECK and FOREIGN KEY constraints to an existing table NOT VALID",
	ruleValidateInTx:       "VALIDATE CONSTRAINT after another change to the table in the same transaction scans under that change's lock; validate in a later migration or a NO TRANSACTION file",
	ruleInlineConstraint:   "ADD COLUMN on an existing table must not carry an inline CHECK, REFERENCES, UNIQUE or PRIMARY KEY; add the column, then the constraint",
	ruleAlterColumnType:    "ALTER COLUMN ... TYPE rewrites or scans an existing table under an ACCESS EXCLUSIVE lock",
	ruleStoredGenerated:    "a STORED generated column rewrites an existing table under an ACCESS EXCLUSIVE lock",
	ruleConcurrentlyInTx:   "CONCURRENTLY needs a file marked -- +goose NO TRANSACTION",
}

type onlineDDLFinding struct {
	Line   int
	Rule   string
	Detail string
}

func TestNewMigrationsStayOnline(t *testing.T) {
	entries, err := fs.ReadDir(FS, "sql")
	if err != nil {
		t.Fatal(err)
	}
	atCutoff := 0
	for _, entry := range entries {
		version, err := goose.NumericComponent(entry.Name())
		if err != nil {
			continue
		}
		if version <= onlineDDLCutoff && !lateMigrations[entry.Name()] {
			atCutoff++
			continue
		}
		data, err := fs.ReadFile(FS, path.Join("sql", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range checkOnlineDDL(string(data)) {
			t.Errorf("%s:%d: %s: %s (see \"Keep migrations online\" in DEVELOPMENT.md)", entry.Name(), f.Line, f.Rule, f.Detail)
		}
	}
	if atCutoff != onlineDDLFilesAtCutoff {
		t.Errorf("%d migrations have a version at or below the online-DDL cutoff %d, want %d. "+
			"A migration created before the cutoff but merged after it must be added to lateMigrations so that it is checked.",
			atCutoff, onlineDDLCutoff, onlineDDLFilesAtCutoff)
	}
}

const sqlName = `(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_$]*)(?:\.(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_$]*))?`

var (
	gooseDownRE      = regexp.MustCompile(`(?im)^\s*--\s*\+goose\s+down\b`)
	gooseNoTxRE      = regexp.MustCompile(`(?im)^\s*--\s*\+goose\s+no\s+transaction\s*$`)
	createTableRE    = regexp.MustCompile(`(?i)^CREATE\s+(?:(?:GLOBAL\s+|LOCAL\s+)?(?:TEMP|TEMPORARY)\s+|UNLOGGED\s+)?(?:TABLE|MATERIALIZED\s+VIEW)\s+(IF\s+NOT\s+EXISTS\s+)?(` + sqlName + `)`)
	createIndexRE    = regexp.MustCompile(`(?i)^CREATE\s+(?:UNIQUE\s+)?INDEX\b(.*?)\bON\s+(?:ONLY\s+)?(` + sqlName + `)`)
	alterTableRE     = regexp.MustCompile(`(?i)^ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?(` + sqlName + `)\s+(.*)$`)
	concurrentlyRE   = regexp.MustCompile(`(?i)\bCONCURRENTLY\b`)
	refreshMatViewRE = regexp.MustCompile(`(?i)^REFRESH\s+MATERIALIZED\s+VIEW\b`)
	addCheckOrFKRE   = regexp.MustCompile(`(?i)^ADD\s+(?:CONSTRAINT\s+` + sqlName + `\s+)?(?:CHECK|FOREIGN\s+KEY)\b`)
	addIndexConstRE  = regexp.MustCompile(`(?i)^ADD\s+(?:CONSTRAINT\s+` + sqlName + `\s+)?(?:UNIQUE|PRIMARY\s+KEY|EXCLUDE)\b`)
	addOtherConstRE  = regexp.MustCompile(`(?i)^ADD\s+CONSTRAINT\b`)
	addColumnRE      = regexp.MustCompile(`(?i)^ADD\b`)
	usingIndexRE     = regexp.MustCompile(`(?i)\bUSING\s+INDEX\s+` + sqlName + `(?:\s+(?:NOT\s+)?DEFERRABLE)?(?:\s+INITIALLY\s+(?:DEFERRED|IMMEDIATE))?$`)
	notValidRE       = regexp.MustCompile(`(?i)\bNOT\s+VALID$`)
	inlineConstRE    = regexp.MustCompile(`(?i)\b(?:CHECK|REFERENCES|UNIQUE|PRIMARY\s+KEY)\b`)
	storedGenRE      = regexp.MustCompile(`(?i)\bGENERATED\s+ALWAYS\s+AS\s*\(.*\bSTORED\b`)
	validateRE       = regexp.MustCompile(`(?i)^VALIDATE\s+CONSTRAINT\b`)
	alterTypeRE      = regexp.MustCompile(`(?i)^ALTER\s+(?:COLUMN\s+)?` + sqlName + `\s+(?:SET\s+DATA\s+)?TYPE\b`)
	quotedIdentRE    = regexp.MustCompile(`"[^"]*"`)
	allowRE          = regexp.MustCompile(`^online-ddl\s+allow\b\s*([a-z-]*)\s*(:?)\s*(.*)$`)
)

// checkOnlineDDL reports statements in a migration's Up section that hold a
// long lock on a table that existed before the migration. Tables created
// earlier in the same file are exempt. Down sections are skipped: a rollback
// is a deliberate operator action, not a startup step.
//
// The check reads statements, not SQL semantics. String literals and
// dollar-quoted bodies are blanked first, so DDL inside a DO block or
// function body is not checked.
func checkOnlineDDL(src string) []onlineDDLFinding {
	noTx := gooseNoTxRE.MatchString(src)
	if loc := gooseDownRE.FindStringIndex(src); loc != nil {
		src = src[:loc[0]]
	}
	created := map[string]bool{} // tables this file created
	altered := map[string]bool{} // tables this file already locked with ALTER TABLE
	var findings []onlineDDLFinding
	for _, st := range splitSQLStatements(src) {
		var hits []onlineDDLFinding
		hit := func(rule, table string) {
			detail := onlineDDLRules[rule]
			if table != "" {
				detail += " (" + table + ")"
			}
			hits = append(hits, onlineDDLFinding{Line: st.line, Rule: rule, Detail: detail})
		}
		// IF NOT EXISTS does not prove the table is new: it may already exist
		// and hold rows.
		if m := createTableRE.FindStringSubmatch(st.text); m != nil && m[1] == "" {
			created[normalizeTableName(m[2])] = true
		}
		if !noTx && concurrentlyRE.MatchString(st.text) && !refreshMatViewRE.MatchString(st.text) {
			hit(ruleConcurrentlyInTx, "")
		}
		if m := createIndexRE.FindStringSubmatch(st.text); m != nil {
			if table := normalizeTableName(m[2]); !created[table] && !concurrentlyRE.MatchString(m[1]) {
				hit(ruleCreateIndex, table)
			}
		}
		if m := alterTableRE.FindStringSubmatch(st.text); m != nil && !created[normalizeTableName(m[1])] {
			table := normalizeTableName(m[1])
			actions := splitTopLevel(m[2], ',')
			validates, others := 0, 0
			for _, action := range actions {
				if validateRE.MatchString(strings.TrimSpace(action)) {
					validates++
				} else {
					others++
				}
			}
			// One statement takes the strongest lock any of its actions needs,
			// even in a NO TRANSACTION file; separate statements share a lock
			// only inside a transaction.
			if validates > 0 && (others > 0 || !noTx && altered[table]) {
				hit(ruleValidateInTx, table)
			}
			for _, action := range actions {
				action = strings.TrimSpace(action)
				if validateRE.MatchString(action) {
					continue
				}
				altered[table] = true
				if rule := alterTableActionRule(action); rule != "" {
					hit(rule, table)
				}
			}
		}
		findings = append(findings, applyAllowances(st, hits)...)
	}
	return findings
}

func alterTableActionRule(action string) string {
	switch {
	case addCheckOrFKRE.MatchString(action):
		if !notValidRE.MatchString(action) {
			return ruleConstraintNotValid
		}
	case addIndexConstRE.MatchString(action):
		if !usingIndexRE.MatchString(action) {
			return ruleCreateIndex
		}
	case addOtherConstRE.MatchString(action):
		// Other table constraints, such as NOT NULL, are not checked.
	case addColumnRE.MatchString(action):
		action = quotedIdentRE.ReplaceAllString(action, "x")
		if storedGenRE.MatchString(action) {
			return ruleStoredGenerated
		}
		if inlineConstRE.MatchString(action) {
			return ruleInlineConstraint
		}
	case alterTypeRE.MatchString(action):
		return ruleAlterColumnType
	}
	return ""
}

// applyAllowances drops the hits a statement's allow comments cover and
// reports malformed or unused allow comments.
func applyAllowances(st sqlStatement, hits []onlineDDLFinding) []onlineDDLFinding {
	var out []onlineDDLFinding
	report := func(detail string) {
		out = append(out, onlineDDLFinding{Line: st.line, Rule: ruleAllowComment, Detail: detail})
	}
	allowed := map[string]bool{}
	for _, c := range st.comments {
		m := allowRE.FindStringSubmatch(c)
		switch {
		case m == nil:
		case m[1] == "" || m[2] == "":
			report("write it as: -- online-ddl allow <rule>: <reason>")
		case onlineDDLRules[m[1]] == "":
			report("unknown rule " + strconv.Quote(m[1]))
		case strings.TrimSpace(m[3]) == "":
			report("the exception for " + m[1] + " needs a reason")
		default:
			allowed[m[1]] = true
		}
	}
	for _, h := range hits {
		if _, ok := allowed[h.Rule]; ok {
			allowed[h.Rule] = false
			continue
		}
		out = append(out, h)
	}
	for _, rule := range slices.Sorted(maps.Keys(allowed)) {
		if allowed[rule] {
			report("the exception for " + rule + " matches nothing in this statement")
		}
	}
	return out
}

func normalizeTableName(name string) string {
	name = strings.ToLower(strings.ReplaceAll(name, `"`, ""))
	return strings.TrimPrefix(name, "public.")
}

type sqlStatement struct {
	line     int      // line of the statement's first token
	text     string   // comments removed, literals blanked, whitespace collapsed
	comments []string // line comments, trimmed of "--"

	commentLine int // line of the first comment
}

// splitSQLStatements splits SQL on top-level semicolons. Quoted strings and
// dollar-quoted bodies are kept as empty literals so their contents never
// match a rule; double-quoted identifiers are kept. A line comment belongs to
// the statement it precedes or sits in, except one that follows a statement's
// semicolon on the same line, which belongs to that statement.
func splitSQLStatements(src string) []sqlStatement {
	var (
		out     []sqlStatement
		cur     strings.Builder
		st      sqlStatement
		line    = 1
		endLine int // line of the last statement's semicolon
	)
	flush := func() {
		st.text = strings.Join(strings.Fields(cur.String()), " ")
		if st.line == 0 {
			// Comments after the last statement are reported on their own line.
			st.line = st.commentLine
		}
		if st.text != "" || len(st.comments) > 0 {
			out = append(out, st)
		}
		cur.Reset()
		st = sqlStatement{}
	}
	start := func() {
		if st.line == 0 {
			st.line = line
		}
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			line++
			cur.WriteByte(' ')
			i++
		case strings.HasPrefix(src[i:], "--"):
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				end = len(src) - i
			}
			comment := strings.TrimSpace(src[i+2 : i+end])
			if line == endLine && len(out) > 0 && strings.TrimSpace(cur.String()) == "" {
				out[len(out)-1].comments = append(out[len(out)-1].comments, comment)
			} else {
				if len(st.comments) == 0 {
					st.commentLine = line
				}
				st.comments = append(st.comments, comment)
			}
			i += end
		case strings.HasPrefix(src[i:], "/*"):
			// Block comments nest in PostgreSQL.
			depth, j := 1, i+2
			for j < len(src) && depth > 0 {
				switch {
				case strings.HasPrefix(src[j:], "/*"):
					depth++
					j += 2
				case strings.HasPrefix(src[j:], "*/"):
					depth--
					j += 2
				default:
					j++
				}
			}
			line += strings.Count(src[i:j], "\n")
			cur.WriteByte(' ')
			i = j
		case c == '\'' || c == '"':
			// E'...' strings take backslash escapes.
			escapes := c == '\'' && i > 0 && (src[i-1] == 'E' || src[i-1] == 'e') && (i < 2 || !isIdentByte(src[i-2]))
			j := i + 1
			for j < len(src) {
				if escapes && src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == c {
					if j+1 < len(src) && src[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			j = min(j+1, len(src))
			start()
			if c == '"' {
				cur.WriteString(src[i:j])
			} else {
				cur.WriteString("''")
			}
			line += strings.Count(src[i:j], "\n")
			i = j
		case c == '$':
			tag := dollarTagRE.FindString(src[i:])
			if tag == "" || i > 0 && isIdentByte(src[i-1]) {
				start()
				cur.WriteByte(c)
				i++
				continue
			}
			end := strings.Index(src[i+len(tag):], tag)
			j := len(src)
			if end >= 0 {
				j = i + len(tag) + end + len(tag)
			}
			start()
			cur.WriteString("$$ $$")
			line += strings.Count(src[i:j], "\n")
			i = j
		case c == ';':
			flush()
			endLine = line
			i++
		default:
			if c != ' ' && c != '\t' && c != '\r' {
				start()
			}
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return out
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '$' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

var dollarTagRE = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$`)

// splitTopLevel splits s on sep outside parentheses and brackets.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

func TestCheckOnlineDDL(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string // rule per finding, in order
	}{
		// Locking patterns.
		{"plain index", "-- +goose Up\nCREATE INDEX media_files_x_idx ON public.media_files (x);", []string{ruleCreateIndex}},
		{"unique index, quoted table", `CREATE UNIQUE INDEX IF NOT EXISTS i ON "public"."Downloads" (x);`, []string{ruleCreateIndex}},
		{"concurrent index in a transaction", "CREATE INDEX CONCURRENTLY i ON media_files (x);", []string{ruleConcurrentlyInTx}},
		{"drop index concurrently in a transaction", "DROP INDEX CONCURRENTLY IF EXISTS i;", []string{ruleConcurrentlyInTx}},
		{"check constraint", "ALTER TABLE media_files ADD CONSTRAINT c CHECK (x > 0);", []string{ruleConstraintNotValid}},
		{"unnamed check constraint", "ALTER TABLE media_files ADD CHECK (x > 0);", []string{ruleConstraintNotValid}},
		{"foreign key", "ALTER TABLE downloads ADD CONSTRAINT f FOREIGN KEY (u) REFERENCES users(id);", []string{ruleConstraintNotValid}},
		{"second action of several", "ALTER TABLE t DROP CONSTRAINT c, ADD CONSTRAINT c CHECK (a IN (1, 2));", []string{ruleConstraintNotValid}},
		{"inline check", "ALTER TABLE public.media_files ADD COLUMN m jsonb NOT NULL DEFAULT '[]'::jsonb\n    CHECK (jsonb_typeof(m) = 'array');", []string{ruleInlineConstraint}},
		{"inline references without COLUMN", "ALTER TABLE downloads ADD revoked_by integer REFERENCES users(id) ON DELETE SET NULL;", []string{ruleInlineConstraint}},
		{"alter column type", "ALTER TABLE media_files ALTER COLUMN size TYPE bigint;", []string{ruleAlterColumnType}},
		{"set data type without COLUMN", "ALTER TABLE media_files ALTER size SET DATA TYPE bigint USING size::bigint;", []string{ruleAlterColumnType}},
		{"stored generated column", "ALTER TABLE media_items ADD COLUMN t tsvector GENERATED ALWAYS AS (to_tsvector('simple', title)) STORED;", []string{ruleStoredGenerated}},
		{"validate in the adding transaction", "ALTER TABLE t ADD CONSTRAINT c CHECK (x > 0) NOT VALID;\nALTER TABLE t VALIDATE CONSTRAINT c;", []string{ruleValidateInTx}},
		{"table created by an earlier migration", "CREATE TABLE other (id int);\nCREATE INDEX i ON t (x);", []string{ruleCreateIndex}},
		{"index before the table is created", "CREATE INDEX i ON t (x);\nCREATE TABLE t (x int);", []string{ruleCreateIndex}},
		{"ON ONLY is not exempt", "CREATE INDEX i ON ONLY policy_decisions (x);", []string{ruleCreateIndex}},
		{"unique constraint builds an index", "ALTER TABLE media_files ADD CONSTRAINT u UNIQUE (file_path);", []string{ruleCreateIndex}},
		{"primary key builds an index", "ALTER TABLE t ADD PRIMARY KEY (id);", []string{ruleCreateIndex}},
		{"inline unique", "ALTER TABLE media_files ADD COLUMN k bigint UNIQUE;", []string{ruleInlineConstraint}},
		{"column named valid", "ALTER TABLE downloads ADD CONSTRAINT c CHECK (revoked OR NOT valid);", []string{ruleConstraintNotValid}},
		{"validate after another change to the table", "ALTER TABLE media_files ADD COLUMN y int;\nALTER TABLE media_files VALIDATE CONSTRAINT c;", []string{ruleValidateInTx}},
		{"validate in the same statement", "ALTER TABLE media_files ADD COLUMN y int, VALIDATE CONSTRAINT c;", []string{ruleValidateInTx}},
		{"validate an older constraint after adding another", "ALTER TABLE t ADD CONSTRAINT b CHECK (x > 0) NOT VALID;\nALTER TABLE t VALIDATE CONSTRAINT a;", []string{ruleValidateInTx}},
		{"validate before another action in the statement", "ALTER TABLE media_files VALIDATE CONSTRAINT c, ADD COLUMN y int;", []string{ruleValidateInTx}},
		{"validate with another action in a NO TRANSACTION file", "-- +goose NO TRANSACTION\nALTER TABLE media_files ADD COLUMN y int, VALIDATE CONSTRAINT c;", []string{ruleValidateInTx}},
		{"array literal in an inline check", "ALTER TABLE media_files ADD COLUMN x int[] DEFAULT ARRAY[1,2] CHECK (cardinality(x) > 0);", []string{ruleInlineConstraint}},
		{"IF NOT EXISTS does not prove the table is new", "CREATE TABLE IF NOT EXISTS public.media_files (id integer);\nCREATE INDEX i ON media_files (id);", []string{ruleCreateIndex}},
		{"nested block comment", "/* outer /* inner */ still a comment */ CREATE INDEX i ON media_files (x);", []string{ruleCreateIndex}},
		{"E-string with an escaped quote", "INSERT INTO t VALUES (E'it\\'s');\nCREATE INDEX i ON media_files (x);\nSELECT 'x';", []string{ruleCreateIndex}},

		// Online patterns and look-alikes.
		{"concurrent index", "-- +goose NO TRANSACTION\n-- +goose Up\nDROP INDEX CONCURRENTLY IF EXISTS i;\nCREATE INDEX CONCURRENTLY i ON media_files (x);", nil},
		{"unique constraint using an existing index", "ALTER TABLE t ADD CONSTRAINT u UNIQUE USING INDEX t_u_idx;", nil},
		{"NOT NULL constraint is not checked", "ALTER TABLE t ADD CONSTRAINT n NOT NULL x;", nil},
		{"constraints NOT VALID", "ALTER TABLE t ADD CONSTRAINT c CHECK (x > 0) NOT VALID, ADD CONSTRAINT f FOREIGN KEY (u) REFERENCES users(id) NOT VALID;", nil},
		{"validate in a later migration", "ALTER TABLE t VALIDATE CONSTRAINT c;", nil},
		{"validate in a NO TRANSACTION file", "-- +goose NO TRANSACTION\nALTER TABLE t ADD CONSTRAINT c CHECK (x > 0) NOT VALID;\nALTER TABLE t VALIDATE CONSTRAINT c;", nil},
		{"plain add column", "ALTER TABLE media_files ADD COLUMN IF NOT EXISTS checksum text NOT NULL DEFAULT '', ADD COLUMN check_count int;", nil},
		{"quoted column named check", `ALTER TABLE t ADD COLUMN "check" boolean;`, nil},
		{"virtual generated column", "ALTER TABLE t ADD COLUMN y int GENERATED ALWAYS AS (x * 2) VIRTUAL;", nil},
		{"identity is not a stored generated column", "ALTER TABLE t ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY;", nil},
		{"alter column default", "ALTER TABLE t ALTER COLUMN x SET DEFAULT 'TYPE';", nil},
		{"new table with inline constraints and indexes", `
CREATE TABLE public.marker_jobs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    file_id integer NOT NULL REFERENCES media_files(id),
    kind text NOT NULL CHECK (kind <> ''),
    doc tsvector GENERATED ALWAYS AS (to_tsvector('simple', kind)) STORED
);
CREATE INDEX marker_jobs_file_idx ON marker_jobs (file_id);
ALTER TABLE marker_jobs ADD COLUMN note text CHECK (note <> ''), ALTER COLUMN kind TYPE varchar(20);
ALTER TABLE public.marker_jobs ADD CONSTRAINT one CHECK (id > 0);`, nil},
		{"allow on a statement about a new table", "CREATE TABLE t (x int);\n-- online-ddl allow create-index: stale\nALTER TABLE t ADD CHECK (x > 0);", []string{ruleAllowComment}},
		{"new materialized view", "CREATE MATERIALIZED VIEW mv AS SELECT 1 AS x;\nCREATE UNIQUE INDEX mv_x ON mv (x);\nREFRESH MATERIALIZED VIEW CONCURRENTLY mv;", nil},
		{"DDL in comments and strings", "-- CREATE INDEX i ON t (x);\n/* ALTER TABLE t ADD CHECK (x > 0); */\nINSERT INTO notes VALUES ('CREATE INDEX i ON t (x); ALTER TABLE t ALTER COLUMN x TYPE int');", nil},
		{"DDL in a function body", "-- +goose StatementBegin\nCREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $fn$\nBEGIN\n  EXECUTE 'CREATE INDEX i ON t (x)';\n  CREATE INDEX j ON t (y);\nEND $fn$;\n-- +goose StatementEnd", nil},
		{"Down section", "-- +goose Up\nSELECT 1;\n-- +goose Down\nCREATE INDEX i ON t (x);\nALTER TABLE t ADD CHECK (x > 0);", nil},

		// Exceptions.
		{"allowed with a reason", "-- online-ddl allow create-index: the table is created empty by the previous migration\nCREATE INDEX i ON t (x);", nil},
		{"allow covers only its rule", "-- online-ddl allow constraint-not-valid: t holds one row per node\nALTER TABLE t ADD CHECK (x > 0), ALTER COLUMN y TYPE bigint;", []string{ruleAlterColumnType}},
		{"allow without a reason", "-- online-ddl allow create-index:\nCREATE INDEX i ON t (x);", []string{ruleAllowComment, ruleCreateIndex}},
		{"allow with an unknown rule", "-- online-ddl allow create-indexes: typo\nCREATE INDEX i ON t (x);", []string{ruleAllowComment, ruleCreateIndex}},
		{"malformed allow", "-- online-ddl allow because it is small\nCREATE INDEX i ON t (x);", []string{ruleAllowComment, ruleCreateIndex}},
		{"unused allow", "-- online-ddl allow create-index: stale\nCREATE INDEX CONCURRENTLY i ON t (x);\n-- +goose NO TRANSACTION", []string{ruleAllowComment}},
		{"allow at the end of the statement's line", "CREATE INDEX i ON t (x); -- online-ddl allow create-index: t holds one row per node\nSELECT 1;", nil},
		{"allow after the last statement", "SELECT 1;\n-- online-ddl allow create-index: nothing follows", []string{ruleAllowComment}},
		{"allow does not carry to the next statement", "-- online-ddl allow create-index: small table\nCREATE INDEX i ON t (x);\nCREATE INDEX j ON t (y);", []string{ruleCreateIndex}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := checkOnlineDDL(tc.sql)
			var got []string
			for _, f := range findings {
				got = append(got, f.Rule)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("rules = %v, want %v\nfindings: %+v", got, tc.want, findings)
			}
		})
	}
}

func TestCheckOnlineDDLReportsStatementLine(t *testing.T) {
	findings := checkOnlineDDL("-- +goose Up\n\nSELECT 1;\n\n-- note\nCREATE INDEX i\n  ON t (x);")
	if len(findings) != 1 || findings[0].Line != 6 {
		t.Fatalf("findings = %+v, want one on line 6", findings)
	}
}

// The two migrations that prompted this check, as merged. They have shipped
// and stay as they are; the check must still catch what they do.
func TestCheckOnlineDDLCatchesShippedLockingMigrations(t *testing.T) {
	for name, want := range map[string][]string{
		"20261010180000_add_marker_thumbnails.sql":   {ruleInlineConstraint, ruleConstraintNotValid},
		"20261008201436_add_download_revocation.sql": {ruleInlineConstraint, ruleCreateIndex},
	} {
		data, err := fs.ReadFile(FS, path.Join("sql", name))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range checkOnlineDDL(string(data)) {
			got = append(got, f.Rule)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: rules = %v, want %v", name, got, want)
		}
	}
}
