package handlers

import (
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
