package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type statsReadSource struct {
	invalidated bool
	err         error
}

func (s *statsReadSource) Invalidate() { s.invalidated = true }
func (s *statsReadSource) Get(context.Context) (AdminStats, error) {
	return AdminStats{TotalUsers: 2}, s.err
}
func TestAdminStatsSharedRead(t *testing.T) {
	source := new(statsReadSource)
	h := &AdminHandler{StatsSource: source}
	stats, err := h.ReadAdminStats(context.Background(), true)
	if err != nil || stats.TotalUsers != 2 || !source.invalidated {
		t.Fatal(stats, err)
	}
	source.err = errors.New("private backend failure")
	rec := httptest.NewRecorder()
	h.HandleGetStats(rec, httptest.NewRequest("GET", "/admin/stats", nil))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "Failed to get stats") || strings.Contains(rec.Body.String(), "private") {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
