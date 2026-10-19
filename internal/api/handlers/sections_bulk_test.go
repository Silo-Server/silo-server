package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/sections"
)

type stubBulkRepo struct {
	failOn  int // if > 0, fail when this many rows have already succeeded (1-indexed)
	created []*sections.PageSection
}

func (s *stubBulkRepo) CreateMany(ctx context.Context, rows []*sections.PageSection) error {
	// Simulate transactional semantics: if failOn fires partway through,
	// the test sees zero persisted rows.
	var local []*sections.PageSection
	for i, r := range rows {
		if s.failOn > 0 && i+1 >= s.failOn {
			return errors.New("simulated insert failure")
		}
		local = append(local, r)
	}
	s.created = append(s.created, local...)
	return nil
}

func TestBulkCreateAppliesToAllLibraries(t *testing.T) {
	repo := &stubBulkRepo{}
	h := &SectionBulkHandler{Repo: repo}
	body, _ := json.Marshal(map[string]any{
		"scope":        "library",
		"library_ids":  []int{1, 2, 3},
		"section_type": "recently_added",
		"title":        "Recently Added",
		"item_limit":   20,
		"enabled":      true,
		"config":       map[string]any{},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sections/bulk-create", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandleBulkCreate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if len(repo.created) != 3 {
		t.Fatalf("created %d sections want 3", len(repo.created))
	}
}

func TestBulkCreateRollsBackOnFailure(t *testing.T) {
	repo := &stubBulkRepo{failOn: 2} // fail before second insert commits
	h := &SectionBulkHandler{Repo: repo}
	body, _ := json.Marshal(map[string]any{
		"scope":        "library",
		"library_ids":  []int{1, 2},
		"section_type": "recently_added",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sections/bulk-create", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleBulkCreate(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if len(repo.created) != 0 {
		t.Fatalf("expected rollback; %d rows persisted", len(repo.created))
	}
}

func TestBulkCreateRejectsUnknownSectionType(t *testing.T) {
	repo := &stubBulkRepo{}
	h := &SectionBulkHandler{Repo: repo}
	body, _ := json.Marshal(map[string]any{
		"scope":        "library",
		"library_ids":  []int{1},
		"section_type": "no_such_recipe",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sections/bulk-create", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleBulkCreate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	if len(repo.created) != 0 {
		t.Fatalf("expected zero created; got %d", len(repo.created))
	}
}

func TestBulkCreateRejectsLibraryScopeWithEmptyIDs(t *testing.T) {
	repo := &stubBulkRepo{}
	h := &SectionBulkHandler{Repo: repo}
	body, _ := json.Marshal(map[string]any{
		"scope":        "library",
		"library_ids":  []int{},
		"section_type": "recently_added",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sections/bulk-create", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleBulkCreate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestBulkCreateRefusesNextUp(t *testing.T) {
	repo := &stubBulkRepo{}
	h := &SectionBulkHandler{Repo: repo}
	body, _ := json.Marshal(map[string]any{
		"scope":        "library",
		"library_ids":  []int{1},
		"section_type": "next_up",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sections/bulk-create", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleBulkCreate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	if len(repo.created) != 0 {
		t.Fatalf("expected zero created; got %d", len(repo.created))
	}
}
