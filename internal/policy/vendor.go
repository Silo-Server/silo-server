package policy

import (
	"embed"
	"io/fs"
	"path/filepath"
	"strings"
)

//go:embed vendor
var vendorFS embed.FS

// ModuleSource is one Rego module loaded into an OPA engine.
type ModuleSource struct {
	Path   string
	Source string
}

// VendorModules returns embedded vendor Rego modules excluding test modules.
func VendorModules() ([]ModuleSource, error) {
	return vendorModules(false)
}

func vendorModules(includeTests bool) ([]ModuleSource, error) {
	var modules []ModuleSource
	err := fs.WalkDir(vendorFS, "vendor", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".rego" {
			return nil
		}
		if !includeTests && strings.HasSuffix(path, "_test.rego") {
			return nil
		}
		source, err := vendorFS.ReadFile(path)
		if err != nil {
			return err
		}
		modules = append(modules, ModuleSource{
			Path:   path,
			Source: string(source),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return modules, nil
}
