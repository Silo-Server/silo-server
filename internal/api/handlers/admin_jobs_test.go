package handlers

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCanReadAdminJob_AdminCanReadAnyJob(t *testing.T) {
	claims := &auth.Claims{UserID: 1, Role: "admin"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeCatalogExport}
	if !canReadAdminJob(claims, job) {
		t.Fatal("admin should be allowed to read any job")
	}
}

func TestCanReadAdminJob_CreatorCanReadOwnItemRefreshJob(t *testing.T) {
	claims := &auth.Claims{UserID: 2, Role: "user"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeItemRefresh}
	if !canReadAdminJob(claims, job) {
		t.Fatal("creator should be allowed to read own item refresh job")
	}
}

func TestCanReadAdminJob_CreatorCannotReadOwnNonItemRefreshJob(t *testing.T) {
	claims := &auth.Claims{UserID: 2, Role: "user"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeCatalogExport}
	if canReadAdminJob(claims, job) {
		t.Fatal("non-admin should not read non-item-refresh jobs")
	}
}

func TestCanReadAdminJob_OtherUserCannotReadItemRefreshJob(t *testing.T) {
	claims := &auth.Claims{UserID: 3, Role: "user"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeItemRefresh}
	if canReadAdminJob(claims, job) {
		t.Fatal("non-admin should not read another user's item refresh job")
	}
}

func TestAdminJobToResponseForClaims_NonAdminSanitizesItemRefreshPayloads(t *testing.T) {
	claims := &auth.Claims{UserID: 2, Role: "user"}
	job := &models.AdminJob{
		JobType:         adminjob.JobTypeItemRefresh,
		CreatedByUserID: 2,
		RequestPayload: json.RawMessage(
			`{"requested_content_id":"item-1","scan_path":"/srv/media/private/movie"}`,
		),
		ResultPayload: json.RawMessage(
			`{"requested_content_id":"item-1","detail_content_id":"item-2","scan_path":"/srv/media/private/movie","scan_result":{"New":1,"RootObservations":[{"RootPath":"/srv/media/private","SampleFilePath":"/srv/media/private/movie.mkv"}]}}`,
		),
		PublicURL: "https://example.test/public",
	}

	resp := adminJobToResponseForClaims(nil, job, nil, claims)

	if string(resp.RequestPayload) != `{}` {
		t.Fatalf("RequestPayload = %s, want sanitized empty object", resp.RequestPayload)
	}
	if resp.PublicURL != "" || resp.DownloadURL != "" || resp.DownloadExpiresAt != nil {
		t.Fatalf("expected non-admin URLs to be stripped, got public=%q download=%q", resp.PublicURL, resp.DownloadURL)
	}
	if bytes.Contains(resp.ResultPayload, []byte("/srv/media")) ||
		bytes.Contains(resp.ResultPayload, []byte("scan_path")) ||
		bytes.Contains(resp.ResultPayload, []byte("RootObservations")) ||
		bytes.Contains(resp.ResultPayload, []byte("SampleFilePath")) {
		t.Fatalf("ResultPayload leaked sensitive data: %s", resp.ResultPayload)
	}
	if !bytes.Contains(resp.ResultPayload, []byte("requested_content_id")) ||
		!bytes.Contains(resp.ResultPayload, []byte("detail_content_id")) {
		t.Fatalf("ResultPayload = %s, want safe item refresh summary fields", resp.ResultPayload)
	}
}
