package audiobooks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

type fakeSettingsReader struct {
	value string
	err   error
}

func (f *fakeSettingsReader) GetString(_ context.Context, key string) (string, error) {
	if key != "audiobookshelf_compat.enabled" {
		return "", errors.New("unexpected key: " + key)
	}
	return f.value, f.err
}

func TestServiceABSCompatEnabledReadsFlag(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		want   bool
	}{
		{"flag true", "true", true},
		{"flag false", "false", false},
		{"flag empty defaults false", "", false},
		{"flag garbage defaults false", "yes-please", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			svc := New(&fakeSettingsReader{value: tc.stored})
			got, err := svc.ABSCompatEnabled(context.Background())
			if err != nil {
				t.Fatalf("ABSCompatEnabled returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ABSCompatEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

type socketTokenStore struct {
	abs.TokenStore
	tok abs.ABSToken
}

func (s socketTokenStore) GetTokenByJTI(context.Context, string) (abs.ABSToken, error) {
	return s.tok, nil
}

type refusingProfileVerifier struct{ calls int }

func (v *refusingProfileVerifier) VerifyTokenProfile(context.Context, string, string, *int64) error {
	v.calls++
	return abs.ErrTokenProfileUnverified
}

// The Socket.io auth applies the profile check bearerAuth applies on HTTP,
// after the revocation check.
func TestABSSocketTokenValidatorChecksProfile(t *testing.T) {
	claims := &abs.Claims{UserID: "1", ProfileID: "parent", JTI: "jti"}
	verifier := &refusingProfileVerifier{}

	revoked, err := absSocketTokenValidator(socketTokenStore{}, verifier)(context.Background(), claims)
	if revoked || !errors.Is(err, abs.ErrTokenProfileUnverified) {
		t.Fatalf("active token, refused profile = %v, %v; want the profile refusal", revoked, err)
	}

	now := time.Now()
	revoked, err = absSocketTokenValidator(socketTokenStore{tok: abs.ABSToken{RevokedAt: &now}}, verifier)(context.Background(), claims)
	if !revoked || err != nil || verifier.calls != 1 {
		t.Fatalf("revoked token = %v, %v (verifier calls %d); want revoked without a profile check", revoked, err, verifier.calls)
	}
}
