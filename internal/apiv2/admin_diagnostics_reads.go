package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

type AdminDiagnosticReadsService interface {
	ListAdminDiagnosticReports(context.Context, diagnostics.ListFilters) (diagnostics.ListResult, error)
	GetAdminDiagnosticReport(context.Context, string) (*diagnostics.Report, error)
}
type AdminDiagnosticSummary struct {
	ID                 ID       `json:"id"`
	ShortID            string   `json:"short_id"`
	UserID             ID       `json:"user_id"`
	ProfileID          *string  `json:"profile_id,omitempty"`
	State              string   `json:"state" enum:"receiving,ready,failed"`
	CapturedAt         Instant  `json:"captured_at"`
	ReceivedAt         Instant  `json:"received_at"`
	ReportType         string   `json:"report_type" enum:"crash,anr,native_crash,hang,abnormal_exit,manual"`
	Platform           string   `json:"platform" enum:"android,android-tv,ios,tvos"`
	AppVersion         string   `json:"app_version"`
	AppBuild           string   `json:"app_build"`
	CrashSummary       *string  `json:"crash_summary,omitempty"`
	PlaybackSessionIDs []string `json:"playback_session_ids"`
	BlobBytes          *int64   `json:"blob_bytes,omitempty"`
	UncompressedBytes  *int64   `json:"uncompressed_bytes,omitempty"`
	BlobSHA256         *string  `json:"blob_sha256,omitempty"`
}
type AdminDiagnosticDetail struct {
	AdminDiagnosticSummary
	Manifest json.RawMessage `json:"manifest" doc:"Original validated client diagnostics manifest document, retaining its own schema_version and extension fields."`
}
type AdminDiagnosticListOutput struct {
	Body Collection[AdminDiagnosticSummary]
}
type AdminDiagnosticDetailOutput struct{ Body AdminDiagnosticDetail }
type AdminDiagnosticIDInput struct {
	ID string `path:"id" minLength:"1" maxLength:"128"`
}
type AdminDiagnosticListInput struct {
	LimitParam
	Cursor     string `query:"cursor" maxLength:"8192"`
	UserID     string `query:"user_id" pattern:"^[1-9][0-9]*$"`
	Platform   string `query:"platform" maxLength:"64"`
	ReportType string `query:"report_type" maxLength:"64"`
	From       string `query:"from" format:"date-time"`
	To         string `query:"to" format:"date-time"`
	ShortID    string `query:"short_id" maxLength:"64"`
}

