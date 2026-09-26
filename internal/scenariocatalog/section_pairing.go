package scenariocatalog

import "net/http"

// RequiredSectionReadScenarios pins the existing frozen section read cases.
var RequiredSectionReadScenarios = []string{
	"overrides_get.ok",
	"overrides_get.empty",
	"overrides_get.scope_default",
	"overrides_get.shape",
	"overrides_get.no_profile",
	"overrides_get.unknown_profile",
	"overrides_get.other_account_profile",
	"overrides_get.no_token",
	"section_settings.ok",
	"section_settings.empty",
	"section_settings.bad_library_id",
	"section_settings.scope",
	"section_settings.shape",
	"section_settings.no_profile",
	"section_settings.other_account_profile",
	"section_settings.no_token",
	"section_flags.ok",
	"section_flags.off",
	"section_flags.on",
	"section_flags.shape",
	"section_flags.no_profile",
	"section_flags.other_account_profile",
	"section_flags.no_token",
	"section_flags.error_shape",
}

func SectionReadAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	return requiredAcceptance(catalogs, http.MethodGet, []string{
		"/api/v1/profile/sections/", "/api/v1/profile/sections/settings", "/api/v1/profile/sections/flags",
	}, RequiredSectionReadScenarios)
}
