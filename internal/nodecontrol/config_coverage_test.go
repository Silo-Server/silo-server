package nodecontrol

import (
	"go/ast"
	"go/types"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/Silo-Server/silo-server/internal/config"
)

// nodeConfigReadsNotCarried are the config fields the transcode node reads
// that the document deliberately does not carry, each with the reason. It is
// empty: a field belongs here only with a reason the node can run without it.
var nodeConfigReadsNotCarried = map[string]string{}

// The node builds its *config.Config from the document (NodeConfigBody.ApplyTo).
// A field internal/transcodenode reads that the document does not fill would
// be zero on a node that takes its configuration from the core, so this test
// fails when the node starts reading a config field the document does not
// carry. It type-checks the package and records every field selected on a
// struct type of internal/config, however the value was reached (cfg.Playback.X,
// or a copy of cfg.Playback).
func TestDocumentCarriesEveryConfigFieldTheNodeReads(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks internal/transcodenode")
	}
	const nodePackage = "github.com/Silo-Server/silo-server/internal/transcodenode"
	configPackage := reflect.TypeFor[config.Config]().PkgPath()
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedImports,
		Dir:  "../..",
	}, nodePackage)
	if err != nil {
		t.Fatal(err)
	}
	if packages.PrintErrors(loaded) > 0 || len(loaded) != 1 {
		t.Fatal("cannot type-check", nodePackage)
	}
	pkg := loaded[0]

	// The section of config.Config each struct type of the config package is.
	sections := map[string]string{}
	root := reflect.TypeFor[config.Config]()
	for field := range root.Fields() {
		if field.Type.Kind() == reflect.Struct && field.Type.PkgPath() == configPackage {
			sections[field.Type.Name()] = field.Name
		}
	}

	read := map[string]bool{}
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			selection := pkg.TypesInfo.Selections[sel]
			if selection == nil || selection.Kind() != types.FieldVal {
				return true
			}
			recv := selection.Recv()
			if ptr, ok := recv.(*types.Pointer); ok {
				recv = ptr.Elem()
			}
			named, ok := recv.(*types.Named)
			if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != configPackage {
				return true
			}
			typeName, field := named.Obj().Name(), sel.Sel.Name
			switch {
			case typeName == root.Name():
				// cfg.Playback itself: the fields read off it are recorded
				// under their section.
			case sections[typeName] != "":
				read[sections[typeName]+"."+field] = true
			default:
				// A nested config struct this test does not map yet.
				read[typeName+"."+field] = true
			}
			return true
		})
	}
	if len(read) == 0 {
		t.Fatal("found no config reads in the node; the scan is broken")
	}

	// A document with every field set, applied to an empty config.
	var body NodeConfigBody
	fillNonZero(reflect.ValueOf(&body).Elem())
	var cfg config.Config
	body.ApplyTo(&cfg)
	applied := reflect.ValueOf(cfg)

	for _, path := range slices.Sorted(maps.Keys(read)) {
		if _, ok := nodeConfigReadsNotCarried[path]; ok {
			continue
		}
		section, field, _ := strings.Cut(path, ".")
		value := applied.FieldByName(section)
		if value.IsValid() {
			value = value.FieldByName(field)
		}
		if !value.IsValid() {
			t.Errorf("internal/transcodenode reads %s, which this test cannot map to a config.Config field", path)
			continue
		}
		if value.IsZero() {
			t.Errorf("internal/transcodenode reads config %s, which the node configuration document does not carry; add it to NodeConfigBody and ApplyTo", path)
		}
	}
}

// fillNonZero sets every leaf of v to a value other than its zero.
func fillNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			fillNonZero(v.Field(i))
		}
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(1)
	default:
		panic("fillNonZero: unhandled kind " + v.Kind().String())
	}
}
