# Interactive API documentation

Open `/api/v2/docs` on your Silo server to browse the native API with Swagger UI.
The page loads the server's embedded OpenAPI document.
No separate service, frontend build or internet connection is needed to load
the viewer. The document is also available directly at `/api/v2/openapi.json`.

Use the filter to find endpoint groups, then expand an operation to inspect its
parameters and request and response schemas. Select **Try it out**, fill in the
parameters and select **Execute** to send a real request to this server.

For protected operations, select **Authorize** and paste a session token or API
key without the `Bearer` prefix. Enter `X-Profile-Id` and any required profile
verification header on operations that need them. The normal account, profile,
permission and rate-limit rules apply. Authorization lasts for the current page
and is cleared on reload. Executing a mutation changes server state.

The viewer is available before login, just like the raw OpenAPI document. Its
assets are bundled locally; it does not send the document to an external
validator. Deployments under a reverse proxy path prefix can use the same page
beneath that prefix, provided the proxy forwards the `/api/v2/` subtree.

The Go registries remain the contract source. Regenerate after changing them:

```sh
make apiv2-openapi apiv2-web-types
```

The pinned Swagger UI version, upstream license and update instructions are in
[`internal/apiv2/docsui/README.md`](../internal/apiv2/docsui/README.md).

This viewer requires no Apple, Android or Jellyfin protocol changes. It presents
the existing native contract and exposes no new media or account behavior.
