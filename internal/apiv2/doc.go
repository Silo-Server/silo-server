// Package apiv2 is the native /api/v2 listener: one Huma API mounted under the
// API listener's middleware stack, with the shared wire rules the contract
// ratifies (docs/architecture/api-contract.md, "Contract foundation").
//
// Shape, and why:
//
//   - NewHandler returns a sealed http.Handler built from the unexported
//     newChiRouter, exactly like the other listeners, so the route inventory
//     generator can audit this package (internal/routeinventory). The Huma API
//     object is constructed from that unexported router and nowhere else. The
//     inventory records the API listener's `/api/v2/*` registration as a
//     delegation to this listener; the operations behind it are described by
//     contracts/api/v2/openapi.json, not by inventory rows.
//   - Every stable operation registers when the router is built. A handler that
//     cannot work in the current wiring reports capability state or a typed
//     problem; it never changes the route table.
//   - Registration is grouped by domain: one register function per domain
//     file (system.go is the only domain in the foundation stage).
//   - An operation declares its class (public, authenticated, profile_scoped,
//     acting_admin, permission_gated) and the existing internal/api/middleware
//     gates are composed onto it per class, so auth, session, profile,
//     acting-admin, permission, demo-mode and rate-limit enforcement reach Huma
//     handlers with the same strength as the v1 router. Handlers read
//     claims, profile and viewer scope from the request context.
//   - One error adapter renders every failure — Huma-generated and
//     application-generated — as an RFC 9457 Problem Details document.
package apiv2
