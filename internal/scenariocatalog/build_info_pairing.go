package scenariocatalog

import (
	"fmt"
	"net/http"
)

// RequiredBuildInfoScenarios contains only the selected unpaired frozen cases.
var RequiredBuildInfoScenarios = []string{
	"build.ok", "build.meaning", "build.shape", "build.no_token",
}

func BuildInfoAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	return buildInfoAcceptance(catalogs, RequiredBuildInfoScenarios)
}

// RequiredBuildAuthorityScenarios keeps the remaining three frozen refusals separate.
var RequiredBuildAuthorityScenarios = []string{
	"build.admin_secondary_profile", "build.non_admin", "build.error_shape",
}

func BuildAuthorityAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	return buildInfoAcceptance(catalogs, RequiredBuildAuthorityScenarios)
}

func buildInfoAcceptance(catalogs []*Catalog, ids []string) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/admin/system/build"}, ids)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				if s.V2Expectation.OperationID != "getAdminBuildInfo" || len(s.Then) != 0 || len(s.V2Expectation.Then) != 0 || len(s.Requires) != 0 {
					return nil, fmt.Errorf("%s: unsupported build metadata acceptance sequence", s.ID)
				}
			}
		}
	}
	return selected, nil
}
