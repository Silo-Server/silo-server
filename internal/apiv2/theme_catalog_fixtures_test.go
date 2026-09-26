package apiv2

func themeCatalogFixtureCases() []fixtureCase {
	const schemas = "#/components/schemas/"
	return []fixtureCase{
		{name: "theme_catalog_document", operationID: "getThemeCatalog", method: "GET", path: Prefix + "/theme/catalog", headers: bearer(memberToken), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: schemas + "ThemeCatalogResponse", scenario: "An account-authenticated read returns a typed portable catalog with explicit freshness and non-null tags."},
		{name: "theme_download_document", operationID: "downloadThemeFile", method: "GET", path: Prefix + "/theme/download?url=https%3A%2F%2Fthemes.example%2Ftheme.json", headers: bearer(memberToken), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: schemas + "ThemeDownloadResponse", scenario: "A downloaded portable theme preserves baseTheme, vars and customCss inside the native document envelope."},
		{name: "theme_catalog_refreshed", operationID: "refreshThemeCatalog", method: "POST", path: Prefix + "/theme/catalog/refresh", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: schemas + "ThemeCatalogResponse", scenario: "Acting-admin refresh completes synchronously and returns the portable catalog; it is not a job acknowledgment."},
	}
}
