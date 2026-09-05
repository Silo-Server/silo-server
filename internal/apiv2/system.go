package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	contracts "github.com/Silo-Server/silo-server/contracts/api/v2"
	"github.com/Silo-Server/silo-server/internal/buildinfo"
)

// The system domain: discovery before login or profile selection.

// SystemInfo is the discovery document. It is a small fixed document: no
// per-request, per-account or deployment-topology detail. `server_version`
// and `contract_digest` are diagnostic only — for support, cache identity and
// last-resort compatibility messages — and are never a substitute for the
// capability documents linked from `links.capabilities`.
type SystemInfo struct {
	ServerVersion  string          `json:"server_version" doc:"Server build identity (short revision, +dirty when built from a modified tree, or \"unavailable\"). Diagnostic only: never feature-detect on it." example:"1.0.0-dev"`
	APIMajor       int             `json:"api_major" doc:"The native API major this server serves at this path" example:"2"`
	ContractDigest string          `json:"contract_digest" doc:"SHA-256 (hex) of the exact committed OpenAPI artifact served at links.openapi. Diagnostic and cache-identity only: never feature-detect on it." example:"3b8f0c2d9e1a4f6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"`
	Links          SystemInfoLinks `json:"links" doc:"Stable links to the contract and to capability documents"`
}

// SystemInfoLinks are the discovery links.
type SystemInfoLinks struct {
	OpenAPI      string `json:"openapi" doc:"Path of the committed OpenAPI artifact" example:"/api/v2/openapi.json"`
	Capabilities string `json:"capabilities" doc:"Path prefix of the per-domain capability documents" example:"/api/v2/capabilities"`
}

// SystemInfoOutput is the getSystemInfo response.
type SystemInfoOutput struct {
	// The document is public and identical for every caller of one build;
	// it may be cached briefly and revalidated. Capability documents are the
	// separately ratified `private, no-cache` exception; this document is
	// neither private nor per-caller, so it takes the same no-cache
	// revalidation posture without the private scope.
	CacheControl string `header:"Cache-Control"`
	Body         SystemInfo
}

// contractDigest is computed once from the embedded bytes, never from live
// wiring, so the value is build-reproducible.
var contractDigest = func() string {
	sum := sha256.Sum256(contracts.OpenAPI)
	return hex.EncodeToString(sum[:])
}()

// ContractDigest is the SHA-256 (hex) of the embedded OpenAPI artifact.
func ContractDigest() string { return contractDigest }

// SetupStatus reports whether the server still needs its first administrator.
type SetupStatus struct {
	NeedsSetup bool `json:"needs_setup" doc:"True until the first administrator account exists" example:"false"`
}

// SetupStatusOutput is the getSetupStatus response.
type SetupStatusOutput struct {
	Body SetupStatus
}

func registerSystem(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/system/info", "getSystemInfo", "system",
			"Get server and contract identity for discovery before login."),
		Class: ClassPublic,
	}, getSystemInfo)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/system/setup", "getSetupStatus", "system",
			"Report whether the server still needs its first administrator."),
		Class:         ClassPublic,
		ServiceBacked: true,
	}, reg.getSetupStatus)
}

// getSetupStatus answers from the same account count v1 GET /auth/setup uses.
func (reg *Registry) getSetupStatus(ctx context.Context, _ *struct{}) (*SetupStatusOutput, error) {
	if reg.deps.Accounts == nil {
		return nil, unavailable("account")
	}
	needsSetup, err := reg.deps.Accounts.NeedsSetup(ctx)
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	return &SetupStatusOutput{Body: SetupStatus{NeedsSetup: needsSetup}}, nil
}

func getSystemInfo(_ context.Context, _ *struct{}) (*SystemInfoOutput, error) {
	return &SystemInfoOutput{
		CacheControl: "no-cache",
		Body: SystemInfo{
			ServerVersion:  buildinfo.Current().Display,
			APIMajor:       APIMajor,
			ContractDigest: contractDigest,
			Links: SystemInfoLinks{
				OpenAPI:      Prefix + "/openapi.json",
				Capabilities: Prefix + "/capabilities",
			},
		},
	}, nil
}
