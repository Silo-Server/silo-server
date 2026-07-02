package policy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/open-policy-agent/opa/v1/ast"
)

const compileCheckTimeout = 2 * time.Second

// LockedCapabilities returns the builtin set allowed for administrator-authored
// Rego. It removes non-pure and network-capable builtins from OPA's current
// capabilities.
func LockedCapabilities() *ast.Capabilities {
	caps := ast.CapabilitiesForThisVersion(ast.CapabilitiesRegoVersion(ast.RegoV1))
	builtins := caps.Builtins[:0]
	for _, builtin := range caps.Builtins {
		if lockedBuiltin(builtin.Name) {
			continue
		}
		builtins = append(builtins, builtin)
	}
	caps.Builtins = builtins
	return caps
}

func lockedBuiltin(name string) bool {
	switch name {
	case "http.send", "opa.runtime", "rego.parse_module", "rego.parse_modules":
		return true
	}
	return strings.HasPrefix(name, "net.")
}

// CompileCheck parses and compiles administrator-authored Rego against the
// embedded vendor bundle using the locked sandbox capabilities.
func CompileCheck(ctx context.Context, domain, source string) error {
	if !validPolicyDomain(domain) {
		return compileErrorMessage(fmt.Sprintf("unsupported policy domain %q", domain))
	}

	compileCtx, cancel := context.WithTimeout(ctx, compileCheckTimeout)
	defer cancel()

	caps := LockedCapabilities()
	candidatePath := customModulePath(domain)
	candidate, err := parsePolicyModule(candidatePath, source, caps)
	if err != nil {
		return err
	}
	if candidate == nil {
		return compileErrorMessage("policy source is empty")
	}

	expectedPackage := "silo_custom." + domain
	if actualPackage := modulePackageName(candidate); actualPackage != expectedPackage {
		location := candidate.Package.Location
		return compileErrorAt(
			location,
			fmt.Sprintf("policy package must be %s, got %s", expectedPackage, actualPackage),
		)
	}

	modules, err := compileCheckModules(caps)
	if err != nil {
		return err
	}
	modules[candidatePath] = candidate

	return compileModulesWithTimeout(compileCtx, modules, caps)
}

func compileCheckModules(caps *ast.Capabilities) (map[string]*ast.Module, error) {
	sources, err := vendorModules(false)
	if err != nil {
		return nil, err
	}
	modules := make(map[string]*ast.Module, len(sources)+1)
	for _, source := range sources {
		module, err := parsePolicyModule(source.Path, source.Source, caps)
		if err != nil {
			return nil, err
		}
		if module != nil {
			modules[source.Path] = module
		}
	}
	return modules, nil
}

func parsePolicyModule(path, source string, caps *ast.Capabilities) (*ast.Module, error) {
	module, err := ast.ParseModuleWithOpts(path, source, ast.ParserOptions{
		Capabilities: caps,
		RegoVersion:  ast.RegoV1,
	})
	if err != nil {
		return nil, compileErrorFromOPA(err)
	}
	return module, nil
}

func compileModulesWithTimeout(ctx context.Context, modules map[string]*ast.Module, caps *ast.Capabilities) error {
	if err := ctx.Err(); err != nil {
		return compileErrorMessage(err.Error())
	}

	result := make(chan error, 1)
	go func() {
		compiler := ast.NewCompiler().
			WithCapabilities(caps).
			WithDefaultRegoVersion(ast.RegoV1)
		compiler.Compile(modules)
		if compiler.Failed() {
			result <- compileErrorFromOPA(compiler.Errors)
			return
		}
		result <- nil
	}()

	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return compileErrorMessage(fmt.Sprintf("policy compile exceeded %s budget", compileCheckTimeout))
	}
}

func modulePackageName(module *ast.Module) string {
	if module == nil || module.Package == nil {
		return ""
	}
	return strings.TrimPrefix(module.Package.Path.String(), "data.")
}

func customModulePath(domain string) string {
	return "custom/" + domain + ".rego"
}

// Domains for admin-authored policy documents. Each maps to one
// silo_custom.<domain> package consulted by the matching vendor module.
const (
	DomainScope      = "scope"
	DomainPermission = "permission"
	DomainAction     = "action"
)

func validPolicyDomain(domain string) bool {
	switch domain {
	case DomainScope, DomainPermission, DomainAction:
		return true
	default:
		return false
	}
}

func compileErrorMessage(message string) *CompileError {
	return &CompileError{Issues: []CompileIssue{{Message: message}}}
}

func compileErrorAt(location *ast.Location, message string) *CompileError {
	issue := CompileIssue{Message: message}
	if location != nil {
		issue.Row = location.Row
		issue.Col = location.Col
	}
	return &CompileError{Issues: []CompileIssue{issue}}
}
