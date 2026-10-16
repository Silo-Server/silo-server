package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/nodeenrollment"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type AdminNodeEnrollmentService interface {
	EnrollAdminNode(context.Context, nodepool.CreateNodeInput, nodeenrollment.Input) (*nodepool.Node, error)
}

// Only the one-time token and endpoint overrides come from the administrator.
// The service supplies the customer's shared encryption key from bootstrap.
type AdminNodeEnrollmentBody struct {
	Name              string `json:"name" minLength:"1" maxLength:"1024"`
	Type              string `json:"type" enum:"transcode"`
	URL               string `json:"url" minLength:"1" maxLength:"8192"`
	Group             string `json:"group,omitempty" maxLength:"1024"`
	MaxJobs           *int   `json:"max_jobs,omitempty" nullable:"true" maximum:"2147483647" minimum:"-2147483648"`
	EnrollmentToken   string `json:"enrollment_token" minLength:"32" maxLength:"512" writeOnly:"true"`
	WorkerDatabaseURL string `json:"worker_database_url,omitempty" maxLength:"8192" writeOnly:"true"`
	WorkerRedisURL    string `json:"worker_redis_url,omitempty" maxLength:"8192" writeOnly:"true"`
}
type AdminNodeEnrollmentInput struct{ Body AdminNodeEnrollmentBody }

func registerAdminNodeEnrollment(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/nodes/enroll", "enrollAdminNode", "admin-nodes", "Bind a provider transcode node using a one-time token and server-side customer configuration, wait for authenticated runtime readiness, then persist its natural URL configuration. HTTP or HTTPS is supported. Exact retries recover an existing same-customer binding and registration; another customer or conflicting registration returns 409. A 503 may follow a saved binding: retry the same request to finish registration. The operator encoding ceiling limits max_jobs; secrets are not included in the receipt."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyUniqueConstraint, MaxBodyBytes: nodeenrollment.MaxBodyBytes}
	op.DefaultStatus = http.StatusCreated
	op.Errors = append(op.Errors, http.StatusConflict, http.StatusServiceUnavailable)
	Register(reg, op, func(ctx context.Context, in *AdminNodeEnrollmentInput) (*AdminNodeConfigurationOutput, error) {
		if reg.deps.AdminNodeEnrollment == nil {
			return nil, unavailable("node enrollment")
		}
		b := in.Body
		node, err := reg.deps.AdminNodeEnrollment.EnrollAdminNode(ctx, nodepool.CreateNodeInput{Name: b.Name, Type: b.Type, URL: b.URL, Group: b.Group, MaxJobs: b.MaxJobs}, nodeenrollment.Input{Token: b.EnrollmentToken, DatabaseURL: b.WorkerDatabaseURL, RedisURL: b.WorkerRedisURL})
		switch {
		case errors.Is(err, nodeenrollment.ErrOtherCustomer):
			return nil, NewProblem(TypeConflict, err.Error())
		case errors.Is(err, nodeenrollment.ErrStartupPending):
			return nil, NewProblem(TypeDependencyUnavailable, err.Error()).WithRetryAfter(5)
		case errors.Is(err, nodeenrollment.ErrInvalidTarget), errors.Is(err, nodeenrollment.ErrRejected), errors.Is(err, nodepool.ErrInvalidNodeInput):
			return nil, NewProblem(TypeValidationFailed, err.Error())
		}
		return adminNodeConfigurationOutput(ctx, node, err)
	})
}
