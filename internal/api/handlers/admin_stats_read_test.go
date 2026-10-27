package handlers

import (
	"context"
)

type statsReadSource struct {
	invalidated bool
	err         error
}

func (s *statsReadSource) Invalidate() { s.invalidated = true }
func (s *statsReadSource) Get(context.Context) (AdminStats, error) {
	return AdminStats{TotalUsers: 2}, s.err
}
