package backup

import (
	"context"
	"io"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// olderSchemaLoader is a loader whose first credential carry meets a column
// the backup's older schema lacks, as a real carry does when it reads an
// account's authority there, and which records where it was asked to move
// the account id sequence from.
type olderSchemaLoader struct {
	fakeLoader
	carries  *int
	advanced *[]string
}

func (l olderSchemaLoader) CarryCredentials(context.Context, string, string) (CarriedCredentials, error) {
	*l.carries++
	if *l.carries == 1 {
		return CarriedCredentials{}, &pgconn.PgError{Code: "42703", Message: `column "is_owner" does not exist`}
	}
	return CarriedCredentials{}, nil
}

func (l olderSchemaLoader) AdvanceAccountIDs(_ context.Context, _, liveURL string) error {
	*l.advanced = append(*l.advanced, liveURL)
	return nil
}

// TestCredentialFallbackAdvancesAccountIDsOnlyFromThisDeployment: when a
// backup's older schema makes the restore leave out the credentials, it
// moves the account id sequence from the live server only for this
// deployment's backup (carryFrom set). A backup of another deployment
// (carryFrom empty) keeps its sequence, even though the restore still
// replaces the live database (destination).
func TestCredentialFallbackAdvancesAccountIDsOnlyFromThisDeployment(t *testing.T) {
	const live = "postgres://live/silo"
	for _, tc := range []struct {
		name      string
		carryFrom string
		want      []string
	}{
		{name: "this deployment's backup", carryFrom: live, want: []string{live}},
		{name: "another deployment's backup", carryFrom: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var carries int
			var advanced []string
			loader := olderSchemaLoader{fakeLoader: fakeLoader{ctl: &fakeControl{t: t}}, carries: &carries, advanced: &advanced}
			post := postLoad{older: true, version: 1, out: io.Discard}
			if err := post.carry(t.Context(), loader, "postgres://restored/silo", tc.carryFrom, live, nil); err != nil {
				t.Fatal(err)
			}
			if carries != 2 {
				t.Fatalf("carried the credentials %d times, want 2 (the carry, then the fallback)", carries)
			}
			if !slices.Equal(advanced, tc.want) {
				t.Errorf("moved the account id sequence from %q, want %q", advanced, tc.want)
			}
		})
	}
}