func adminDiagnosticSummaryOf(r diagnostics.Report) AdminDiagnosticSummary {
	sessions := slices.Clone(r.PlaybackSessionIDs)
	if sessions == nil {
		sessions = []string{}
	}
	return AdminDiagnosticSummary{ID: ID(r.ID), ShortID: r.ShortID, UserID: IDFromInt(int64(r.UserID)), ProfileID: r.ProfileID, State: string(r.State), CapturedAt: NewInstant(r.CapturedAt), ReceivedAt: NewInstant(r.ReceivedAt), ReportType: r.ReportType, Platform: r.Platform, AppVersion: r.AppVersion, AppBuild: r.AppBuild, CrashSummary: r.CrashSummary, PlaybackSessionIDs: sessions, BlobBytes: r.BlobBytes, UncompressedBytes: r.UncompressedBytes, BlobSHA256: r.BlobSHA256}
}
func adminDiagnosticReadProblem(err error) error {
	switch {
	case errors.Is(err, diagnostics.ErrNotFound):
		return NewProblem(TypeNotFound, "Diagnostic report not found")
	case errors.Is(err, diagnostics.ErrInvalidCursor):
		return NewProblem(TypeInvalidCursor, "Invalid diagnostic report cursor")
	case errors.Is(err, diagnostics.ErrInvalidShortID):
		return NewProblem(TypeValidationFailed, "Invalid short_id")
	case errors.Is(err, diagnostics.ErrReportStoreUnavailable):
		return unavailable("diagnostic reports")
	default:
		return NewProblem(TypeInternalError, "Diagnostic report read failed")
	}
}
func adminDiagnosticFilters(in *AdminDiagnosticListInput) (diagnostics.ListFilters, error) {
	f := diagnostics.ListFilters{Platform: strings.TrimSpace(in.Platform), ReportType: strings.TrimSpace(in.ReportType), Limit: in.Limit}
	if in.UserID != "" {
		id, err := intOfID(ID(in.UserID))
		if err != nil || id <= 0 {
			return f, NewProblem(TypeValidationFailed, "Invalid user_id")
		}
		f.UserID = &id
	}
	for _, v := range []struct {
		raw    string
		target **time.Time
	}{{in.From, &f.From}, {in.To, &f.To}} {
		if v.raw != "" {
			t, err := time.Parse(time.RFC3339Nano, v.raw)
			if err != nil || t.IsZero() {
				return f, NewProblem(TypeValidationFailed, "Expected a nonzero RFC 3339 instant")
			}
			t = t.UTC()
			*v.target = &t
		}
	}
	if f.From != nil && f.To != nil && f.From.After(*f.To) {
		return f, NewProblem(TypeValidationFailed, "from must not be after to")
	}
	if strings.TrimSpace(in.ShortID) != "" {
		id, err := diagnostics.ParseShortID(in.ShortID)
		if err != nil {
			return f, adminDiagnosticReadProblem(err)
		}
		f.ShortID = id
	}
	return f, nil
}
func registerAdminDiagnosticReads(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := func(path, id, summary string) Operation {
		return Operation{Operation: humaOp("GET", Prefix+"/admin/diagnostics/reports"+path, id, "admin-observability", summary), Class: ClassActingAdmin, ServiceBacked: true}
	}
	Register(reg, op("", "listAdminDiagnosticReports", "Page diagnostic report summaries without loading their manifests."), func(ctx context.Context, in *AdminDiagnosticListInput) (*AdminDiagnosticListOutput, error) {
		if reg.deps.AdminDiagnosticReads == nil {
			return nil, unavailable("diagnostic reports")
		}
		filters, err := adminDiagnosticFilters(in)
		if err != nil {
			return nil, err
		}
		filter, _ := json.Marshal(filters)
		scope := CursorScope{OperationID: "listAdminDiagnosticReports", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: string(filter), Sort: "-received_at", Tiebreaker: "-id"}
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &filters.Cursor); p != nil {
				return nil, p
			}
			if filters.Cursor == "" {
				return nil, NewProblem(TypeInvalidCursor, "Invalid diagnostic report cursor")
			}
		}
		result, err := reg.deps.AdminDiagnosticReads.ListAdminDiagnosticReports(ctx, filters)
		if err != nil {
			return nil, adminDiagnosticReadProblem(err)
		}
		items := make([]AdminDiagnosticSummary, 0, len(result.Reports))
		for _, r := range result.Reports {
			items = append(items, adminDiagnosticSummaryOf(r))
		}
		next := ""
		if result.NextCursor != "" {
			next, err = cursors.Encode(scope, result.NextCursor)
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &AdminDiagnosticListOutput{Body: Paginated(items, next)}, nil
	})
	Register(reg, op("/{id}", "getAdminDiagnosticReport", "Read diagnostic report metadata and its original manifest."), func(ctx context.Context, in *AdminDiagnosticIDInput) (*AdminDiagnosticDetailOutput, error) {
		if reg.deps.AdminDiagnosticReads == nil {
			return nil, unavailable("diagnostic reports")
		}
		report, err := reg.deps.AdminDiagnosticReads.GetAdminDiagnosticReport(ctx, in.ID)
		if err != nil {
			return nil, adminDiagnosticReadProblem(err)
		}
		if report == nil {
			return nil, adminDiagnosticReadProblem(diagnostics.ErrNotFound)
		}
		return &AdminDiagnosticDetailOutput{Body: AdminDiagnosticDetail{AdminDiagnosticSummary: adminDiagnosticSummaryOf(*report), Manifest: report.Manifest}}, nil
	})
}
