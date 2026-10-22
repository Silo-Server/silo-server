package apiv2

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
)

// TestAuditedOperationsAreExplicit pins the operations outside /admin that
// record domain audit rows. Adding one is a deliberate audit-contract change:
// update docs/admin-api.md ("Account and permission audit details") with it.
func TestAuditedOperationsAreExplicit(t *testing.T) {
	var got []string
	for _, op := range DeclaredOperations() {
		if op.Audited {
			got = append(got, op.OperationID)
		}
	}
	slices.Sort(got)
	want := []string{"changePassword", "completePasswordReset", "createPersonalAPIKey", "createProfile", "deleteProfile",
		"deleteSession", "endImpersonation", "revokePersonalAPIKey", "updateProfile"}
	if !slices.Equal(got, want) {
		t.Fatalf("audited operations = %v, want %v", got, want)
	}
}

func TestAuditedOperationOptsIntoDomainRows(t *testing.T) {
	recording := map[string]bool{}
	probe := func(id string) func(context.Context, *struct{}) (*struct{}, error) {
		return func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			recording[id] = auditmutation.Recording(ctx)
			return nil, nil
		}
	}
	h := NewHandler(Dependencies{testRegister: func(reg *Registry) {
		Register(reg, Operation{Operation: humaOp(http.MethodPost, Prefix+"/probe/audited", "auditedProbe", "probe", "audited"),
			Class: ClassPublic, RetrySafety: RetrySafetyNonRetryable, Audited: true}, probe("audited"))
		Register(reg, Operation{Operation: humaOp(http.MethodPost, Prefix+"/probe/unmarked", "unmarkedProbe", "probe", "unmarked"),
			Class: ClassPublic, RetrySafety: RetrySafetyNonRetryable}, probe("unmarked"))
	}})
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auditmutation.SetLogContext(r.Context(), &auditmutation.LogContext{Request: r})))
	})
	for _, id := range []string{"audited", "unmarked"} {
		if rec := do(t, logged, http.MethodPost, Prefix+"/probe/"+id, "", nil); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body)
		}
	}
	if !recording["audited"] || recording["unmarked"] {
		t.Fatalf("domain audit opt-in = %v", recording)
	}
}

func TestAuditedDeclarationIsForNonAdminMutations(t *testing.T) {
	for name, op := range map[string]Operation{
		"read":  {Operation: humaOp(http.MethodGet, Prefix+"/probe/audited-read", "auditedRead", "probe", "read"), Class: ClassPublic, Audited: true},
		"admin": {Operation: humaOp(http.MethodPost, Prefix+"/admin/probe", "auditedAdmin", "probe", "admin"), Class: ClassPublic, RetrySafety: RetrySafetyNonRetryable, Audited: true},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil || !strings.Contains(r.(string), "audited") {
					t.Fatalf("expected an audited declaration panic, got %v", r)
				}
			}()
			registerTestOperations(func(reg *Registry) {
				Register(reg, op, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
			})
		})
	}
}
