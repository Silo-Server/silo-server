package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestCollisions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  []group
	}{
		{
			name: "module paths equal once the extension is removed",
			files: []string{
				"web/src/pages/admin-users/detail/UserDetailTabs.tsx",
				"web/src/pages/admin-users/detail/userDetailTabs.ts",
			},
			want: []group{{module: true, paths: []string{
				"web/src/pages/admin-users/detail/UserDetailTabs.tsx",
				"web/src/pages/admin-users/detail/userDetailTabs.ts",
			}}},
		},
		{
			name: "renamed module no longer collides",
			files: []string{
				"web/src/pages/admin-users/detail/UserDetailTabBar.tsx",
				"web/src/pages/admin-users/detail/userDetailTabs.ts",
			},
		},
		{
			name: "stylesheets are imported with their extension",
			files: []string{
				"web/src/App.tsx", "web/src/app.css",
				"web/src/pages/DetailLayout.tsx", "web/src/pages/detailLayout.css",
			},
		},
		{
			name:  "same module name with different extensions is not a case problem",
			files: []string{"web/src/foo.ts", "web/src/foo.tsx", "web/src/foo.d.ts", "web/src/foo.test.ts"},
		},
		{
			name:  "declaration file and module",
			files: []string{"web/src/Foo.d.ts", "web/src/foo.js"},
			want:  []group{{module: true, paths: []string{"web/src/Foo.d.ts", "web/src/foo.js"}}},
		},
		{
			name:  "every module extension",
			files: []string{"a/X.mjs", "a/x.cjs", "b/Y.jsx", "b/y.ts"},
			want: []group{
				{module: true, paths: []string{"a/X.mjs", "a/x.cjs"}},
				{module: true, paths: []string{"b/Y.jsx", "b/y.ts"}},
			},
		},
		{
			name:  "module trees outside web are checked too",
			files: []string{"tools/gen/Index.js", "tools/gen/index.mjs"},
			want:  []group{{module: true, paths: []string{"tools/gen/Index.js", "tools/gen/index.mjs"}}},
		},
		{
			name:  "directory index module and sibling module",
			files: []string{"web/src/UserDetailTabs/index.ts", "web/src/userDetailTabs.ts"},
			want: []group{{module: true, paths: []string{
				"web/src/UserDetailTabs/index.ts", "web/src/userDetailTabs.ts",
			}}},
		},
		{
			name:  "every index module extension names its directory",
			files: []string{"a/Foo/index.d.ts", "a/foo.js", "b/Bar/index.mjs", "b/bar.tsx"},
			want: []group{
				{module: true, paths: []string{"a/Foo/index.d.ts", "a/foo.js"}},
				{module: true, paths: []string{"b/Bar/index.mjs", "b/bar.tsx"}},
			},
		},
		{
			name:  "index module beside a stylesheet",
			files: []string{"web/src/Foo/index.ts", "web/src/Foo.css", "web/src/foo.css"},
			want:  []group{{paths: []string{"web/src/Foo.css", "web/src/foo.css"}}},
		},
		{
			name:  "index module beside a same-case sibling resolves alike everywhere",
			files: []string{"web/src/foo/index.ts", "web/src/foo.ts", "web/src/foo/index.tsx"},
		},
		{
			name:  "index module alone",
			files: []string{"web/src/foo/index.ts", "web/src/foo/Bar.tsx", "index.ts"},
		},
		{
			name:  "a module named like index elsewhere in the path is not an index",
			files: []string{"web/src/Foo/indexes.ts", "web/src/foo.ts", "web/src/index/Foo.ts"},
		},
		{
			name:  "index modules in directories equal ignoring case are reported once each way",
			files: []string{"web/src/A/index.ts", "web/src/a/index.js"},
			want: []group{
				{paths: []string{"web/src/A", "web/src/a"}},
				{module: true, paths: []string{"web/src/A/index.ts", "web/src/a/index.js"}},
			},
		},
		{
			name:  "same file ignoring case is reported once, as a path",
			files: []string{"web/src/Foo.ts", "web/src/foo.ts"},
			want:  []group{{paths: []string{"web/src/Foo.ts", "web/src/foo.ts"}}},
		},
		{
			name:  "same non-module file ignoring case",
			files: []string{"docs/README.md", "docs/readme.md"},
			want:  []group{{paths: []string{"docs/README.md", "docs/readme.md"}}},
		},
		{
			name:  "directories equal ignoring case",
			files: []string{"web/Src/a.ts", "web/src/b.ts"},
			want:  []group{{paths: []string{"web/Src", "web/src"}}},
		},
		{
			name:  "directory and file equal ignoring case",
			files: []string{"internal/Notes", "internal/notes/a.go"},
			want:  []group{{paths: []string{"internal/Notes", "internal/notes"}}},
		},
		{
			name:  "non-ASCII letters fold",
			files: []string{"docs/Ä.md", "docs/ä.md"},
			want:  []group{{paths: []string{"docs/Ä.md", "docs/ä.md"}}},
		},
		{
			name:  "duplicate entries are one path",
			files: []string{"web/src/a.ts", "web/src/a.ts"},
		},
		{
			name:  "other extensions are not stripped",
			files: []string{"cmd/Foo.go", "cmd/foo_test.go", "web/Foo.json", "web/foo.ts"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collisions(tc.files)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("collisions(%q)\n got %+v\nwant %+v", tc.files, got, tc.want)
			}
		})
	}
}

func TestReportNamesEveryPath(t *testing.T) {
	got := report([]group{
		{paths: []string{"web/src/Foo.ts", "web/src/foo.ts"}},
		{module: true, paths: []string{"web/src/Bar.tsx", "web/src/bar.ts"}},
	})
	for _, want := range []string{
		"::error::2 group(s)",
		"same path ignoring case:\n  web/src/Foo.ts\n  web/src/foo.ts\n",
		"same import path ignoring case (an extensionless import can resolve to any of them):\n  web/src/Bar.tsx\n  web/src/bar.ts\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
}
