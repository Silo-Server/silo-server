package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

const passwordSessionsLegacyPath = "/api/v1/auth/account/password"
const passwordSessionsChangeOperation = "changePassword"
const passwordSessionsPrimaryPrincipal = "primary_profile"

var RequiredPasswordSessionsScenarios = []string{"password.ok", "password.meaning", "password.shape", "session_delete.ok", "session_delete.meaning", "session_delete.shape"}

func PasswordSessionsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var selected []*Catalog
	for _, group := range []struct {
		method, path string
		ids          []string
	}{
		{http.MethodPost, passwordSessionsLegacyPath, RequiredPasswordSessionsScenarios[:3]},
		{http.MethodDelete, "/api/v1/auth/sessions/{id}", RequiredPasswordSessionsScenarios[3:]},
	} {
		rows, err := requiredAcceptance(catalogs, group.method, []string{group.path}, group.ids)
		if err != nil {
			return nil, err
		}
		selected = append(selected, rows...)
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				password := strings.HasPrefix(s.ID, "password.")
				op, principal := "deleteSession", Principal{Class: "authenticated"}
				if password {
					op = passwordSessionsChangeOperation
					principal = Principal{Class: passwordSessionsPrimaryPrincipal}
				}
				steps := 0
				if s.ID == "password.meaning" {
					steps = 2
				}
				repeat := 0
				if s.ID == "session_delete.meaning" {
					repeat = 2
				}
				if pair.OperationID != op || pair.Method != r.Method || pair.Principal != nil || !reflect.DeepEqual(s.Principal, principal) || len(s.Then) != steps || len(pair.Then) != steps || len(s.Settings) != 0 || s.Request.Repeat != repeat || !passwordSessionRequestSame(s.Request, pair.Request) {
					return nil, fmt.Errorf("%s: changed password/session exchange", s.ID)
				}
				for _, req := range s.Requires {
					if req != frozenDatabaseRequirement {
						return nil, fmt.Errorf("%s: unsupported requirement", s.ID)
					}
				}
				for i, step := range s.Then {
					v := pair.Then[i]
					if v.OperationID != lifecycleLoginOperation || v.Method != step.Method || len(v.FromPrevious) != 0 || !reflect.DeepEqual(v.Principal, step.Principal) || !passwordSessionRequestSame(step.Request, v.Request) {
						return nil, fmt.Errorf("%s: changed password follow-up", s.ID)
					}
				}
			}
		}
	}
	return selected, nil
}
func passwordSessionRequestSame(original, translated Request) bool {
	original.Path = strings.Replace(original.Path, passwordSessionsLegacyPath, "/api/v2/account/password", 1)
	original.Path = strings.Replace(original.Path, "/api/v1/", "/api/v2/", 1)
	var a, b any
	if len(original.Body) > 0 {
		if json.Unmarshal(original.Body, &a) != nil {
			return false
		}
	}
	if len(translated.Body) > 0 {
		if json.Unmarshal(translated.Body, &b) != nil {
			return false
		}
	}
	original.Body = nil
	translated.Body = nil
	return reflect.DeepEqual(original, translated) && reflect.DeepEqual(a, b)
}
