package recommendations

import "testing"

func TestValidateQueryEmbeddingLockAllowsMissingLock(t *testing.T) {
	if err := validateQueryEmbeddingLock(nil, "http://embeddings", "model-a", 1536); err != nil {
		t.Fatalf("missing query embedding lock returned error: %v", err)
	}
}

func TestValidateQueryEmbeddingLockChecksExistingLock(t *testing.T) {
	lock := &EmbeddingLock{
		BaseURL:           "http://embeddings",
		Model:             "model-a",
		SourceDimensions:  384,
		StorageDimensions: CanonicalEmbeddingDimensions,
	}

	if err := validateQueryEmbeddingLock(lock, "http://embeddings", "model-a", 1536); err == nil {
		t.Fatal("dimension mismatch returned nil error")
	}
}
