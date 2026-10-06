package branding

import (
	"context"
	"errors"
	"testing"
)

type countingSettings struct {
	values   map[string]string
	gets     int
	batches  int
	batchErr error
}

func (c *countingSettings) Get(_ context.Context, key string) (string, error) {
	c.gets++
	return c.values[key], nil
}

func (c *countingSettings) Set(_ context.Context, key, value string) error {
	c.values[key] = value
	return nil
}

func (c *countingSettings) GetMany(_ context.Context, keys ...string) (map[string]string, error) {
	c.batches++
	if c.batchErr != nil {
		return nil, c.batchErr
	}
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if v := c.values[key]; v != "" {
			out[key] = v
		}
	}
	return out, nil
}

func TestLoadReadsSettingsInOneBatch(t *testing.T) {
	settings := &countingSettings{values: map[string]string{
		KeyServerName:           "Harbor",
		KeyAccentColor:          "#f5a524",
		"branding.wordmark_ref": "abc.webp",
	}}
	snap := NewService(settings, nil).Load(context.Background())
	if settings.batches != 1 || settings.gets != 0 {
		t.Fatalf("batches=%d gets=%d, want one batch read and no single reads", settings.batches, settings.gets)
	}
	if snap.ServerName != "Harbor" || snap.AccentColor != "#f5a524" || snap.AssetRef(KindWordmark) != "abc.webp" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.LoginSubtitle != DefaultLoginSubtitle {
		t.Fatalf("unset subtitle = %q, want the default", snap.LoginSubtitle)
	}
}

func TestLoadFallsBackToSingleReadsWhenBatchFails(t *testing.T) {
	settings := &countingSettings{
		values:   map[string]string{KeyServerName: "Harbor"},
		batchErr: errors.New("batch unsupported"),
	}
	snap := NewService(settings, nil).Load(context.Background())
	if snap.ServerName != "Harbor" || settings.gets == 0 {
		t.Fatalf("name=%q gets=%d, want per-key reads after the batch failed", snap.ServerName, settings.gets)
	}
}
