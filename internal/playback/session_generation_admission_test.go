package playback

import (
	"context"
	"errors"
	"path"
	"strings"
	"testing"
	"time"
)

type admissionTombstoneStore struct {
	ended      bool
	err        error
	sessionID  string
	generation string
}

func (s *admissionTombstoneStore) WasSessionGenerationEnded(_ context.Context, sessionID, generation string, _ time.Time) (bool, error) {
	s.sessionID = sessionID
	s.generation = generation
	return s.ended, s.err
}

func (*admissionTombstoneStore) RecordEndedSessionGeneration(context.Context, string, string, time.Time) error {
	return nil
}

func TestAuthorizeSessionGeneration(t *testing.T) {
	const generation = "32A4E124-9DF7-4CFA-BE49-E8E503316714"

	t.Run("valid generation consults durable authority", func(t *testing.T) {
		store := &admissionTombstoneStore{}
		err := AuthorizeSessionGeneration(t.Context(), store, "public-session", generation, time.Now())
		if err != nil {
			t.Fatalf("AuthorizeSessionGeneration() error = %v", err)
		}
		if store.sessionID != "public-session" || store.generation != strings.ToLower(generation) {
			t.Fatalf("authority received (%q, %q)", store.sessionID, store.generation)
		}
	})

	t.Run("legacy empty remains public and is consulted as empty", func(t *testing.T) {
		store := &admissionTombstoneStore{}
		if err := AuthorizeSessionGeneration(t.Context(), store, "legacy", "", time.Now()); err != nil {
			t.Fatalf("AuthorizeSessionGeneration() error = %v", err)
		}
		if store.generation != "" {
			t.Fatalf("generation = %q, want public legacy empty", store.generation)
		}
	})

	t.Run("ended generation is denied", func(t *testing.T) {
		err := AuthorizeSessionGeneration(t.Context(), &admissionTombstoneStore{ended: true}, "public-session", generation, time.Now())
		if !errors.Is(err, ErrSessionGenerationEnded) {
			t.Fatalf("error = %v, want ErrSessionGenerationEnded", err)
		}
	})

	t.Run("missing and failed authority are unavailable", func(t *testing.T) {
		for _, store := range []SessionGenerationTombstoneStore{nil, &admissionTombstoneStore{err: errors.New("db down")}} {
			err := AuthorizeSessionGeneration(t.Context(), store, "public-session", generation, time.Now())
			if !errors.Is(err, ErrSessionGenerationTombstoneUnavailable) {
				t.Fatalf("error = %v, want ErrSessionGenerationTombstoneUnavailable", err)
			}
		}
	})

	t.Run("malformed and reserved generations never reach authority", func(t *testing.T) {
		for _, generation := range []string{"not-a-uuid", LegacySessionGenerationSentinel, "{00000000-0000-0000-0000-000000000000}"} {
			store := &admissionTombstoneStore{}
			err := AuthorizeSessionGeneration(t.Context(), store, "public-session", generation, time.Now())
			if !errors.Is(err, ErrInvalidSessionGeneration) {
				t.Fatalf("generation %q error = %v", generation, err)
			}
			if store.sessionID != "" {
				t.Fatalf("generation %q reached authority", generation)
			}
		}
	})
}

func TestGenerationBoundTranscodeTransportID(t *testing.T) {
	const (
		publicA = "public-session-a"
		publicB = "public-session-b"
		genA    = "32a4e124-9df7-4cfa-be49-e8e503316714"
		genB    = "d24a82d2-2bc2-44a4-96c6-a86671d508d7"
	)

	first, err := GenerationBoundTranscodeTransportID(publicA, genA)
	if err != nil {
		t.Fatalf("GenerationBoundTranscodeTransportID() error = %v", err)
	}
	again, _ := GenerationBoundTranscodeTransportID(publicA, strings.ToUpper(genA))
	if first != again {
		t.Fatalf("transport IDs are not stable: %q != %q", first, again)
	}
	if first != strings.ToLower(first) || path.Base(first) != first || strings.ContainsAny(first, `/\\`) {
		t.Fatalf("transport ID is not canonical/path safe: %q", first)
	}
	for _, tc := range []struct{ public, generation string }{{publicA, genB}, {publicB, genA}} {
		other, err := GenerationBoundTranscodeTransportID(tc.public, tc.generation)
		if err != nil || other == first {
			t.Fatalf("identity (%q,%q) produced %q, %v", tc.public, tc.generation, other, err)
		}
	}
	for _, tc := range []struct{ public, generation string }{{"", genA}, {publicA, ""}, {publicA, "bad"}, {publicA, LegacySessionGenerationSentinel}} {
		if _, err := GenerationBoundTranscodeTransportID(tc.public, tc.generation); err == nil {
			t.Fatalf("identity (%q,%q) unexpectedly accepted", tc.public, tc.generation)
		}
	}
}
