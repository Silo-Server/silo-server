package autoscan

import (
	"context"
	"errors"
	"testing"
)

// Every autoscan row id is a `uuid` column, so an id that is not a UUID can
// only ever miss. The repository has to answer ErrNotFound before the query
// runs: sending the malformed value to Postgres raises SQLSTATE 22P02, which
// the admin routes report as a 500 rather than a 404.
//
// The repository here has a nil pool on purpose — reaching Postgres at all
// would panic and fail the test.
func TestRepositoryRejectsMalformedIDsWithoutQuerying(t *testing.T) {
	repo := &Repository{}
	ctx := context.Background()
	malformed := []string{"not-a-uuid", "", "   ", "42", "11111111-1111-1111-1111-11111111111"}

	calls := map[string]func(id string) error{
		"GetSource": func(id string) error {
			_, err := repo.GetSource(ctx, id)
			return err
		},
		"DeleteSource": func(id string) error { return repo.DeleteSource(ctx, id) },
		"UpdateSource": func(id string) error {
			_, err := repo.UpdateSource(ctx, Source{ID: id})
			return err
		},
		"GetConnection": func(id string) error {
			_, err := repo.GetConnection(ctx, id)
			return err
		},
		"DeleteConnection": func(id string) error { return repo.DeleteConnection(ctx, id) },
		"UpdateConnection": func(id string) error {
			_, err := repo.UpdateConnection(ctx, Connection{ID: id})
			return err
		},
		"CreateWebhookEndpoint": func(id string) error {
			_, _, err := repo.CreateWebhookEndpoint(ctx, id)
			return err
		},
		"RotateWebhookEndpoint": func(id string) error {
			_, _, err := repo.RotateWebhookEndpoint(ctx, id)
			return err
		},
		"DeleteWebhookEndpoint": func(id string) error { return repo.DeleteWebhookEndpoint(ctx, id) },
		"GetWebhookEndpoint": func(id string) error {
			_, err := repo.GetWebhookEndpoint(ctx, id)
			return err
		},
		"RevealWebhookToken": func(id string) error {
			_, err := repo.RevealWebhookToken(ctx, id)
			return err
		},
	}

	for name, call := range calls {
		for _, id := range malformed {
			if err := call(id); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s(%q) error = %v, want ErrNotFound", name, id, err)
			}
		}
	}
}

// A source write that binds a malformed connection id is the same miss: the FK
// path already maps a non-existent connection to ErrNotFound, and a value the
// uuid column cannot even hold must not become a 500.
func TestSourceWritesRejectMalformedConnectionID(t *testing.T) {
	repo := &Repository{}
	ctx := context.Background()
	bad := "not-a-uuid"

	if _, err := repo.CreateSource(ctx, Source{ConnectionID: &bad}); !errors.Is(err, ErrNotFound) {
		t.Errorf("CreateSource with malformed connection id: error = %v, want ErrNotFound", err)
	}
	if _, err := repo.UpdateSource(ctx, Source{ID: "11111111-1111-1111-1111-111111111111", ConnectionID: &bad}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateSource with malformed connection id: error = %v, want ErrNotFound", err)
	}
}

func TestMissingIDAcceptsWellFormedUUIDs(t *testing.T) {
	for _, id := range []string{
		"11111111-1111-1111-1111-111111111111",
		"11111111111111111111111111111111",
	} {
		if err := missingID("source", id); err != nil {
			t.Errorf("missingID(%q) = %v, want nil", id, err)
		}
	}
	// The exact value is bound to the uuid column, so padding must be a miss
	// rather than a 22P02 from Postgres.
	if err := missingID("source", "  11111111-1111-1111-1111-111111111111  "); !errors.Is(err, ErrNotFound) {
		t.Errorf("missingID(padded) = %v, want ErrNotFound", err)
	}
}

func TestMissingConnectionIDAllowsUnbound(t *testing.T) {
	blank := "   "
	if err := missingConnectionID(nil); err != nil {
		t.Errorf("missingConnectionID(nil) = %v, want nil", err)
	}
	if err := missingConnectionID(&blank); err != nil {
		t.Errorf("missingConnectionID(blank) = %v, want nil", err)
	}
}
