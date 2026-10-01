"use strict";

// Keep both the spec and interactive requests on this installation, including
// deployments where a reverse proxy strips a path prefix before forwarding.
const basePath = window.location.pathname.replace(/\/api\/v2\/docs$/, "");
window.ui = SwaggerUIBundle({
  url: new URL("openapi.json", window.location.href).href,
  dom_id: "#swagger-ui",
  deepLinking: true,
  filter: true,
  docExpansion: "none",
  defaultModelsExpandDepth: -1,
  displayRequestDuration: true,
  persistAuthorization: false,
  queryConfigEnabled: false,
  validatorUrl: null,
  requestInterceptor(request) {
    const target = new URL(request.url, window.location.href);
    if (target.origin === window.location.origin && target.pathname.startsWith("/api/v2/")) {
      target.pathname = basePath + target.pathname;
      request.url = target.href;
    }
    return request;
  },
});
