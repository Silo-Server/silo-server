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
	"strconv"
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
	// Resolves the account's effective policy for another reader; what that
	// reader decides is the one listed.
	policyResolution = "resolves the effective policy another listed reader uses"
	// Reads or writes permissions while a backup is restored, outside any
	// session.
	restore = "restores accounts from a backup, outside any session"
	// Records the account's permissions in an audit entry.
	auditRecord = "records the account's values in an audit entry"
	// The filter itself: keeps for such a session only the permissions with
	// an operation open to it.
	sessionFilter = "the filter that keeps such a session's permissions to its own (permittedPermissions)"
)

// accountPermissionReaders is every function outside internal/auth and
// internal/policy that resolves an account's permissions
// (PolicyPermissions), reads a field named Permissions, or names one
// permission (a Permission constant of this package, or a string equal to
// one's value), keyed by file and enclosing function, with why a
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
	"internal/apiv2/wire.go:<package scope>":                                         declaration,
	"internal/routeinventory/classify.go:<package scope>":                            declaration,
	"internal/apiv2/account.go:accountForSession":                                    sessionFilter,
	"internal/apiv2/admin_accounts.go:createAdminAccount":                            adminView,
	"internal/apiv2/admin_accounts.go:updateAdminAccount":                            adminView,
	"internal/apiv2/admin_users.go:adminUserFromView":                                adminView,
	"internal/access/groups.go:ApplyGroupPolicy":                                     policyResolution,
	"internal/access/groups.go:WiderThan":                                            policyResolution,
	"internal/auditmutation/mutation.go:UserValues":                                  auditRecord,
	"internal/backup/restoreset_users.go:access":                                     restore,
	"internal/backup/restoreset_users.go:effectivePolicyOf":                          restore,
	"internal/backup/restoreset_users.go:readBackupAccounts":                         restore,
	"internal/backup/restoreset_users.go:user":                                       restore,
	"internal/backup/restoreset_users.go:validateAccountEntry":                       restore,
	"internal/backup/users_apply.go:restoredAccount":                                 restore,
}

func TestEveryAccountPermissionReaderIsListed(t *testing.T) {
	root := filepath.Join("..", "..")
	names := permissionNames(t)
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
		for _, fn := range accountPermissionReadingFunctions(t, path, names) {
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

// permissionVocabulary is what names a permission in source: the
// identifiers of the Permission constants declared in this package and
// their string values, read from the declarations so a permission added
// later is covered the day it is added.
type permissionVocabulary struct {
	idents map[string]bool
	values map[string]bool
}

func permissionNames(t *testing.T) permissionVocabulary {
	t.Helper()
	out := permissionVocabulary{idents: map[string]bool{"PolicyPermissions": true}, values: map[string]bool{}}
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if typ, isIdent := value.Type.(*ast.Ident); !ok || !isIdent || typ.Name != "Permission" {
					continue
				}
				for i, name := range value.Names {
					out.idents[name.Name] = true
					if lit, isLit := value.Values[i].(*ast.BasicLit); isLit {
						if v, err := strconv.Unquote(lit.Value); err == nil {
							out.values[v] = true
						}
					}
				}
			}
		}
	}
	if len(out.values) == 0 {
		t.Fatal("found no Permission constants to look for")
	}
	return out
}

// accountPermissionReadingFunctions returns the functions in path that name a
// permission (a Permission constant, or a string literal equal to one's
// value), call PolicyPermissions, or read a field named Permissions; one
// outside any function is "<package scope>".
//
// Its limits: it reads source, so it does not see a permission read in SQL
// (a query on users.permissions), a reader reached through an interface or a
// value passed in from elsewhere, or a field named otherwise.
func accountPermissionReadingFunctions(t *testing.T, path string, names permissionVocabulary) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	visit := func(name string, node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			hit := false
			switch x := n.(type) {
			case *ast.SelectorExpr:
				hit = names.idents[x.Sel.Name] || x.Sel.Name == "Permissions"
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					v, err := strconv.Unquote(x.Value)
					hit = err == nil && names.values[v]
				}
			}
			if hit && !slices.Contains(out, name) {
				out = append(out, name)
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
