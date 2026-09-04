package apiv2

// RawHandshake is a v2 route the listener serves outside Huma: a protocol
// upgrade or a raw client handshake whose exchange no request/response
// schema describes. Every such route is listed here so the route/spec
// reconciliation (TestCommittedArtifactMatchesRouter) accounts for it; it is
// deliberately not described in contracts/api/v2/openapi.json, and its
// protocol is covered by protocol tests rather than by a fake free-form
// schema (docs/architecture/api-contract.md, "Foundation").
type RawHandshake struct {
	Method string
	Path   string
	// Protocol names the exchange (for example "websocket"); it is what the
	// protocol tests are keyed by.
	Protocol string
	// Reason states why the route cannot be a Huma operation.
	Reason string
}

// rawHandshakes is the manual registry. It is empty today: every v2 route is
// a Huma operation. Adding an entry here is a contract change reviewed with
// the operation's section.
var rawHandshakes = []RawHandshake{}

// RawHandshakes lists the manual registry, plus any test-only entries the
// wiring carries.
func RawHandshakes(deps Dependencies) []RawHandshake {
	out := append([]RawHandshake(nil), rawHandshakes...)
	return append(out, deps.testRawHandshakes...)
}
