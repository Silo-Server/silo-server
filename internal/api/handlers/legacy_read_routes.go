package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const legacyReadSunset = "Wed, 01 Jul 2026 00:00:00 GMT"

func writeDeprecatedReadHeaders(w http.ResponseWriter, successor string) {
	headers := w.Header()
	headers.Set("Deprecation", "true")
	headers.Set("Sunset", legacyReadSunset)
	if successor != "" {
		headers.Set("Link", fmt.Sprintf("<%s>; rel=\"successor-version\"", successor))
	}
}

func buildLegacyItemsCatalogValues(values url.Values) (url.Values, bool) {
	translated := cloneCatalogValues(values)
	if strings.TrimSpace(values.Get("person_id")) != "" {
		translated.Set("source", "person")
	} else {
		translated.Set("source", "query")
	}

	if sort := strings.TrimSpace(values.Get("sort")); sort != "" {
		if sort == "created_at" {
			sort = "added_at"
		}
		translated.Set("sort", sort)
	}

	return translated, true
}

func buildLegacyLatestCatalogValues(values url.Values) url.Values {
	translated := cloneCatalogValues(values)
	translated.Set("source", "query")
	translated.Set("sort", "added_at")
	translated.Set("order", "desc")
	return translated
}

func buildLegacySearchCatalogValues(values url.Values) (url.Values, bool) {
	query := strings.TrimSpace(values.Get("q"))
	if query == "" {
		return nil, false
	}

	types := parseSearchTypes(values["type"])
	if len(types) > 1 {
		return nil, false
	}
	if len(types) == 1 && types[0] != "movie" && types[0] != "series" && types[0] != "episode" {
		return nil, false
	}

	translated := url.Values{}
	translated.Set("source", "query")
	translated.Set("q", query)
	translated.Set("sort", "relevance")
	translated.Set("order", "desc")
	copyCatalogValue(translated, values, "limit")
	copyCatalogValue(translated, values, "offset")
	if len(types) == 1 {
		translated.Set("type", types[0])
	}
	return translated, true
}

func cloneCatalogValues(values url.Values) url.Values {
	translated := url.Values{}
	for _, key := range []string{
		"q",
		"name_prefix",
		"type",
		"genre",
		"year_min",
		"year_max",
		"content_rating",
		"library_id",
		"person_id",
		"status",
		"limit",
		"offset",
		"order",
	} {
		copyCatalogValue(translated, values, key)
	}
	return translated
}

func copyCatalogValue(dst, src url.Values, key string) {
	if value := strings.TrimSpace(src.Get(key)); value != "" {
		dst.Set(key, value)
	}
}
