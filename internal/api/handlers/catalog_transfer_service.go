package handlers

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/models"
)

type CatalogImportSourceSelection struct {
	LocalPath   string
	ExportJobID string
	ArtifactKey string
	RemoteURL   string
}

func validateCatalogImportSelection(source CatalogImportSourceSelection) error {
	count := 0
	for _, value := range []string{source.LocalPath, source.ExportJobID, source.ArtifactKey, source.RemoteURL} {
		if value != "" {
			count++
		}
	}
	if count != 1 {
		return apiError(http.StatusBadRequest, "bad_request", "Select exactly one catalog import source")
	}
	return nil
}

func (h *CatalogSeedHandler) ExportCatalog(ctx context.Context, opts catalogseed.ExportOptions) ([]byte, error) {
	return h.service.Export(ctx, opts)
}

func (h *CatalogSeedHandler) CreateCatalogExportJob(ctx context.Context, userID int, opts catalogseed.ExportOptions) (*models.AdminJob, error) {
	if h.jobRepo == nil || h.store == nil {
		return nil, apiError(http.StatusServiceUnavailable, "service_unavailable", "Catalog export storage is unavailable")
	}
	job, err := h.jobRepo.Create(ctx, adminjob.CreateJobInput{JobType: adminjob.JobTypeCatalogExport, CreatedByUserID: userID, RequestPayload: opts, Message: "Queued catalog export"})
	if err == nil && h.RealtimeHub != nil {
		publishEventJob(ctx, h.RealtimeHub.EventsHub(), "job.created", job)
	}
	return job, err
}

func (h *CatalogSeedHandler) ImportCatalog(ctx context.Context, source CatalogImportSourceSelection, opts catalogseed.ImportOptions) (*catalogseed.ImportResult, error) {
	if err := validateCatalogImportSelection(source); err != nil {
		return nil, err
	}
	var data []byte
	var err error
	switch {
	case source.LocalPath != "":
		data, err = readLocalImportFile(source.LocalPath)
	case source.RemoteURL != "":
		data, err = readImportDataFromRemoteURL(ctx, source.RemoteURL)
	case source.ArtifactKey != "":
		data, err = h.readImportDataFromArtifactKey(ctx, source.ArtifactKey)
	default:
		data, err = h.readImportDataFromExportJob(ctx, source.ExportJobID)
	}
	if err != nil {
		return nil, catalogImportSourceProblem(err)
	}
	return h.service.Import(ctx, data, opts)
}

func (h *CatalogSeedHandler) CreateCatalogImportJob(ctx context.Context, userID int, source CatalogImportSourceSelection, opts catalogseed.ImportOptions) (*models.AdminJob, error) {
	if h.jobRepo == nil {
		return nil, apiError(http.StatusServiceUnavailable, "service_unavailable", "Catalog jobs are unavailable")
	}
	if err := validateCatalogImportSelection(source); err != nil {
		return nil, err
	}
	req := adminjob.CatalogImportRequest{Options: opts}
	switch {
	case source.LocalPath != "":
		path, err := filepath.Abs(filepath.Clean(source.LocalPath))
		if err != nil || !strings.HasSuffix(strings.ToLower(path), ".json.gz") {
			return nil, catalogImportSourceProblem(errCatalogSeedImportInvalidLocalPath)
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, catalogImportSourceProblem(errCatalogSeedImportInvalidLocalPath)
		}
		req.LocalPath, req.SourceLabel = path, filepath.Base(path)
	case source.RemoteURL != "":
		if _, err := readImportDataFromRemoteURL(ctx, source.RemoteURL); err != nil {
			return nil, catalogImportSourceProblem(err)
		}
		req.RemoteURL, req.SourceLabel = source.RemoteURL, source.RemoteURL
	case source.ArtifactKey != "":
		if h.store == nil {
			return nil, catalogImportSourceProblem(errCatalogSeedImportSourceUnavailable)
		}
		req.SourceBucket, req.SourceKey, req.SourceLabel = h.store.Bucket(), source.ArtifactKey, filepath.Base(source.ArtifactKey)
	default:
		bucket, key, err := h.resolveExportJobArtifactRef(ctx, source.ExportJobID)
		if err != nil {
			return nil, catalogImportSourceProblem(err)
		}
		req.SourceBucket, req.SourceKey, req.SourceLabel = bucket, key, "Export job "+source.ExportJobID
	}
	job, err := h.jobRepo.Create(ctx, adminjob.CreateJobInput{JobType: adminjob.JobTypeCatalogImport, CreatedByUserID: userID, RequestPayload: req, Message: "Queued catalog import"})
	if err == nil && h.RealtimeHub != nil {
		publishEventJob(ctx, h.RealtimeHub.EventsHub(), "job.created", job)
	}
	return job, err
}

func catalogImportSourceProblem(err error) error {
	if errors.Is(err, errCatalogSeedImportSourceUnavailable) {
		return apiError(http.StatusServiceUnavailable, "service_unavailable", "Catalog import storage is unavailable")
	}
	return apiError(http.StatusBadRequest, "bad_request", "Cannot load the selected catalog import source")
}

// PublishCatalogExportJob saves a seven-day signed URL, without changing the
// storage ACL. Returning an existing saved URL does not renew its expiry.
func (h *CatalogSeedHandler) PublishCatalogExportJob(ctx context.Context, id string) (*models.AdminJob, error) {
	if h.jobRepo == nil || h.store == nil {
		return nil, apiError(http.StatusServiceUnavailable, "service_unavailable", "Catalog export storage is unavailable")
	}
	job, err := h.jobRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if job.JobType != adminjob.JobTypeCatalogExport || job.Status != adminjob.StatusCompleted || job.ArtifactBucket == "" || job.ArtifactKey == "" {
		return nil, apiError(http.StatusConflict, "conflict", "A completed catalog export artifact is required")
	}
	if job.PublicURL != "" {
		return job, nil
	}
	url, err := h.store.PresignGetURL(ctx, job.ArtifactBucket, job.ArtifactKey, catalogSeedPublishExpiry)
	if errors.Is(err, blobstore.ErrNoPresign) {
		// A seven-day link is a URL handed to someone outside this server, which
		// only storage-side presigning can provide. The short-lived signed
		// download route is not a substitute: it is a capability for the
		// administrator who is already here.
		return nil, apiError(http.StatusConflict, "presign_unsupported",
			"Public links require object storage that can presign URLs. This server stores exports locally; download the artifact instead.")
	}
	if err != nil {
		return nil, err
	}
	published := time.Now().UTC()
	if err := h.jobRepo.MarkPublic(ctx, id, url, published); err != nil {
		return nil, err
	}
	job.PublicURL, job.PublishedAt = url, &published
	return job, nil
}
