package apiv2

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	contracts "github.com/Silo-Server/silo-server/contracts/api/v2"
)

// OpenAPIDocumentOutput is the getOpenAPIDocument response: the committed
// artifact, byte for byte. The body is written by hand (Body is a writer
// callback) so nothing re-encodes it; the digest a client computes over the
// bytes it received equals the one /api/v2/system/info reports.
type OpenAPIDocumentOutput struct {
	ContentType string `header:"Content-Type"`
	// The document is public, identical for every caller of one build, and
	// addressed by its digest (ETag). A short public freshness window lets a
	// proxy or a client library absorb repeated fetches without a new deploy
	// staying invisible for more than five minutes; the ETag then makes a
	// revalidation a 304-sized exchange once conditional requests are
	// ratified (docs/architecture/api-contract.md, "Response caching").
	CacheControl string `header:"Cache-Control"`
	ETag         string `header:"ETag"`
	Body         func(huma.Context)
}

// registerOpenAPIDocument registers the artifact route as an ordinary v2
// operation (tag system, getOpenAPIDocument) rather than as a manual-registry
// entry: it is a plain GET with a fixed JSON body, so it belongs in the
// artifact it serves, and every gate, negotiation and header rule of the
// listener applies to it unchanged.
func registerOpenAPIDocument(reg *Registry) {
	op := Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/openapi.json", "getOpenAPIDocument", "system",
			"Get the committed OpenAPI document this server was built from."),
		Class: ClassPublic,
	}
	op.Responses = map[string]*huma.Response{
		"200": {
			Description: "The OpenAPI 3.1 document, exactly the committed contracts/api/v2/openapi.json bytes.",
			Content: map[string]*huma.MediaType{
				mediaTypeJSON: {Schema: &huma.Schema{
					Type:                 "object",
					Description:          "An OpenAPI 3.1 document. Its members are fixed by the OpenAPI specification, not by this contract.",
					AdditionalProperties: true,
					Extensions:           map[string]any{extExtensionBag: "openapi-document"},
				}},
			},
		},
	}
	Register(reg, op, getOpenAPIDocument)
}

func getOpenAPIDocument(_ context.Context, _ *struct{}) (*OpenAPIDocumentOutput, error) {
	return &OpenAPIDocumentOutput{
		ContentType:  mediaTypeJSON,
		CacheControl: "public, max-age=300",
		ETag:         `"` + contractDigest + `"`,
		Body: func(ctx huma.Context) {
			ctx.SetStatus(http.StatusOK)
			_, _ = ctx.BodyWriter().Write(contracts.OpenAPI)
		},
	}, nil
}
