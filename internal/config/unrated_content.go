package config

import (
	"context"
	"strings"
	"sync"
	"time"
)

// SettingReader reads one server setting by key.
type SettingReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// unratedContentCacheTTL bounds how long a node serves a cached
// access.unrated_content value. Every viewer scope resolution reads the
// setting, so it is not worth a database round trip per request, while an
// administrator's change still reaches every node within seconds.
const unratedContentCacheTTL = 10 * time.Second

// UnratedContentPolicy resolves AccessUnratedContentSettingKey for the viewer
// scope resolvers, caching a successful read for unratedContentCacheTTL.
type UnratedContentPolicy struct {
	settings SettingReader
	now      func() time.Time

	mu      sync.Mutex
	allow   bool
	expires time.Time
}

// NewUnratedContentPolicy binds the policy to a server settings reader. A nil
// reader keeps the default.
func NewUnratedContentPolicy(settings SettingReader) *UnratedContentPolicy {
	return &UnratedContentPolicy{settings: settings, now: time.Now}
}

// AllowUnratedContent reports whether a title with no rating stays visible to
// a viewer with a content-rating ceiling. Anything other than an explicit
// "allow" — including an unset row or a value written before this setting
// existed — keeps the default of hiding it. A read failure also hides it and
// is not cached, so a parental control never loosens because a lookup failed.
//
// The lock guards only the cached value. The settings read runs without it,
// so a slow database stalls the callers that need a fresh value rather than
// queueing every scope resolution behind one query; concurrent callers that
// find the cache expired may each read once.
func (p *UnratedContentPolicy) AllowUnratedContent(ctx context.Context) bool {
	if p == nil || p.settings == nil {
		return false
	}
	p.mu.Lock()
	allow, fresh := p.allow, p.now().Before(p.expires)
	p.mu.Unlock()
	if fresh {
		return allow
	}

	value, err := p.settings.Get(ctx, AccessUnratedContentSettingKey)
	if err != nil {
		return false
	}
	allow = strings.EqualFold(strings.TrimSpace(value), AccessUnratedContentAllow)

	p.mu.Lock()
	p.allow = allow
	p.expires = p.now().Add(unratedContentCacheTTL)
	p.mu.Unlock()
	return allow
}
