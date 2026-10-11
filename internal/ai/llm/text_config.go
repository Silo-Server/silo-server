package llm

import (
	"context"
	"time"
)

type textConfigKey struct{}

// WithTextConfig binds a job's provider snapshot to all of its text requests.
// The snapshot stays in memory; rotating credentials are still read on demand.
func WithTextConfig(ctx context.Context, cfg Config) context.Context {
	return context.WithValue(ctx, textConfigKey{}, cfg)
}

// TextConfig resolves the current provider, or a job's previously bound snapshot.
// It reads settings without making provider requests or refreshing credentials.
func (c *Client) TextConfig(ctx context.Context) (Config, error) {
	if cfg, ok := ctx.Value(textConfigKey{}).(Config); ok {
		return cfg, nil
	}
	cfg := c.Config()
	if cfg.TextConfigSource == nil {
		return cfg, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return cfg.TextConfigSource(readCtx)
}

// TextConfigured reports whether the resolved text provider has an endpoint and model.
func (c Config) TextConfigured() bool {
	if c.ChatModel == "" {
		return false
	}
	if c.AuthMode == AuthModeChatGPT {
		return c.ChatGPTTokenSource != nil
	}
	return c.BaseURL != ""
}
