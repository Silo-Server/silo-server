package apiv2

import (
	"reflect"

	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/collections/templates"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/Silo-Server/silo-server/internal/onboarding"
	"github.com/danielgtaylor/huma/v2"
)

// Domain-owned documents have explicit product names in the native contract.
// These names are API identifiers, independent of their Go package spelling.
var domainSchemaNames = map[reflect.Type]string{
	reflect.TypeFor[templates.Bundle]():         "CollectionTemplateBundle",
	reflect.TypeFor[templates.Catalog]():        "CollectionTemplateCatalog",
	reflect.TypeFor[templates.Template]():       "CollectionTemplate",
	reflect.TypeFor[onboarding.Step]():          "OnboardingStep",
	reflect.TypeFor[onboarding.Flow]():          "OnboardingFlow",
	reflect.TypeFor[diagnostics.IngestResult](): "DiagnosticsIngestResult",
	reflect.TypeFor[catalogseed.PathRewrite]():  "CatalogImportPathRewrite",
}

func nativeSchemaName(t reflect.Type, hint string) string {
	base := t
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	if name, ok := domainSchemaNames[base]; ok {
		return name
	}
	return huma.DefaultSchemaNamer(t, hint)
}
