package apiv2

import (
	"context"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type AdminAutoscanConnectionTestService interface {
	TestAdminAutoscanConnection(context.Context, handlers.AdminAutoscanConnectionTestInput) (autoscan.ConnectionTestResult, error)
}
type AdminAutoscanConnectionTestBody struct {
	ConnectionID         *string `json:"connection_id,omitempty" maxLength:"256"`
	BaseURL              string  `json:"base_url,omitempty" maxLength:"8192"`
	APIKeyRef            string  `json:"api_key_ref,omitempty" maxLength:"8192" writeOnly:"true"`
	RequestIntegrationID *string `json:"request_integration_id,omitempty" maxLength:"256"`
}
type AdminAutoscanConnectionTestInput struct {
	Body AdminAutoscanConnectionTestBody
}
type AdminAutoscanConnectionTestResult struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}
type AdminAutoscanConnectionTestOutput struct {
	Body AdminAutoscanConnectionTestResult
}

func registerAdminAutoscanConnectionTest(reg *Registry) {
	op := Operation{Operation: humaOp("POST", Prefix+"/admin/autoscan/connections/test", "testAdminAutoscanConnection", "admin-autoscan", "Test one stored or draft connection synchronously without saving it. Failed checks remain ok:false; no automatic retry, replay or durable job."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanConnectionTestInput) (*AdminAutoscanConnectionTestOutput, error) {
		if reg.deps.AdminAutoscanConnectionTests == nil {
			return nil, unavailable("autoscan connection test")
		}
		b := in.Body
		hasID := b.ConnectionID != nil && strings.TrimSpace(*b.ConnectionID) != ""
		hasLink := b.RequestIntegrationID != nil && strings.TrimSpace(*b.RequestIntegrationID) != ""
		if !hasID && strings.TrimSpace(b.BaseURL) == "" && !hasLink {
			return nil, NewProblem(TypeValidationFailed, "A stored connection, base URL or request integration is required.")
		}
		intent := handlers.AdminAutoscanConnectionTestInput{ConnectionID: b.ConnectionID, BaseURL: b.BaseURL, APIKeyRef: b.APIKeyRef, RequestIntegrationID: b.RequestIntegrationID}
		result, err := reg.deps.AdminAutoscanConnectionTests.TestAdminAutoscanConnection(ctx, intent)
		switch {
		case errors.Is(err, handlers.ErrAdminAutoscanConnectionTestUnavailable):
			return nil, unavailable("autoscan connection test")
		case errors.Is(err, autoscan.ErrNotFound):
			return nil, NewProblem(TypeNotFound, "Connection not found.")
		case err != nil:
			return nil, NewProblem(TypeInternalError, "Connection test could not be performed.")
		}
		body := AdminAutoscanConnectionTestResult{OK: result.OK}
		if result.OK {
			body.Version = result.Version
		} else {
			body.Error = "Connection could not be verified. Check its configuration and access."
		}
		return &AdminAutoscanConnectionTestOutput{Body: body}, nil
	})
}
