package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// sessionEndWrite matches SQL that ends login sessions: it sets
// auth_sessions.revoked_at, which fires the auth_sessions_withdraw_preapprovals
// trigger on the device sign-ins those sessions approved in advance.
var sessionEndWrite = regexp.MustCompile(`(?is)\bupdate\s+(?:only\s+)?(?:"?public"?\.)?"?auth_sessions\b[^;]*?\bset\b[^;]*?\brevoked_at\s*=`)

// requestLockers are the calls that take the request rows a session end
// writes, in id order, before the session rows.
var requestLockers = map[string]bool{
	"lockSessionPreapprovals":                    true,
	"withdrawDeviceSignInApprovalsInTransaction": true,
}

// sessionEndersWithoutPreapprovals end only sessions that cannot have
// approved a device sign-in in advance (approving one needs the person's
// own login session), or have no caller.
var sessionEndersWithoutPreapprovals = map[string]string{
	"internal/auth/owner.go:moveOwnership":                   "ends only sessions viewing as another account",
	"internal/auth/repository.go:endImpersonationsInvolving": "ends only sessions viewing as another account",
	"internal/auth/session.go:RevokeAllByUser":               "no caller",
	"internal/auth/session.go:RevokeAllByImpersonator":       "no caller",
}

// Every statement that ends login sessions is preceded, on every path to it
// in its function, by a call that locks the request rows those sessions
// approved in advance (requestLockers). A path that ends a session first and
// lets the trigger take the request rows after deadlocks with one that takes
// them in the documented order. The check reads source: it finds a locker
// among the statements run unconditionally before the one holding the SQL
// (in its block or an enclosing one), and the staged tests cover the orders
// themselves.
func TestSessionEndersLockRequestsFirst(t *testing.T) {
	root := filepath.Join("..", "..")
	seen := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for name, ok := range sessionEndsLockedFirst(t, path) {
			key := filepath.ToSlash(rel) + ":" + name
			seen[key] = true
			if _, exempt := sessionEndersWithoutPreapprovals[key]; exempt {
				continue
			}
			if !ok {
				t.Errorf("%s ends login sessions without first locking the device sign-ins they approved in advance (lockSessionPreapprovals)", key)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range sessionEndersWithoutPreapprovals {
		if !seen[key] {
			t.Errorf("sessionEndersWithoutPreapprovals lists %s, which no longer ends sessions; remove it", key)
		}
	}
}

// sessionEndsLockedFirst reports, per function in path that ends sessions,
// whether every such statement is preceded by a request locker.
func sessionEndsLockedFirst(t *testing.T, path string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !sessionEndWrite.Match(src) {
		return nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var stack []ast.Node
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || !sessionEndWrite.MatchString(value) {
				return true
			}
			locked := lockedBefore(stack)
			if prev, seen := out[fn.Name.Name]; !seen || prev {
				out[fn.Name.Name] = locked
			}
			return true
		})
	}
	return out
}

// lockedBefore reports whether, for the node at the top of stack, some
// enclosing block runs a request locker in a statement before the one that
// leads to the node.
func lockedBefore(stack []ast.Node) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		block, ok := stack[i].(*ast.BlockStmt)
		if !ok {
			continue
		}
		child := stack[i+1]
		for _, stmt := range block.List {
			if stmt == child {
				break
			}
			if callsRequestLocker(stmt) {
				return true
			}
		}
	}
	return false
}

// callsRequestLocker reports whether stmt itself, not a branch inside it,
// calls a request locker: `f(...)`, `x, err := f(...)`, or the init of
// `if err := f(...); err != nil`.
func callsRequestLocker(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		return isLockerCall(s.X)
	case *ast.AssignStmt:
		for _, rhs := range s.Rhs {
			if isLockerCall(rhs) {
				return true
			}
		}
	case *ast.IfStmt:
		if s.Init != nil {
			return callsRequestLocker(s.Init)
		}
	}
	return false
}

func isLockerCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return requestLockers[fn.Name]
	case *ast.SelectorExpr:
		return requestLockers[fn.Sel.Name]
	}
	return false
}
