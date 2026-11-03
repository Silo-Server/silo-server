package subtitles

import (
	"errors"
	"testing"
)

func TestProviderConfigRevisionRequiresEncryption(t *testing.T) {
	repo := NewPgRepository(nil, nil)
	if _, err := repo.GetProviderConfigWithRevision(t.Context(), "subdl"); !errors.Is(err, ErrProviderConfigEncryptionUnavailable) {
		t.Fatal(err)
	}
	if _, err := repo.SaveProviderConfigWithRevision(t.Context(), "subdl", ProviderConfigChange{APIKey: "fixture"}, new(int64(0))); !errors.Is(err, ErrProviderConfigEncryptionUnavailable) {
		t.Fatal(err)
	}
}
