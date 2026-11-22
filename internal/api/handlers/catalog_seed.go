package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

type CatalogSeedArtifactStore interface {
	AdminJobArtifactStore
	GetObjectStream(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	ListObjectInfos(ctx context.Context, bucket, prefix string) ([]s3client.ObjectInfo, error)
}

type CatalogSeedHandler struct {
	service        *catalogseed.Service
	jobRepo        *adminjob.Repository
	store          CatalogSeedArtifactStore
	localImportDir string
	RealtimeHub    *notifications.Hub
}

func NewCatalogSeedHandler(service *catalogseed.Service, jobRepo *adminjob.Repository, store CatalogSeedArtifactStore) *CatalogSeedHandler {
	return &CatalogSeedHandler{service: service, jobRepo: jobRepo, store: store}
}

const catalogSeedPublishExpiry = 7 * 24 * time.Hour

const defaultLocalImportDir = "/catalog-seeds"

var (
	errCatalogSeedImportSourceUnavailable = errors.New("catalog imports from stored artifacts require configured storage")
	errCatalogSeedImportInvalidLocalPath  = errors.New("Local path must point to an existing .json.gz file")
)

func (h *CatalogSeedHandler) SetLocalImportDir(dir string) {
	h.localImportDir = dir
}

func (h *CatalogSeedHandler) localImportDirectory() string {
	if h.localImportDir != "" {
		return h.localImportDir
	}
	return defaultLocalImportDir
}

func openLocalImportFile(localPath string) (io.ReadCloser, error) {
	cleaned := filepath.Clean(localPath)
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return nil, errCatalogSeedImportInvalidLocalPath
	}
	if !strings.HasSuffix(strings.ToLower(abs), ".json.gz") {
		return nil, errCatalogSeedImportInvalidLocalPath
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errCatalogSeedImportInvalidLocalPath
	}
	file, err := os.Open(abs) //nolint:gosec // G304: the .json.gz file an admin chose to import, checked above
	if err != nil {
		return nil, fmt.Errorf("reading local import file: %w", err)
	}
	return file, nil
}

func (h *CatalogSeedHandler) openImportDataFromExportJob(ctx context.Context, jobID string) (io.ReadCloser, error) {
	bucket, key, err := h.resolveExportJobArtifactRef(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return h.store.GetObjectStream(ctx, bucket, key)
}

func (h *CatalogSeedHandler) openImportDataFromArtifactKey(ctx context.Context, artifactKey string) (io.ReadCloser, error) {
	if h.store == nil {
		return nil, errCatalogSeedImportSourceUnavailable
	}
	return h.store.GetObjectStream(ctx, h.store.Bucket(), artifactKey)
}

func (h *CatalogSeedHandler) resolveExportJobArtifactRef(ctx context.Context, jobID string) (string, string, error) {
	if h.jobRepo == nil || h.store == nil {
		return "", "", errCatalogSeedImportSourceUnavailable
	}

	job, err := h.jobRepo.GetByID(ctx, jobID)
	if err != nil {
		return "", "", err
	}
	if job.JobType != adminjob.JobTypeCatalogExport || job.Status != adminjob.StatusCompleted || job.ArtifactBucket == "" || job.ArtifactKey == "" {
		return "", "", fmt.Errorf("catalog export job %s is not ready for import", jobID)
	}
	return job.ArtifactBucket, job.ArtifactKey, nil
}
