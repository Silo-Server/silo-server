package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/storagetransition"
)

type AdminStorageTransitionService interface {
	Start(context.Context, int, storagetransition.StartRequest) (*models.AdminJob, storagetransition.Preflight, error)
	SourceHealth(context.Context, bool) (storagetransition.SourceHealth, error)
}

const (
	storageRecoveryRunning      = "running"
	storageRecoveryWaitingRetry = "waiting_retry"
	storageRecoveryBlocked      = "blocked"
	storageRecoveryPending      = "pending"
	storageRecoveryUnknown      = "unknown"
)

type AdminStorageTransitionRequest struct {
	Policy string            `json:"policy" enum:"start_fresh,preserve_uploads,migrate_all"`
	Values map[string]string `json:"values"`
}

type AdminStorageTransitionInput struct {
	RawBody []byte
	Body    AdminStorageTransitionRequest
}

type AdminStorageTransitionAccepted struct {
	Job       AdminTaskJob                    `json:"job"`
	Preflight AdminStorageTransitionPreflight `json:"preflight"`
}

type AdminStorageTransitionPreflight struct {
	CurrentBackend string   `json:"current_backend"`
	TargetBackend  string   `json:"target_backend"`
	Policy         string   `json:"policy"`
	Warnings       []string `json:"warnings"`
	ProviderImages string   `json:"provider_images"`
	Uploads        string   `json:"uploads"`
	Diagnostics    string   `json:"diagnostics"`
	Subtitles      string   `json:"subtitles"`
	CatalogSeeds   string   `json:"catalog_seeds"`
}

type AdminStorageTransitionOutput struct {
	Location   string `header:"Location"`
	RetryAfter string `header:"Retry-After"`
	Body       AdminStorageTransitionAccepted
}

type AdminStorageTransitionSourceHealthOutput struct {
	Body AdminStorageTransitionSourceHealth
}

type AdminStorageTransitionCapabilities struct {
	Capability
	StartFresh        bool `json:"start_fresh"`
	PreserveUploads   bool `json:"preserve_uploads"`
	MigrateAll        bool `json:"migrate_all"`
	LocalTarget       bool `json:"local_target"`
	S3Target          bool `json:"s3_target"`
	SourceHealth      bool `json:"source_health"`
	JobCancellation   bool `json:"job_cancellation"`
	ResumableRecovery bool `json:"resumable_recovery"`
}

type AdminStorageTransitionCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminStorageTransitionCapabilities
}

type AdminStorageTransitionSourceHealthInput struct {
	Probe bool `query:"probe" default:"true"`
}

type AdminStorageTransitionSourceHealth struct {
	CurrentBackend          string `json:"current_backend"`
	Reachable               bool   `json:"reachable"`
	PublicConfigured        bool   `json:"public_configured"`
	PublicReachable         bool   `json:"public_reachable"`
	PrivateConfigured       bool   `json:"private_configured"`
	PrivateReachable        bool   `json:"private_reachable"`
	ReachabilityProbed      bool   `json:"reachability_probed"`
	Message                 string `json:"message"`
	RecoveryPending         bool   `json:"recovery_pending"`
	RecoveryState           string `json:"recovery_state,omitempty"`
	RecoveryError           string `json:"recovery_error,omitempty" doc:"Safe failure summary without storage errors or locations."`
	RecoveryFailureCategory string `json:"recovery_failure_category,omitempty" enum:"retryable,blocked,unknown"`
	RecoveryProgress        int    `json:"recovery_progress_percent,omitempty"`
	RecoveryMessage         string `json:"recovery_progress_message,omitempty" doc:"Safe recovery status without storage errors or locations."`
}

