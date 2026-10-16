package metadata

import "context"

// MatchWithProvidersForTest makes Process match against providers instead of
// the installed plugin chain, so tests outside the package can run the real
// scan and match path with stub providers.
func MatchWithProvidersForTest(s *MetadataService, language string, providers ...Provider) {
	s.hooks.process = func(ctx context.Context, req ProcessRequest) (*ProcessResult, error) {
		req.Language = language
		return s.ProcessWithProviders(ctx, req, providers)
	}
}
