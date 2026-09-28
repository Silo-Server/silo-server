package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

const (
	scanLibraryIDLocation = "body.library_id"
	scanInvalidIDCode     = "invalid"
)

type ScanControlService interface {
	ScanControlAvailable() bool
	StartLibraryScan(context.Context, *int, string) (handlers.ScanAdmission, error)
	CancelLibraryScans(context.Context, int) (handlers.ScanCancellation, error)
}
type ScanStartInput struct {
	Body struct {
		LibraryID *ID    `json:"library_id,omitempty" nullable:"false"`
		Path      string `json:"path,omitempty" doc:"Optional file or subtree within a configured library root."`
	}
}
type ScanStartOutput struct {
	Body struct {
		Status    string `json:"status" enum:"accepted"`
		Mode      string `json:"mode" enum:"library,subtree,file"`
		LibraryID ID     `json:"library_id"`
	}
}
type ScanCancelInput struct {
	Body struct {
		LibraryID ID `json:"library_id"`
	}
}
type ScanCancelOutput struct {
	Body struct {
		Canceled  int `json:"cancelled"` //nolint:misspell // Preserve the established wire member.
		LibraryID ID  `json:"library_id"`
	}
}
type ScanCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         Capability
}

func registerScanControls(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/scan/capabilities", "getScanCapabilities", "scan", "Discover scan-control availability."), Class: ClassActingAdmin, ServiceBacked: true}, func(context.Context, *CapabilityInput) (*ScanCapabilitiesOutput, error) {
		state := StateNotConfigured
		if reg.deps.ScanControls != nil && reg.deps.ScanControls.ScanControlAvailable() {
			state = StateAvailable
		}
		return &ScanCapabilitiesOutput{Body: Capability{State: state}}, nil
	})
	start := Operation{Operation: humaOp(http.MethodPost, Prefix+"/scan", "startLibraryScan", "scan", "Resolve and dispatch a library, subtree or file scan through the configured execution path."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	start.DefaultStatus = http.StatusAccepted
	start.Errors = []int{400, 404, 409, 500}
	Register(reg, start, func(ctx context.Context, in *ScanStartInput) (*ScanStartOutput, error) {
		if reg.deps.ScanControls == nil {
			return nil, unavailable("scanner")
		}
		var id *int
		if in.Body.LibraryID != nil {
			value, p := scanControlLibraryID(*in.Body.LibraryID)
			if p != nil {
				return nil, p
			}
			id = new(value)
		}
		result, err := reg.deps.ScanControls.StartLibraryScan(ctx, id, in.Body.Path)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := new(ScanStartOutput)
		out.Body.Status = result.Status
		out.Body.Mode = result.Mode
		out.Body.LibraryID = ID(strconv.Itoa(result.LibraryID))
		return out, nil
	})
	cancel := Operation{Operation: humaOp(http.MethodPost, Prefix+"/scan/cancel", "cancelLibraryScans", "scan", "Cancel the library's currently queued and local scans."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	cancel.Errors = []int{400, 500}
	Register(reg, cancel, func(ctx context.Context, in *ScanCancelInput) (*ScanCancelOutput, error) {
		if reg.deps.ScanControls == nil {
			return nil, unavailable("scanner")
		}
		id, p := scanControlLibraryID(in.Body.LibraryID)
		if p != nil {
			return nil, p
		}
		result, err := reg.deps.ScanControls.CancelLibraryScans(ctx, id)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := new(ScanCancelOutput)
		out.Body.Canceled = result.Canceled
		out.Body.LibraryID = ID(strconv.Itoa(result.LibraryID))
		return out, nil
	})
}

func scanControlLibraryID(id ID) (int, *Problem) {
	value, p := id.positive(scanLibraryIDLocation)
	if p != nil {
		return 0, NewProblem(TypeValidationFailed, "library_id must be a positive library identifier.").WithErrors(ProblemError{Location: scanLibraryIDLocation, Code: scanInvalidIDCode, Detail: "Expected a positive library identifier."})
	}
	return value, nil
}
