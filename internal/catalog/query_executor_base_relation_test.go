package catalog

import (
	"strings"
	"testing"
)

func TestPreviewPageSQLBindsBaseRelationArgsFirst(t *testing.T) {
	e := &QueryExecutor{
		BaseRelationSQL:    "(SELECT m.*, m.content_id AS access_content_id FROM media_items m WHERE m.title = $1) mi",
		BaseRelationArgs:   []any{"base-title"},
		LibraryContentExpr: "mi.access_content_id",
		SourceWhere:        "mi.year = $1",
		SourceArgs:         []any{1999},
	}
	sql, args, err := e.buildPreviewPageSQL(QueryDefinition{}, AccessFilter{AllowedLibraryIDs: []int{7}}, 10, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 2 || args[0] != "base-title" || args[1] != 1999 {
		t.Fatalf("args = %#v, want base arg then source arg", args)
	}
	for _, want := range []string{"m.title = $1", "mi.year = $2", "mil_scope_in.content_id = mi.access_content_id"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("SQL missing %q:\n%s", want, sql)
		}
	}
}

func TestPreviewPageSQLKeepsDefaultsWithoutBaseRelationArgs(t *testing.T) {
	e := &QueryExecutor{SourceWhere: "mi.year = $1", SourceArgs: []any{1999}}
	sql, args, err := e.buildPreviewPageSQL(QueryDefinition{}, AccessFilter{AllowedLibraryIDs: []int{7}}, 10, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != 1999 || !strings.Contains(sql, "mi.year = $1") || !strings.Contains(sql, "mil_scope_in.content_id = mi.content_id") {
		t.Fatalf("default plan changed:\n%s\nargs=%#v", sql, args)
	}
}
