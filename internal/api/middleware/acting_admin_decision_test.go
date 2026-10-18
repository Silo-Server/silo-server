package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// verifiedProfileStage stands in for RequireViewerAccess: it resolves and
// verifies a declared profile into the request context.
func verifiedProfileStage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if id := r.Header.Get("X-Profile-Id"); id != "" {
			ctx = access.SetScope(ctx, access.Scope{UserID: 7, ProfileID: id, ProfileVerified: true})
			ctx = SetProfileID(ctx, id)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// householdPrimary has one primary profile, "parent", and one child, "kid".
func householdPrimary(_ context.Context, _ int, profileID string) (bool, bool, error) {
	switch profileID {
	case "parent":
		return true, true, nil
	case "kid":
		return false, true, nil
	}
	return false, false, nil
}

// TestActingAdminAgreesWithGate: on a route outside the admin groups,
// ActingAdmin answers exactly what RequireActingAdmin decides on an admin
// route, for both the policy-backed and the legacy decision.
func TestActingAdminAgreesWithGate(t *testing.T) {
	cases := []struct {
		name       string
		claims     *auth.Claims
		profile    string
		restricted bool
		want       bool
	}{
		{name: "user account", claims: &auth.Claims{UserID: 7, Role: "user", TokenType: auth.TokenTypeAccess}, profile: "parent"},
		{name: "primary profile", claims: adminClaims(), profile: "parent", restricted: true, want: true},
		{name: "child profile", claims: adminClaims(), profile: "kid", restricted: true},
		{name: "child profile, no PIN anywhere", claims: adminClaims(), profile: "kid"},
		{name: "unknown profile", claims: adminClaims(), profile: "someone-else"},
		{name: "no profile, unrestricted household", claims: adminClaims(), want: true},
		{name: "no profile, restricted household", claims: adminClaims(), restricted: true},
		{name: "API key, no profile, restricted household", claims: &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAPIKey}, restricted: true, want: true},
	}
	deciders := map[string]func(HouseholdProfileRequirement) ActingAdminDecider{
		"policy": func(household HouseholdProfileRequirement) ActingAdminDecider {
			return NewPolicyActingAdminDecider(newMiddlewarePolicyPDP(t), householdPrimary, household)
		},
		"legacy": func(household HouseholdProfileRequirement) ActingAdminDecider {
			return NewActingAdminDecider(householdPrimary, household)
		},
	}
	for kind, build := range deciders {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				restricted := tc.restricted
				decide := build(func(context.Context, int) (bool, error) { return restricted, nil })

				var helper bool
				var helperErr error
				route := func(gate func(http.Handler) http.Handler) int {
					h := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						helper, helperErr = ActingAdmin(r.Context())
						w.WriteHeader(http.StatusNoContent)
					}))
					if gate != nil {
						h = gate(h)
					}
					h = ProvideActingAdmin(decide)(verifiedProfileStage(h))
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					if tc.profile != "" {
						req.Header.Set("X-Profile-Id", tc.profile)
					}
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req.WithContext(SetClaims(req.Context(), tc.claims)))
					return rec.Code
				}

				if route(nil); helperErr != nil || helper != tc.want {
					t.Fatalf("ActingAdmin = %v, %v; want %v", helper, helperErr, tc.want)
				}
				gated := route(RequireActingAdminWith(decide)) == http.StatusNoContent
				if gated != tc.want {
					t.Fatalf("gate admitted = %v, want %v", gated, tc.want)
				}
			})
		}
	}
}

// TestActingAdminNeedsAVerifiedProfile: naming the primary profile counts
// only once viewer access verified it in this request.
func TestActingAdminNeedsAVerifiedProfile(t *testing.T) {
	decide := NewActingAdminDecider(householdPrimary, nil)
	for name, ctx := range map[string]context.Context{
		"profile without viewer scope": SetProfileID(context.Background(), "parent"),
		"scope for another profile": access.SetScope(SetProfileID(context.Background(), "parent"),
			access.Scope{UserID: 7, ProfileID: "kid", ProfileVerified: true}),
		"unverified scope": access.SetScope(SetProfileID(context.Background(), "parent"),
			access.Scope{UserID: 7, ProfileID: "parent"}),
	} {
		t.Run(name, func(t *testing.T) {
			decided := SetActingAdminDecider(SetClaims(ctx, adminClaims()), decide)
			if ok, err := ActingAdmin(decided); ok || err != nil {
				t.Fatalf("ActingAdmin = %v, %v; want false", ok, err)
			}
		})
	}

	// A header the viewer gate never saw is still a declared profile.
	var got bool
	h := ProvideActingAdmin(decide)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = ActingAdmin(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Profile-Id", "parent")
	h.ServeHTTP(httptest.NewRecorder(), req.WithContext(SetClaims(req.Context(), adminClaims())))
	if got {
		t.Fatal("an unverified X-Profile-Id header made the request an acting admin")
	}
}

func TestActingAdminFailsClosed(t *testing.T) {
	if ok, err := ActingAdmin(SetClaims(context.Background(), adminClaims())); ok || err != nil {
		t.Fatalf("without a decider: ActingAdmin = %v, %v; want false", ok, err)
	}
	down := errors.New("profile store down")
	ctx := SetActingAdminDecider(SetClaims(context.Background(), adminClaims()), func(context.Context, ActingAdminRequest) (bool, error) {
		return true, down
	})
	if ok, err := ActingAdmin(ctx); ok || !errors.Is(err, down) {
		t.Fatalf("decider error: ActingAdmin = %v, %v; want false, %v", ok, err, down)
	}
	// The legacy decider allows a profile-less session whose household
	// lookup failed, alongside the error; ActingAdmin must not.
	legacy := NewActingAdminDecider(nil, func(context.Context, int) (bool, error) { return false, down })
	ctx = SetActingAdminDecider(SetClaims(context.Background(), adminClaims()), legacy)
	if ok, err := ActingAdmin(ctx); ok || !errors.Is(err, down) {
		t.Fatalf("legacy household lookup error: ActingAdmin = %v, %v; want false, %v", ok, err, down)
	}
	called := false
	ctx = SetActingAdminDecider(SetClaims(context.Background(), userClaims()), func(context.Context, ActingAdminRequest) (bool, error) {
		called = true
		return true, nil
	})
	if ok, _ := ActingAdmin(ctx); ok || called {
		t.Fatalf("user account: ActingAdmin = %v, decider consulted = %v", ok, called)
	}
}
