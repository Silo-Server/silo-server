package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// How a writer of an account's sign-in rows stays out of a deadlock with a
// plugin uninstall, which deletes identities and then clears the requests and
// sessions that named them (see "Lock order with a plugin uninstall" in
// docs/architecture/external-sign-in.md).
const (
	// Locks the account row, then the account's identities, before it writes
	// requests or sessions.
	identitiesFirst = "account row, then identities, then requests and sessions"
	// One statement on at most one row of one table.
	oneRow = "one row"
	// Writes only sessions that carry no identity, which an uninstall never
	// locks.
	noIdentityRows = "rows that carry no identity"
	// Skips rows another transaction holds; never waits.
	neverWaits = "never waits"
	// Deletes an account created in the same request, before it has any
	// identity, request or session.
	newAccount = "account with no sign-in rows yet"
	// No caller.
	unused = "no caller"
	// Clears several requests approved for a profile without taking the
	// account's identities. It can deadlock with an uninstall only when two
	// requests approved for that profile through one identity are still
	// retained and the two lock them in opposite orders.
	knownGap = "known gap: several requests, no identity lock"
)

// signInRowWriters is every statement outside tests that writes
// auth_sessions or device_login_requests, or deletes identities, accounts,
// installations or profiles (whose foreign-key actions write them), keyed by
// file and enclosing function, with the rule that keeps it from deadlocking
// with a plugin uninstall. The test below checks that every writer is listed
// here, not that it keeps the rule named beside it: the rule is for whoever
// adds or changes a writer to read and check.
var signInRowWriters = map[string]string{
	// The uninstall itself: installation, then identities (cascade), then
	// device sign-in requests and sessions (SET NULL).
	"internal/plugins/installation.go:Delete": "the uninstall",

	"internal/auth/admin_users.go:MutateAdminAccount":                          identitiesFirst,
	"internal/auth/password_reset.go:revokeSignInsInTransaction":               identitiesFirst,
	"internal/auth/device_login.go:withdrawDeviceSignInApprovalsInTransaction": identitiesFirst,
	"internal/auth/session.go:RevokeAsAdmin":                                   identitiesFirst,
	"internal/auth/repository.go:ReplaceTemporaryPassword":                     identitiesFirst,
	"internal/auth/provider_recheck.go:applyAnswer":                            identitiesFirst,
	"internal/auth/identity_repository.go:deleteIdentity":                      identitiesFirst,
	"internal/auth/device_login.go:Approve":                                    identitiesFirst,
	"internal/auth/device_login.go:approveForProfile":                          identitiesFirst,
	"internal/auth/device_login.go:poll":                                       identitiesFirst,

	"internal/auth/session.go:createWithQuerier":                 oneRow,
	"internal/auth/device_login.go:markOpened":                   oneRow,
	"internal/auth/device_login.go:Cancel":                       oneRow,
	"internal/auth/device_login.go:Deny":                         oneRow,
	"internal/auth/device_login.go:withdrawQuickConnectApproval": oneRow,
	"internal/auth/device_login.go:create":                       oneRow,
	"internal/auth/device_login.go:createWithFreshCodes":         oneRow,
	"internal/auth/session.go:Revoke":                            oneRow,
	"internal/auth/session.go:revokeChecked":                     oneRow,
	"internal/auth/session.go:UpdateLastSeen":                    oneRow,
	"internal/auth/session.go:ExtendExpiresAt":                   oneRow,

	// Inserts a new identity, which no uninstall can see yet, and gives it
	// to the account's sessions that had none.
	"internal/auth/account_resolution.go:linkIdentityTx":     noIdentityRows,
	"internal/auth/owner.go:moveOwnership":                   noIdentityRows,
	"internal/auth/repository.go:endImpersonationsInvolving": noIdentityRows,

	"internal/auth/session.go:DeleteExpired":      neverWaits,
	"internal/auth/device_login.go:DeleteExpired": neverWaits,

	"internal/auth/repository.go:Delete":                          newAccount,
	"internal/auth/session.go:RevokeAllByUser":                    unused,
	"internal/auth/session.go:RevokeAllByImpersonator":            unused,
	"internal/userstore/pgstore/profiles.go:DeleteProfileAudited": knownGap,
}

// signInRowWrite matches SQL that writes the tables, allowing a schema and
// any spacing; a table name inside a longer one does not match.
var signInRowWrite = regexp.MustCompile(`(?is)\b(?:update(?:\s+only)?|insert\s+into|delete\s+from(?:\s+only)?)\s+(?:"?public"?\.)?"?(?:auth_sessions|device_login_requests)\b` +
	`|\bdelete\s+from(?:\s+only)?\s+(?:"?public"?\.)?"?(?:plugin_auth_identities|users|plugin_installations|user_profiles)\b`)

// Every writer of an account's sign-in rows is listed in signInRowWriters
// with the rule that keeps it from deadlocking with a plugin uninstall. A new
// writer is added there once it follows one of them.
func TestEverySignInRowWriterKeepsTheLockOrder(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			for _, fn := range signInRowWritingFunctions(t, path) {
				found[filepath.ToSlash(rel)+":"+fn] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, writer := range slices.Sorted(maps.Keys(found)) {
		if _, ok := signInRowWriters[writer]; !ok {
			t.Errorf("%s writes login sessions, device sign-in requests or a row whose deletion clears them; lock the account row and then its identities first (lockIdentitiesBeforeSessions) and add it to signInRowWriters with its rule", writer)
		}
	}
	for _, writer := range slices.Sorted(maps.Keys(signInRowWriters)) {
		if !found[writer] {
			t.Errorf("signInRowWriters lists %s, which no longer writes those rows; remove it", writer)
		}
	}
}

// signInRowWritingFunctions returns the functions in path holding a string
// literal that signInRowWrite matches; one outside any function is reported
// as "<package scope>".
func signInRowWritingFunctions(t *testing.T, path string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !signInRowWrite.Match(src) {
		return nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	visit := func(name string, node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if value, err := strconv.Unquote(lit.Value); err == nil && signInRowWrite.MatchString(value) {
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
