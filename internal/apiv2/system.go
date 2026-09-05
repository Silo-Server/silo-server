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
	ServerVersion  string          `json:"server_version" doc:"Server build identity (short revision, +dirty when built from a modified tree, or \"unavailable\"). Diagnostic only: never feature-detect on it."`
	APIMajor       int             `json:"api_major" doc:"The native API major this server serves at this path"`
	ContractDigest string          `json:"contract_digest" doc:"SHA-256 (hex) of the exact committed OpenAPI artifact served at links.openapi. Diagnostic and cache-identity only: never feature-detect on it."`
	Links          SystemInfoLinks `json:"links" doc:"Stable links to the contract and to capability documents"`
}

// SystemInfoLinks are the discovery links.
type SystemInfoLinks struct {
	OpenAPI      string `json:"openapi" doc:"Path of the committed OpenAPI artifact"`
	Capabilities string `json:"capabilities" doc:"Path prefix of the per-domain capability documents"`
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

func registerSystem(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/system/info", "getSystemInfo", "system",
			"Get server and contract identity for discovery before login."),
		Class: ClassPublic,
	}, getSystemInfo)
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
