package adminjob

import "github.com/Silo-Server/silo-server/internal/catalogseed"

type CatalogImportRequest struct {
	SourceBucket  string                    `json:"source_bucket"`
	SourceKey     string                    `json:"source_key"`
	SourceLabel   string                    `json:"source_label,omitempty"`
	CleanupSource bool                      `json:"cleanup_source,omitempty"`
	LocalPath     string                    `json:"local_path,omitempty"`
	RemoteURL     string                    `json:"remote_url,omitempty"`
	Options       catalogseed.ImportOptions `json:"options"`

	// SourceExportJobID names the export job an import reads from, so a
	// client can label the source in its own language. SourceLabel keeps the
	// English "Export job <id>" for clients that predate it.
	SourceExportJobID string `json:"source_export_job_id,omitempty"`
}
