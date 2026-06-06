package autoscan

import (
	"context"
	"fmt"
)

// ArrStatusProbe probes an arr instance's /api/v3/system/status to confirm the
// base URL + api key are reachable and valid. It returns the reported version
// on success.
type ArrStatusProbe interface {
	SystemStatus(ctx context.Context, baseURL, apiKey string) (version string, err error)
}

// ConnectionTestResult is the outcome of probing a connection's arr endpoint.
type ConnectionTestResult struct {
	OK      bool
	Version string
	Err     string
}

// TestConnection resolves the given connection to concrete credentials and
// probes its arr /api/v3/system/status. A resolve failure or an unreachable /
// unauthorized / non-200 endpoint yields OK=false with a human-readable Err
// (never an HTTP error from this method itself — probe failures are part of the
// successful response payload). A nil probe dependency is a wiring error and is
// returned as a real error.
func (s *Service) TestConnection(ctx context.Context, c Connection) (ConnectionTestResult, error) {
	if s.rootFolders == nil {
		return ConnectionTestResult{}, fmt.Errorf("autoscan: connection probe not configured")
	}
	probe, ok := s.rootFolders.(ArrStatusProbe)
	if !ok {
		return ConnectionTestResult{}, fmt.Errorf("autoscan: connection probe not configured")
	}
	resolved, err := s.connres.Resolve(ctx, c)
	if err != nil {
		return ConnectionTestResult{OK: false, Err: err.Error()}, nil
	}
	version, err := probe.SystemStatus(ctx, resolved.BaseURL, resolved.APIKey)
	if err != nil {
		return ConnectionTestResult{OK: false, Err: err.Error()}, nil
	}
	return ConnectionTestResult{OK: true, Version: version}, nil
}

// TestConnectionByID loads a stored connection by id and tests it.
func (s *Service) TestConnectionByID(ctx context.Context, id string) (ConnectionTestResult, error) {
	c, err := s.store.GetConnection(ctx, id)
	if err != nil {
		return ConnectionTestResult{}, err
	}
	return s.TestConnection(ctx, c)
}
