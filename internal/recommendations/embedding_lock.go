package recommendations

import (
	"encoding/json"
	"fmt"
	"strings"

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

// SameEmbeddingModel reports whether two model names name one model: they are
// equal, or one is the other behind a vendor prefix, the way a gateway such as
// OpenRouter names it (google/gemini-embedding-001). The same model returns the
// same vectors whichever provider serves it. Two different prefixes name
// different models.
func SameEmbeddingModel(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return a == b
	}
	return a == b || strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a)
}

// ValidateConfig ensures the lock accepts the configured model. The embedding
// space belongs to the model, so a different provider serving the same model
// is accepted; the vector size is still checked once one is returned.
func (l EmbeddingLock) ValidateConfig(model string) error {
	if l.Model != "" && !SameEmbeddingModel(l.Model, model) {
		return fmt.Errorf("recommendations reset required: embedding model changed from %q to %q", l.Model, model)
	}
	return nil
}

// Validate ensures the lock matches the provided embedding configuration.
func (l EmbeddingLock) Validate(model string, sourceDimensions int) error {
	if err := l.ValidateConfig(model); err != nil {
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
