package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Why a reader of an account's permissions cannot hand a session another
// app's key opened a power that is refused to it (docs/architecture/app-keys.md,
// "What a granted app can do").
const (
	// An operation's own permission gate: the origin gate refuses such a
	// session every operation it gates before this runs.
	operationGate = "the gate of an operation that is refused to such a session first"
	// Builds the account view; apiv2's accountForSession filters what such a
	// session reads (permittedPermissions).
	accountView = "the account view, filtered for such a session by accountForSession"
	// An administrator's view of another account; such a session never acts
	// as an administrator.
	adminView = "an administrator's view"
	// Refuses such a session itself before reading the permission.
	refusesGrantedApp = "answers false for such a session before reading the permission"
	// Names a permission as data (an operation's declared permission, a
	// list of permissions an invitation may carry, a policy simulation).
	declaration = "names the permission as data, decides nothing for a session"
)

// accountPermissionReaders is every function outside internal/auth and
// internal/policy that resolves an account's permissions
// (PolicyPermissions) or names one permission (PermissionMetadataCuration,
// PermissionMarkerEdit), keyed by file and enclosing function, with why a
// session another app's key opened gets nothing from it that the operation
// table refuses. A new by-hand permission check fails the test below until it
// is listed, which is the moment to make it answer for the session, not the
// account.
var accountPermissionReaders = map[string]string{
	"internal/api/handlers/auth.go:effectiveAccountPolicy": accountView,
	"internal/api/handlers/auth.go:buildUserResponse":      accountView,
	"internal/apiv2/account.go:accountFromView":            accountView,
	"internal/api/handlers/admin.go:toAdminUserResponse":   adminView,
	// Its one caller, CanViewFilePaths, answers false for such a session first.
	"internal/api/handlers/items.go:hasMaskedMetadataCuration":                       refusesGrantedApp,
	"internal/api/middleware/permissions.go:RequireMetadataCurationForItem":          operationGate,
	"internal/api/middleware/permissions.go:RequireMarkerEdit":                       operationGate,
	"internal/api/middleware/policy_gates.go:RequireMetadataCurationForItem":         operationGate,
	"internal/api/middleware/policy_gates.go:RequireMarkerEdit":                      operationGate,
	"internal/api/router.go:v2Dependencies":                                          operationGate,
	"internal/apiv2/admin_catalog_item_metadata.go:registerAdminCatalogItemMetadata": declaration,
	"internal/apiv2/admin_catalog_match.go:registerAdminCatalogMatch":                declaration,
	"internal/apiv2/admin_catalog_translation.go:registerAdminCatalogTranslation":    declaration,
	"internal/apiv2/markers.go:registerMarkers":                                      declaration,
	"internal/apiv2/registry.go:<package scope>":                                     declaration,
	"internal/apiv2/registry.go:checkOperation":                                      declaration,
	"internal/invitations/app_code.go:<package scope>":                               declaration,
}

func TestEveryAccountPermissionReaderIsListed(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if name := d.Name(); name == "testdata" || name == "node_modules" || rel == "internal/auth" || rel == "internal/policy" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, fn := range accountPermissionReadingFunctions(t, path) {
			found[rel+":"+fn] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, reader := range slices.Sorted(maps.Keys(found)) {
		if _, ok := accountPermissionReaders[reader]; !ok {
			t.Errorf("%s reads an account's permissions by hand; make it answer for the session (a session another app's key opened gets a permission only while an operation it gates is open to it) and list it in accountPermissionReaders", reader)
		}
	}
	for _, reader := range slices.Sorted(maps.Keys(accountPermissionReaders)) {
		if !found[reader] {
			t.Errorf("accountPermissionReaders lists %s, which no longer reads permissions; remove it", reader)
		}
	}
}

// accountPermissionReadingFunctions returns the functions in path that call
// PolicyPermissions or name a permission constant; one outside any function
// is "<package scope>".
func accountPermissionReadingFunctions(t *testing.T, path string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "PolicyPermissions") && !strings.Contains(string(src), "PermissionMetadataCuration") &&
		!strings.Contains(string(src), "PermissionMarkerEdit") {
		return nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	visit := func(name string, node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "PolicyPermissions", "PermissionMetadataCuration", "PermissionMarkerEdit":
				if !slices.Contains(out, name) {
					out = append(out, name)
				}
			}
			return true
		})
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			visit(fn.Name.Name, fn)
		} else {
			visit("<package scope>", decl)
		}
	}
	return out
}
