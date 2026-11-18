package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

type stubSectionEpisodeFetcher struct {
	calls int
	meta  map[string]sections.SectionItemMeta
}

func (s *stubSectionEpisodeFetcher) FetchEpisodesByContentIDs(_ context.Context, _ []string, _ catalog.AccessFilter) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error) {
	s.calls++
	return nil, s.meta, nil
}

type stubSectionPlayableTargetResolver struct {
	query   catalog.PlayableTargetQuery
	targets map[string]string
}

func (s *stubSectionPlayableTargetResolver) ResolvePlayableTargets(_ context.Context, query catalog.PlayableTargetQuery) (map[string]string, error) {
	s.query = query
	return s.targets, nil
}

type countingSectionImageResolver struct {
	batchCalls  int
	singleCalls int
	variant     string
	paths       []string
}

func (r *countingSectionImageResolver) ResolveImageURL(_ context.Context, path string, variant string) string {
	r.singleCalls++
	return "single:" + variant + ":" + path
}

func (r *countingSectionImageResolver) ResolveImageURLs(_ context.Context, paths []string, variant string) map[string]string {
	resolved := r.ResolveImageURLsWithExpiry(context.Background(), paths, variant)
	urls := make(map[string]string, len(resolved))
	for path, value := range resolved {
		urls[path] = value.URL
	}
	return urls
}

func (r *countingSectionImageResolver) ResolveImageURLWithExpiry(_ context.Context, path string, variant string) catalog.ResolvedImageURL {
	r.singleCalls++
	return catalog.ResolvedImageURL{URL: "single:" + variant + ":" + path}
}

func (r *countingSectionImageResolver) ResolveImageURLsWithExpiry(_ context.Context, paths []string, variant string) map[string]catalog.ResolvedImageURL {
	r.batchCalls++
	r.variant = variant
	r.paths = append([]string{}, paths...)
	resolved := make(map[string]catalog.ResolvedImageURL, len(paths))
	for _, path := range paths {
		resolved[path] = catalog.ResolvedImageURL{URL: "resolved:" + path}
	}
	return resolved
}
