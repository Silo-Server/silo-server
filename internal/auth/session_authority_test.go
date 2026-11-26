package auth

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// originRow answers ReadSessionOrigin's one read: opened_via, device_key_id
// and the key's enrolled_via, each nil when the column is NULL.
type originRow struct{ openedVia, keyID, enrolledVia *string }

func (r originRow) QueryRow(context.Context, string, ...any) pgx.Row { return r }

func (r originRow) Scan(dest ...any) error {
	for i, v := range []*string{r.openedVia, r.keyID, r.enrolledVia} {
		*dest[i].(**string) = v
	}
	return nil
}

// TestReadSessionOriginGrantKey: GrantKey decides every other-app refusal,
// so it must agree with LimitedToItself and fail closed. Regression: a
// session naming a key whose row is missing read as the person's own key.
func TestReadSessionOriginGrantKey(t *testing.T) {
	str := func(s string) *string { return &s }
	appKey, grant, session := str(models.SessionOpenedAppKey), str(models.AppKeyEnrolledGrant), str(models.AppKeyEnrolledSession)
	for _, tc := range []struct {
		name string
		row  originRow
		want bool
	}{
		{"own key", originRow{appKey, str("k"), session}, false},
		{"grant key", originRow{appKey, str("k"), grant}, true},
		{"key row missing", originRow{appKey, str("k"), nil}, true},
		{"no key", originRow{str(models.SessionOpenedPassword), nil, nil}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin, err := ReadSessionOrigin(t.Context(), tc.row, &Claims{UserID: 1, SessionID: "s"})
			if err != nil || origin.GrantKey != tc.want {
				t.Fatalf("GrantKey = %v (%v), want %v", origin.GrantKey, err, tc.want)
			}
		})
	}
}
