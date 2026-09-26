package apiv2

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

type AdminTelemetryParityService interface {
	ReadStreamTelemetryParity(context.Context) handlers.StreamTelemetryParityView
}
type AdminTelemetryParityView struct {
	Available          bool     `json:"available"`
	BuiltAt            *Instant `json:"built_at,omitempty"`
	AgeMS              int64    `json:"age_ms"`
	Stale              bool     `json:"stale"`
	BuildTookMS        int64    `json:"build_took_ms"`
	Refreshes          int64    `json:"refreshes"`
	Failures           int64    `json:"failures"`
	LastError          string   `json:"last_error,omitempty"`
	Complete           bool     `json:"complete"`
	IncompleteReasons  []string `json:"incomplete_reasons"`
	MissingPublishers  []string `json:"missing_publishers"`
	ClockSkewSuspected bool     `json:"clock_skew_suspected"`
	Publishers         []string `json:"publishers"`
	SessionCount       int      `json:"session_count"`
	TransferCount      int      `json:"transfer_count"`
}
type AdminTelemetryParitySource struct {
	Source               string                        `json:"source"`
	Available            bool                          `json:"available"`
	Error                string                        `json:"error,omitempty"`
	Notes                []string                      `json:"notes"`
	Report               *streamtelemetry.ParityReport `json:"report,omitempty"`
	LegacyScanLimit      int                           `json:"legacy_scan_limit"`
	LegacyMayBeTruncated bool                          `json:"legacy_may_be_truncated"`
}
type AdminTelemetryParity struct {
	Enabled bool                         `json:"enabled"`
	Reason  string                       `json:"reason,omitempty"`
	View    AdminTelemetryParityView     `json:"view"`
	Sources []AdminTelemetryParitySource `json:"sources"`
}
type AdminTelemetryParityOutput struct{ Body AdminTelemetryParity }

func registerAdminTelemetryParity(reg *Registry) {
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/stream-telemetry/parity", "getAdminStreamTelemetryParity", "admin-observability", "Compare cached telemetry and bounded legacy session reads. Disabled, missing, incomplete or capped sources are not evidence of parity or cutover readiness."), Class: ClassActingAdmin, ServiceBacked: true}, func(ctx context.Context, _ *struct{}) (*AdminTelemetryParityOutput, error) {
		if reg.deps.AdminTelemetryParity == nil {
			return nil, unavailable("stream telemetry parity")
		}
		snapshot := reg.deps.AdminTelemetryParity.ReadStreamTelemetryParity(ctx)
		v := snapshot.View
		body := AdminTelemetryParity{Enabled: snapshot.Enabled, Reason: snapshot.Reason, Sources: []AdminTelemetryParitySource{}, View: AdminTelemetryParityView{Available: v.Available, AgeMS: v.AgeMS, Stale: v.Stale, BuildTookMS: v.BuildTookMS, Refreshes: v.Refreshes, Failures: v.Failures, Complete: v.Complete, IncompleteReasons: append([]string{}, v.IncompleteReasons...), MissingPublishers: append([]string{}, v.MissingPublishers...), ClockSkewSuspected: v.ClockSkewSuspected, Publishers: append([]string{}, v.Publishers...), SessionCount: v.SessionCount, TransferCount: v.TransferCount}}
		if v.BuiltAt != "" {
			at, err := time.Parse(time.RFC3339Nano, v.BuiltAt)
			if err != nil {
				return nil, serviceProblem(err)
			}
			body.View.BuiltAt = new(NewInstant(at))
		}
		if v.LastError != "" {
			body.View.LastError = "Telemetry view refresh failed."
		}
		for _, source := range snapshot.Sources {
			item := AdminTelemetryParitySource{Source: source.Source, Available: source.Available, Notes: append([]string{}, source.Notes...), Report: source.Report, LegacyScanLimit: handlers.StreamTelemetryParityScanLimit, LegacyMayBeTruncated: source.LegacyMayBeTruncated}
			if source.Error != "" {
				item.Error = "Legacy comparison source unavailable."
			}
			body.Sources = append(body.Sources, item)
		}
		return &AdminTelemetryParityOutput{Body: body}, nil
	})
}
