package apiv2

import (
	"net/http"
	"strings"
	"testing"
)

func TestInvitationProfileLimitValidation(t *testing.T) {
	for _, field := range []string{"0", "-1", "2147483648", "9223372036854775808", "null", "1.5", `"2"`} {
		t.Run(field, func(t *testing.T) {
			f := fixtureInvitations()
			h := invitationTestHandler(f)
			requireProblem(t, do(t, h, http.MethodPost, Prefix+"/admin/invitations", `{"email":"invitee@example.invalid","max_profiles":`+field+`}`, actingRequestAdmin), TypeValidationFailed)
			if f.writes != 0 {
				t.Fatal("invalid profile limit reached invitation service")
			}
		})
	}
}

func TestInvitationProfileLimitContract(t *testing.T) {
	f := fixtureInvitations()
	h := invitationTestHandler(f)
	created := do(t, h, http.MethodPost, Prefix+"/admin/invitations", `{"email":"invitee@example.invalid","max_profiles":2}`, actingRequestAdmin)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"max_profiles":2`) {
		t.Fatal(created.Code, created.Body.String())
	}
	if f.send == nil || f.send.MaxProfiles == nil || *f.send.MaxProfiles != 2 {
		t.Fatal("profile limit was not propagated to invitation service")
	}
	f.row.MaxProfiles = new(2)
	for _, path := range []string{"/admin/invitations/7", "/admin/invitations"} {
		stored := do(t, h, http.MethodGet, Prefix+path, "", actingRequestAdmin)
		if stored.Code != http.StatusOK || !strings.Contains(stored.Body.String(), `"max_profiles":2`) {
			t.Fatal(stored.Code, stored.Body.String())
		}
	}
	resent := do(t, h, http.MethodPost, Prefix+"/admin/invitations/7/resend", "", actingRequestAdmin)
	if resent.Code != http.StatusCreated || !strings.Contains(resent.Body.String(), `"max_profiles":2`) {
		t.Fatal(resent.Code, resent.Body.String())
	}
	for _, path := range []string{"/invitations/capabilities", "/admin/invitations/capabilities"} {
		caps := do(t, h, http.MethodGet, Prefix+path, "", actingRequestAdmin)
		if caps.Code != http.StatusOK || !strings.Contains(caps.Body.String(), `"profile_limit":true`) {
			t.Fatal(caps.Code, caps.Body.String())
		}
	}
}

func TestInvitationProfileLimitOmission(t *testing.T) {
	f := fixtureInvitations()
	h := invitationTestHandler(f)
	created := do(t, h, http.MethodPost, Prefix+"/admin/invitations", `{"email":"invitee@example.invalid"}`, actingRequestAdmin)
	if created.Code != http.StatusCreated || f.send == nil || f.send.MaxProfiles != nil {
		t.Fatal(created.Code, created.Body.String(), f.send)
	}
	if strings.Contains(created.Body.String(), "max_profiles") {
		t.Fatal("omission advertised an explicit profile override")
	}
	d := requestDeps(fixtureRequests())
	caps := do(t, NewHandler(d), http.MethodGet, Prefix+"/invitations/capabilities", "", nil)
	if caps.Code != http.StatusOK || !strings.Contains(caps.Body.String(), `"profile_limit":false`) {
		t.Fatal(caps.Code, caps.Body.String())
	}
}

func TestInvitationNonReplacingCreationContract(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        bool
	}{
		{"omitted", "", true},
		{"explicit true", `,"replace_existing":true`, true},
		{"explicit false", `,"replace_existing":false`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixtureInvitations()
			r := do(t, invitationTestHandler(f), http.MethodPost, Prefix+"/admin/invitations", `{"email":"invitee@example.invalid"`+tc.field+`}`, actingRequestAdmin)
			if r.Code != http.StatusCreated || f.send == nil || f.send.ReplaceExisting == nil || *f.send.ReplaceExisting != tc.want {
				t.Fatalf("status=%d body=%s input=%+v", r.Code, r.Body.String(), f.send)
			}
		})
	}
	for _, field := range []string{"null", `"false"`, "0", "1", "{}", "[]"} {
		t.Run("invalid "+field, func(t *testing.T) {
			f := fixtureInvitations()
			requireProblem(t, do(t, invitationTestHandler(f), http.MethodPost, Prefix+"/admin/invitations", `{"email":"invitee@example.invalid","replace_existing":`+field+`}`, actingRequestAdmin), TypeValidationFailed)
			if f.writes != 0 {
				t.Fatal("invalid replace_existing reached the invitation service")
			}
		})
	}
	for _, configured := range []bool{true, false} {
		d := requestDeps(fixtureRequests())
		if configured {
			d.Invitations = fixtureInvitations()
		}
		for _, path := range []string{"/invitations/capabilities", "/admin/invitations/capabilities"} {
			r := do(t, NewHandler(d), http.MethodGet, Prefix+path, "", actingRequestAdmin)
			want := `"non_replacing_creation":false`
			if configured {
				want = `"non_replacing_creation":true`
			}
			if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), want) {
				t.Fatal(r.Code, r.Body.String())
			}
		}
	}
}
