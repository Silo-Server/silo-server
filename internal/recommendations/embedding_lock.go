package recommendations

import (
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/embeddingvectors"
)

const CanonicalEmbeddingDimensions = embeddingvectors.CanonicalDimensions

// EmbeddingLock records the embedding space currently locked for an
// installation.
type EmbeddingLock struct {
	BaseURL           string `json:"base_url"`
	Model             string `json:"model"`
	SourceDimensions  int    `json:"source_dimensions"`
	StorageDimensions int    `json:"storage_dimensions"`
}

// Marshal encodes the lock as JSON.
func (l EmbeddingLock) Marshal() (string, error) {
	raw, err := json.Marshal(l)
	if err != nil {
		return "", fmt.Errorf("marshal embedding lock: %w", err)
	}
	return string(raw), nil
}

// ParseEmbeddingLock decodes a lock from JSON.
func ParseEmbeddingLock(raw string) (*EmbeddingLock, error) {
	if raw == "" {
		return nil, nil
	}

	var lock EmbeddingLock
	if err := json.Unmarshal([]byte(raw), &lock); err != nil {
		return nil, fmt.Errorf("parse embedding lock: %w", err)
	}
	return &lock, nil
}

// Validate ensures the lock matches the provided embedding configuration.
func (l EmbeddingLock) ValidateConfig(baseURL, model string) error {
	if l.BaseURL != "" && l.BaseURL != baseURL {
		return fmt.Errorf("recommendations reset required: embedding base URL changed from %q to %q", l.BaseURL, baseURL)
	}
	if l.Model != "" && l.Model != model {
		return fmt.Errorf("recommendations reset required: embedding model changed from %q to %q", l.Model, model)
	}
	return nil
}

// Validate ensures the lock matches the provided embedding configuration.
func (l EmbeddingLock) Validate(baseURL, model string, sourceDimensions int) error {
	if err := l.ValidateConfig(baseURL, model); err != nil {
		return err
	}
	if l.SourceDimensions != 0 && l.SourceDimensions != sourceDimensions {
		return fmt.Errorf("recommendations reset required: embedding source dimensions changed from %d to %d", l.SourceDimensions, sourceDimensions)
	}
	if l.StorageDimensions != 0 && l.StorageDimensions != CanonicalEmbeddingDimensions {
		return fmt.Errorf("recommendations reset required: embedding storage dimensions changed from %d to %d", l.StorageDimensions, CanonicalEmbeddingDimensions)
	}
	return nil
}
