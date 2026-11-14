package access

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestScopeCheckProfileVerification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scope   Scope
		profile *userstore.Profile
		allowed bool
	}{
		{"unchanged", Scope{ProfileID: "parent", ProfileVerified: true, ProfilePINRevision: 4}, &userstore.Profile{ID: "parent", PINRevision: 4}, true},
		{"legacy zero", Scope{ProfileID: "parent", ProfileVerified: true}, &userstore.Profile{ID: "parent"}, true},
		{"changed", Scope{ProfileID: "parent", ProfileVerified: true, ProfilePINRevision: 4}, &userstore.Profile{ID: "parent", PINRevision: 5}, false},
		{"new PIN", Scope{ProfileID: "parent", ProfileVerified: true}, &userstore.Profile{ID: "parent", PINRevision: 1}, false},
		{"wrong profile", Scope{ProfileID: "child", ProfileVerified: true}, &userstore.Profile{ID: "parent"}, false},
		{"unverified", Scope{ProfileID: "parent"}, &userstore.Profile{ID: "parent"}, false},
		{"skipped", Scope{ProfileID: "parent", ProfileVerified: true, PINVerificationSkipped: true}, &userstore.Profile{ID: "parent"}, false},
		{"missing profile", Scope{ProfileID: "parent", ProfileVerified: true}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.scope.CheckProfileVerification(tc.profile)
			if tc.allowed && err != nil {
				t.Fatal(err)
			}
			if !tc.allowed && !errors.Is(err, ErrProfileUnverified) {
				t.Fatalf("verification error = %v", err)
			}
		})
	}
}
