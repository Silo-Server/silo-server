package config

import (
	"context"
	"strings"
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
	setting *CachedSetting[bool]
}

// NewUnratedContentPolicy binds the policy to a server settings reader. A nil
// reader keeps the default.
func NewUnratedContentPolicy(settings SettingReader) *UnratedContentPolicy {
	return &UnratedContentPolicy{setting: NewCachedSetting(settings, AccessUnratedContentSettingKey, unratedContentCacheTTL, func(value string) bool {
		return strings.EqualFold(strings.TrimSpace(value), AccessUnratedContentAllow)
	})}
}

// AllowUnratedContent reports whether a title with no rating stays visible to
// a viewer with a content-rating ceiling. Anything other than an explicit
// "allow" — including an unset row or a value written before this setting
// existed — keeps the default of hiding it.
//
// A read failure answers with the value last read successfully (see
// CachedSetting). Flipping an administrator's "allow" back to "hide" because
// one lookup timed out is not a safety win: it makes titles blink out of a
// library, and because this value feeds the /api/v2 viewer scope digest it
// also invalidates every in-flight cursor and bounces paging clients to page 1.
func (p *UnratedContentPolicy) AllowUnratedContent(ctx context.Context) bool {
	if p == nil {
		return false
	}
	return p.setting.Get(ctx)
}
