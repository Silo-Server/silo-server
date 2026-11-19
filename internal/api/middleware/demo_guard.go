package middleware

import "context"

// DemoSettingsReader is the subset of ServerSettingsStore that reads the
// "demo.enabled" server setting.
type DemoSettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}
