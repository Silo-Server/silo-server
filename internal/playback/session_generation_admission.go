package playback

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrSessionGenerationEnded means the exact public playback lifecycle has
// already committed its durable terminal tombstone.
var ErrSessionGenerationEnded = errors.New("session generation ended")

var transcodeTransportNamespace = uuid.MustParse("f605f8b4-492a-4ca1-8e85-02193b26bcde")

// CanonicalPublicSessionGeneration validates a token-visible generation and
// returns its canonical lower-case UUID. The empty string is the legacy public
// generation and remains empty; the persistence-only nil UUID is never public.
func CanonicalPublicSessionGeneration(generation string) (string, error) {
	if generation == "" {
		return "", nil
	}
	parsed, err := uuid.Parse(generation)
	if err != nil {
		return "", fmt.Errorf("%w: malformed UUID", ErrInvalidSessionGeneration)
	}
	if parsed == uuid.Nil {
		return "", fmt.Errorf("%w: reserved legacy sentinel", ErrInvalidSessionGeneration)
	}
	return parsed.String(), nil
}

// AuthorizeSessionGeneration fails closed unless durable authority confirms
// that the exact public lifecycle has not ended.
func AuthorizeSessionGeneration(ctx context.Context, store SessionGenerationTombstoneStore, sessionID, generation string, now time.Time) error {
	canonical, err := CanonicalPublicSessionGeneration(generation)
	if err != nil {
		return err
	}
	if store == nil {
		return ErrSessionGenerationTombstoneUnavailable
	}
	ended, err := store.WasSessionGenerationEnded(ctx, sessionID, canonical, now)
	if err != nil {
		return errors.Join(ErrSessionGenerationTombstoneUnavailable, err)
	}
	if ended {
		return ErrSessionGenerationEnded
	}
	return nil
}

// GenerationBoundTranscodeTransportID derives a private, path-safe UUID from
// an exact nonlegacy public playback identity.
func GenerationBoundTranscodeTransportID(publicSessionID, generation string) (string, error) {
	if strings.TrimSpace(publicSessionID) == "" {
		return "", fmt.Errorf("public session ID is required")
	}
	canonical, err := CanonicalPublicSessionGeneration(generation)
	if err != nil {
		return "", err
	}
	if canonical == "" {
		return "", fmt.Errorf("%w: generation-bound transport requires a nonlegacy generation", ErrInvalidSessionGeneration)
	}
	return uuid.NewSHA1(transcodeTransportNamespace, []byte(publicSessionID+"\x00"+canonical)).String(), nil
}
