package secret

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeRows yields preset all-string rows for the backfill SELECT.
type fakeRows struct {
	rows [][]string
	i    int
}

func (f *fakeRows) Next() bool { f.i++; return f.i <= len(f.rows) }
func (f *fakeRows) Scan(dest ...any) error {
	row := f.rows[f.i-1]
	if len(dest) != len(row) {
		return fmt.Errorf("fakeRows: scan into %d dests, row has %d", len(dest), len(row))
	}
	for j, d := range dest {
		p, ok := d.(*string)
		if !ok {
			return fmt.Errorf("fakeRows: dest %d is %T, want *string", j, d)
		}
		*p = row[j]
	}
	return nil
}
func (f *fakeRows) Close()                                       {}
func (f *fakeRows) Err() error                                   { return nil }
func (f *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (f *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (f *fakeRows) Values() ([]any, error)                       { return nil, nil }
func (f *fakeRows) RawValues() [][]byte                          { return nil }
func (f *fakeRows) Conn() *pgx.Conn                              { return nil }

type capturedExec struct {
	args []any
}

// fakeExec returns a fixed candidate set from Query and captures every Exec.
type fakeExec struct {
	rows  [][]string
	execs []capturedExec
}

func (f *fakeExec) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &fakeRows{rows: f.rows}, nil
}
func (f *fakeExec) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, capturedExec{args: args})
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func TestBackfillColumns_EncryptsBindsAndSkipsEncrypted(t *testing.T) {
	c := newTestCipher(t)
	target := BackfillTarget{Table: "subtitle_provider_config", Column: "api_key", KeyExpr: "provider_name"}

	// Pre-encrypted row must be skipped by the in-loop EncryptIfPlaintext guard.
	preEnc, err := c.Encrypt("already", RowAAD(target.Table, target.Column, "opensubtitles"))
	if err != nil {
		t.Fatalf("seed encrypt: %v", err)
	}
	db := &fakeExec{rows: [][]string{
		// {key, aadID, value}; for this target aadID == key (provider_name).
		{"subdl", "subdl", "plain-subdl-key"},
		{"opensubtitles", "opensubtitles", preEnc},
	}}

	n, err := BackfillColumns(context.Background(), db, c, []BackfillTarget{target})
	if err != nil {
		t.Fatalf("BackfillColumns: %v", err)
	}
	if n != 1 {
		t.Fatalf("encrypted = %d, want 1 (pre-encrypted row skipped)", n)
	}
	if len(db.execs) != 1 {
		t.Fatalf("exec count = %d, want 1", len(db.execs))
	}
	// args: [ciphertext, key, originalValue]
	got := db.execs[0].args
	ct, key, orig := got[0].(string), got[1].(string), got[2].(string)
	if key != "subdl" || orig != "plain-subdl-key" {
		t.Fatalf("update bound key=%q orig=%q", key, orig)
	}
	if !IsEncrypted(ct) {
		t.Fatalf("update value %q is not enc:v1:", ct)
	}
	// Ciphertext must decrypt under the row-bound AAD back to the original.
	plain, err := c.Decrypt(ct, RowAAD(target.Table, target.Column, key))
	if err != nil || plain != "plain-subdl-key" {
		t.Fatalf("decrypt = (%q, %v), want original", plain, err)
	}
}

func TestBackfillReferencedColumns_ResolveThenEncrypt(t *testing.T) {
	c := newTestCipher(t)
	target := BackfillTarget{Table: "request_integrations", Column: "api_key_ref", KeyExpr: "id::text"}

	// resolve mimics the settings decorator: a known settings key resolves to the
	// real credential; anything else is a literal (returns "").
	resolve := func(_ context.Context, ref string) (string, error) {
		if ref == "requests.radarr.api_key" {
			return "REAL-RADARR-KEY", nil
		}
		return "", nil
	}
	db := &fakeExec{rows: [][]string{
		{"int-1", "requests.radarr.api_key"}, // a reference → encrypt the resolved key
		{"int-2", "literal-key-xyz"},         // a literal → encrypt the value itself
	}}

	n, err := BackfillReferencedColumns(context.Background(), db, c, resolve, []BackfillTarget{target})
	if err != nil {
		t.Fatalf("BackfillReferencedColumns: %v", err)
	}
	if n != 2 || len(db.execs) != 2 {
		t.Fatalf("encrypted=%d execs=%d, want 2/2", n, len(db.execs))
	}

	want := map[string]string{
		"int-1": "REAL-RADARR-KEY", // resolved, not the setting name
		"int-2": "literal-key-xyz",
	}
	for _, e := range db.execs {
		ct, key, orig := e.args[0].(string), e.args[1].(string), e.args[2].(string)
		plain, err := c.Decrypt(ct, RowAAD(target.Table, target.Column, key))
		if err != nil {
			t.Fatalf("decrypt for %s: %v", key, err)
		}
		if plain != want[key] {
			t.Fatalf("row %s encrypted %q, want %q", key, plain, want[key])
		}
		// The UPDATE guard ($3) must be the ORIGINAL stored value so concurrent
		// boots converge (the reference name for int-1, the literal for int-2).
		if key == "int-1" && orig != "requests.radarr.api_key" {
			t.Fatalf("int-1 guard = %q, want the original reference", orig)
		}
	}
}
