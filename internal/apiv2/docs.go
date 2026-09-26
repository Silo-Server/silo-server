package apiv2

import (
	"bytes"
	_ "embed"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

//go:embed docsui/index.html
var docsHTML []byte

//go:embed docsui/init.js
var docsInitializer []byte

//go:embed docsui/swagger-ui-bundle.js
var docsScript []byte

//go:embed docsui/swagger-ui.css
var docsStylesheet []byte

//go:embed docsui/swagger-ui-bundle.js.LICENSE.txt
var docsScriptLicenses []byte

//go:embed docsui/LICENSE
var docsLicense []byte

//go:embed docsui/NOTICE
var docsNotice []byte

var docsLicenseNotices = bytes.Join([][]byte{docsLicense, docsNotice, docsScriptLicenses}, []byte("\n"))

const (
	docsHTMLMediaType      = "text/html"
	docsScriptMediaType    = "text/javascript"
	docsTextMediaType      = "text/plain"
	docsCacheControlHeader = "Cache-Control"
	docsCachePolicy        = "no-cache"
)

const docsContentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"

func registerAPIDocs(reg *Registry) {
	for _, asset := range []struct {
		path, id, mediaType string
		body                []byte
	}{
		{"/docs", "APIDocs", docsHTMLMediaType, docsHTML},
		{"/docs/init.js", "APIDocsInitializer", docsScriptMediaType, docsInitializer},
		{"/docs/swagger-ui-bundle.js", "APIDocsScript", docsScriptMediaType, docsScript},
		{"/docs/swagger-ui.css", "APIDocsStylesheet", "text/css", docsStylesheet},
		{"/docs/swagger-ui-bundle.js.LICENSE.txt", "APIDocsScriptLicenses", docsTextMediaType, docsLicenseNotices},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			verb := strings.ToLower(method)
			op := Operation{Operation: humaOp(method, Prefix+asset.path, verb+asset.id, "system", "Serve the bundled API documentation viewer asset."), Class: ClassPublic}
			response := &huma.Response{Description: "Bundled API documentation asset", Headers: map[string]*huma.Header{
				docsCacheControlHeader: {Schema: &huma.Schema{Type: huma.TypeString, Enum: []any{docsCachePolicy}}},
			}}
			if method == http.MethodGet {
				response.Content = map[string]*huma.MediaType{asset.mediaType: {Schema: &huma.Schema{Type: huma.TypeString}}}
			}
			op.Responses = map[string]*huma.Response{"200": response}
			RegisterRaw(reg, RawOperation{Operation: op, Protocol: "api-documentation", Reason: "The viewer serves HTML, JavaScript and CSS bytes without JSON negotiation."}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", asset.mediaType+"; charset=utf-8")
				w.Header().Set("Content-Length", strconv.Itoa(len(asset.body)))
				w.Header().Set(docsCacheControlHeader, docsCachePolicy)
				w.Header().Set("Content-Security-Policy", docsContentSecurityPolicy)
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.Header().Set("Referrer-Policy", "no-referrer")
				w.WriteHeader(http.StatusOK)
				if r.Method != http.MethodHead {
					_, _ = w.Write(asset.body)
				}
			}))
		}
	}
}
