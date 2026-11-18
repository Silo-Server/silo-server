package metadata

import (
	"context"
	"errors"
	"slices"
	"sync"
)

// languageTextProvider answers each request with the text it has in the
// requested language, recording the languages it was asked for.
type languageTextProvider struct {
	slug     string
	types    []string
	items    map[string]*MetadataResult
	seasons  map[string][]SeasonResult
	episodes map[string][]EpisodeResult
	failIn   string

	mu        sync.Mutex
	requested []string
}

func (p *languageTextProvider) Slug() string       { return p.slug }
func (p *languageTextProvider) Name() string       { return p.slug }
func (p *languageTextProvider) ForTypes() []string { return p.types }

func (p *languageTextProvider) record(language string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requested = append(p.requested, language)
	if language == p.failIn {
		return errors.New("provider unavailable")
	}
	return nil
}

func (p *languageTextProvider) askedFor(language string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Contains(p.requested, language)
}

func (p *languageTextProvider) GetMetadata(_ context.Context, req MetadataRequest) (*MetadataResult, error) {
	if err := p.record(req.Language); err != nil {
		return nil, err
	}
	item := p.items[req.Language]
	if item == nil {
		return &MetadataResult{}, nil
	}
	cp := *item
	cp.HasMetadata = true
	return &cp, nil
}

func (p *languageTextProvider) GetSeasons(_ context.Context, req SeasonsRequest) ([]SeasonResult, error) {
	if err := p.record(req.Language); err != nil {
		return nil, err
	}
	return slices.Clone(p.seasons[req.Language]), nil
}

func (p *languageTextProvider) GetEpisodes(_ context.Context, req EpisodesRequest) ([]EpisodeResult, error) {
	if err := p.record(req.Language); err != nil {
		return nil, err
	}
	return slices.Clone(p.episodes[req.Language]), nil
}
