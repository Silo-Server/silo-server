package config

import (
	"context"
	"sync"
	"time"
)

// CachedSetting serves one server setting, parsed, from a per-node cache so a
// hot read path does not query the settings table on every request. A
// successful read is cached for the TTL. A failed read answers with the value
// last read successfully, or the parse of "" when no read has succeeded yet,
// and is not cached, so the next call retries. Serving the last good value
// keeps one slow lookup from flipping an administrator's choice back to the
// default mid-browse.
//
// The lock guards only the cached value. The settings read runs without it,
// so a slow database stalls the callers that need a fresh value rather than
// queueing every caller behind one query; concurrent callers that find the
// cache expired may each read once.
type CachedSetting[T any] struct {
	settings SettingReader
	key      string
	ttl      time.Duration
	parse    func(string) T
	now      func() time.Time

	mu      sync.Mutex
	value   T
	read    bool
	expires time.Time
	// started counts reads begun; published is the latest one whose result
	// was cached, so a slow read cannot overwrite a newer one's value.
	started   uint64
	published uint64
}

// NewCachedSetting binds a cached read of key to a settings reader. A nil
// reader, or a nil *CachedSetting, answers with the parse of "".
func NewCachedSetting[T any](settings SettingReader, key string, ttl time.Duration, parse func(string) T) *CachedSetting[T] {
	return &CachedSetting[T]{settings: settings, key: key, ttl: ttl, parse: parse, now: time.Now}
}

// Get returns the parsed setting.
func (c *CachedSetting[T]) Get(ctx context.Context) T {
	if c == nil {
		var zero T
		return zero
	}
	if c.settings == nil {
		return c.parse("")
	}
	c.mu.Lock()
	cached, fresh := c.value, c.now().Before(c.expires)
	if !fresh {
		c.started++
	}
	generation := c.started
	c.mu.Unlock()
	if fresh {
		return cached
	}

	raw, err := c.settings.Get(ctx, c.key)
	if err != nil {
		// Another caller may have read successfully while this read ran, so
		// answer with what the cache holds now, not the earlier snapshot.
		c.mu.Lock()
		latest, read := c.value, c.read
		c.mu.Unlock()
		if read {
			return latest
		}
		return c.parse("")
	}
	value := c.parse(raw)

	c.mu.Lock()
	defer c.mu.Unlock()
	if generation < c.published {
		// A read that started later already cached its answer.
		return c.value
	}
	c.value, c.read, c.published = value, true, generation
	c.expires = c.now().Add(c.ttl)
	return value
}
