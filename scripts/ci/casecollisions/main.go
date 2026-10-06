// Command casecollisions fails when two tracked paths would name the same
// file on a case-insensitive file system, the default on macOS and Windows.
//
// Linux CI checks out such a pair without complaint, so a collision only
// shows up when someone builds on another platform. Two kinds are reported:
//
//   - paths equal ignoring case, for files and for the directories that
//     contain them (web/src/Foo.ts and web/src/foo.ts, or web/Src/a.ts and
//     web/src/b.ts);
//   - JavaScript and TypeScript modules whose paths are equal ignoring case
//     once the module extension is removed (UserDetailTabs.tsx and
//     userDetailTabs.ts), because an extensionless import resolves to either.
//     A directory index module also answers to its directory's name, so
//     UserDetailTabs/index.ts collides with userDetailTabs.ts: ./UserDetailTabs
//     resolves to the index on Linux and to the sibling file on macOS.
//
// Files imported with their extension, such as stylesheets, only collide when
// the whole path does: App.tsx and app.css are fine.
//
// Usage (the repository root is the cwd):
//
//	go run ./scripts/ci/casecollisions
package main

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"unicode"
)

// moduleExtensions are the extensions TypeScript and bundlers try for an
// extensionless import. Longer suffixes come first so .d.ts wins over .ts.
var moduleExtensions = []string{".d.ts", ".tsx", ".ts", ".jsx", ".js", ".mjs", ".cjs"}

// module is a JS or TS file and one extensionless import path that resolves
// to it: the path without its extension, or the directory of an index module.
type module struct {
	file, importPath string
}

// group is a set of tracked paths that one case-insensitive name would cover.
type group struct {
	module bool // equal only once the module extension is removed
	paths  []string
}

func main() {
	out, err := exec.Command("git", "ls-files", "-z", "--full-name", "--", ":/").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "casecollisions: git ls-files: %v\n", err)
		os.Exit(1)
	}
	var files []string
	for _, path := range strings.Split(string(out), "\x00") {
		if path != "" {
			files = append(files, path)
		}
	}

	groups := collisions(files)
	if len(groups) == 0 {
		fmt.Printf("casecollisions: %d tracked files, no paths collide ignoring case\n", len(files))
		return
	}
	fmt.Print(report(groups))
	os.Exit(1)
}

// collisions returns every group of paths that collide ignoring case, sorted
// by their first path.
func collisions(files []string) []group {
	files = slices.Clone(files)
	slices.Sort(files)
	files = slices.Compact(files)

	// Every file and every directory above it.
	names := map[string]bool{}
	for _, file := range files {
		for name := file; name != "" && !names[name]; name = parent(name) {
			names[name] = true
		}
	}
	byFold := map[string][]string{}
	for name := range names {
		key := fold(name)
		byFold[key] = append(byFold[key], name)
	}
	var groups []group
	for _, paths := range byFold {
		if len(paths) > 1 {
			slices.Sort(paths)
			groups = append(groups, group{paths: paths})
		}
	}

	byImport := map[string][]module{}
	for _, file := range files {
		for _, importPath := range importPaths(file) {
			key := fold(importPath)
			byImport[key] = append(byImport[key], module{file: file, importPath: importPath})
		}
	}
	// Two import paths can name the same files (A/index.ts and a/index.js
	// collide as A/index and as A), so each set of files is reported once.
	reported := map[string]bool{}
	for _, modules := range byImport {
		paths := moduleCollision(modules)
		id := strings.Join(paths, "\x00")
		if paths != nil && !reported[id] {
			reported[id] = true
			groups = append(groups, group{module: true, paths: paths})
		}
	}

	slices.SortFunc(groups, func(a, b group) int {
		return strings.Compare(a.paths[0], b.paths[0])
	})
	return groups
}

// moduleCollision returns the sorted files of modules whose import paths
// fold equal, or nil unless two of them differ in case and are not already
// the same path ignoring case. foo.ts beside foo.tsx or foo/index.ts differs
// only in what the resolver tries first, which is the same on every file
// system; Foo.ts and foo.ts are reported as a path collision instead.
func moduleCollision(modules []module) []string {
	collides := false
	for i, a := range modules {
		for _, b := range modules[i+1:] {
			if a.importPath != b.importPath && fold(a.file) != fold(b.file) {
				collides = true
			}
		}
	}
	if !collides {
		return nil
	}
	var paths []string
	for _, m := range modules {
		paths = append(paths, m.file)
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// importPaths returns the extensionless import paths that resolve to file:
// none for a file that is not a module, the path without its extension, and
// for an index module also its directory.
func importPaths(file string) []string {
	base, ok := stripModuleExtension(file)
	if !ok {
		return nil
	}
	paths := []string{base}
	if dir := parent(base); dir != "" && base[len(dir)+1:] == "index" {
		paths = append(paths, dir)
	}
	return paths
}

func stripModuleExtension(path string) (string, bool) {
	for _, ext := range moduleExtensions {
		if base, ok := strings.CutSuffix(path, ext); ok && base != "" && !strings.HasSuffix(base, "/") {
			return base, true
		}
	}
	return "", false
}

func parent(path string) string {
	i := strings.LastIndexByte(path, '/')
	if i < 0 {
		return ""
	}
	return path[:i]
}

// fold maps every rune to the smallest rune in its Unicode case-folding
// orbit, so two strings fold equal exactly when strings.EqualFold says so.
func fold(s string) string {
	return strings.Map(func(r rune) rune {
		smallest := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			smallest = min(smallest, f)
		}
		return smallest
	}, s)
}

func report(groups []group) string {
	var b strings.Builder
	fmt.Fprintf(&b, "::error::%d group(s) of tracked paths collide on a case-insensitive file system (macOS, Windows); rename all but one path in each group\n", len(groups))
	for _, g := range groups {
		if g.module {
			b.WriteString("\nsame import path ignoring case (an extensionless import can resolve to any of them):\n")
		} else {
			b.WriteString("\nsame path ignoring case:\n")
		}
		for _, path := range g.paths {
			fmt.Fprintf(&b, "  %s\n", path)
		}
	}
	return b.String()
}