func registerAdminStorageTransition(reg *Registry) {
	capabilitiesOp := Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/admin/storage-transitions/capabilities", "getAdminStorageTransitionCapabilities", "admin-settings", "Discover managed storage transition support in this build."),
		Class:     ClassActingAdmin,
	}
	Register(reg, capabilitiesOp, func(context.Context, *CapabilityInput) (*AdminStorageTransitionCapabilitiesOutput, error) {
		return &AdminStorageTransitionCapabilitiesOutput{Body: AdminStorageTransitionCapabilities{
			Capability:        Capability{State: configuredCapabilityState(reg.deps.AdminStorageTransition != nil)},
			StartFresh:        true,
			PreserveUploads:   true,
			MigrateAll:        true,
			LocalTarget:       true,
			S3Target:          true,
			SourceHealth:      true,
			JobCancellation:   true,
			ResumableRecovery: true,
		}}, nil
	})

	healthOp := Operation{
		Operation:     humaOp(http.MethodGet, Prefix+"/admin/storage-transitions/source-health", "getAdminStorageTransitionSourceHealth", "admin-settings", "Check whether the currently configured S3 source is reachable before choosing a storage-transition policy."),
		Class:         ClassActingAdmin,
		ServiceBacked: true,
	}
	Register(reg, healthOp, func(ctx context.Context, in *AdminStorageTransitionSourceHealthInput) (*AdminStorageTransitionSourceHealthOutput, error) {
		if reg.deps.AdminStorageTransition == nil {
			return nil, unavailable("storage transitions")
		}
		health, err := reg.deps.AdminStorageTransition.SourceHealth(ctx, in.Probe)
		if err != nil {
			return nil, NewProblem(TypeDependencyUnavailable, "Storage source health is temporarily unavailable.")
		}
		recoveryState, recoveryMessage, recoveryError, recoveryCategory := safeStorageRecovery(health)
		return &AdminStorageTransitionSourceHealthOutput{Body: AdminStorageTransitionSourceHealth{
			CurrentBackend:          health.CurrentBackend,
			Reachable:               health.Reachable,
			PublicConfigured:        health.PublicConfigured,
			PublicReachable:         health.PublicReachable,
			PrivateConfigured:       health.PrivateConfigured,
			PrivateReachable:        health.PrivateReachable,
			ReachabilityProbed:      health.ReachabilityProbed,
			Message:                 health.Message,
			RecoveryPending:         health.RecoveryPending,
			RecoveryState:           recoveryState,
			RecoveryError:           recoveryError,
			RecoveryFailureCategory: recoveryCategory,
			RecoveryProgress:        min(max(health.RecoveryProgress, 0), 100),
			RecoveryMessage:         recoveryMessage,
		}}, nil
	})

	op := Operation{
		Operation:      humaOp(http.MethodPost, Prefix+"/admin/storage-transitions", "createAdminStorageTransition", "admin-settings", "Queue a verified managed storage transition. The old location is retained and the committed target takes effect after restart."),
		Class:          ClassActingAdmin,
		DemoRestricted: true,
		ServiceBacked:  true,
		RetrySafety:    RetrySafetyNonRetryable,
	}
	op.DefaultStatus = http.StatusAccepted
	op.Errors = append(op.Errors, http.StatusConflict)
	Register(reg, op, func(ctx context.Context, in *AdminStorageTransitionInput) (*AdminStorageTransitionOutput, error) {
		if reg.deps.AdminStorageTransition == nil {
			return nil, unavailable("storage transitions")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		job, preflight, err := reg.deps.AdminStorageTransition.Start(ctx, claimsFrom(ctx).UserID, storagetransition.StartRequest{Policy: in.Body.Policy, Values: in.Body.Values})
		if err != nil {
			if conflict, ok := errors.AsType[*adminjob.ActiveJobConflictError](err); ok {
				p := NewProblem(TypeConflict, "A storage transition is already queued or running.")
				if conflict.Job != nil {
					p = p.WithHeader("Location", Prefix+"/admin/jobs/"+conflict.Job.ID)
				}
				return nil, p
			}
			if _, ok := errors.AsType[*storagetransition.ValidationError](err); ok {
				return nil, NewProblem(TypeValidationFailed, err.Error())
			}
			if _, ok := errors.AsType[*storagetransition.SourceUnavailableError](err); ok {
				return nil, NewProblem(TypeValidationFailed, err.Error())
			}
			return nil, serviceProblem(err)
		}
		return &AdminStorageTransitionOutput{Location: Prefix + "/admin/jobs/" + job.ID, RetryAfter: "5", Body: AdminStorageTransitionAccepted{Job: reg.adminTaskJobOf(ctx, job, true), Preflight: AdminStorageTransitionPreflight{CurrentBackend: preflight.CurrentBackend, TargetBackend: preflight.TargetBackend, Policy: preflight.Policy, Warnings: preflight.Warnings, ProviderImages: preflight.ProviderImages, Uploads: preflight.Uploads, Diagnostics: preflight.Diagnostics, Subtitles: preflight.Subtitles, CatalogSeeds: preflight.CatalogSeeds}}}, nil
	})
}

func safeStorageRecovery(health storagetransition.SourceHealth) (state, message, summary, category string) {
	if !health.RecoveryPending {
		return "", "", "", ""
	}
	switch health.RecoveryState {
	case storageRecoveryRunning:
		state, message = storageRecoveryRunning, "Reconciling storage after restart."
	case storageRecoveryWaitingRetry:
		state, message = storageRecoveryWaitingRetry, "Storage recovery is waiting to retry."
	case storageRecoveryBlocked:
		state, message = storageRecoveryBlocked, "Storage recovery is blocked."
	default:
		state, message = storageRecoveryPending, "Storage recovery is pending."
	}
	if health.RecoveryError != "" {
		summary = "Storage recovery needs attention. Inspect administrator diagnostics."
		switch state {
		case storageRecoveryWaitingRetry:
			category = "retryable"
		case storageRecoveryBlocked:
			category = storageRecoveryBlocked
		default:
			category = storageRecoveryUnknown
		}
	}
	return state, message, summary, category
}
