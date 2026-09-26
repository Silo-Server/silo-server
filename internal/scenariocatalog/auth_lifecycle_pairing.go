package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

const lifecyclePublicPrincipal = "public"

const lifecycleLogoutPath = "/api/v1/auth/logout"
const lifecycleLoginOperation = "login"
const lifecycleLogoutOperation = "logout"

var RequiredAuthLifecycleScenarios = []string{"login.ok", "login.user_meaning", "login.email_alias", "login.grouped_download_policy", "login.admin_permissions", "login.unknown_fields_ignored", "refresh.ok", "refresh.rotation", "refresh.shape", "logout.session_gone"}

func AuthLifecycleAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{loginCredentialsLegacyRoute, "/api/v1/auth/refresh", lifecycleLogoutPath}, RequiredAuthLifecycleScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				op, principal, repeat := lifecycleLoginOperation, Principal{Class: lifecyclePublicPrincipal}, 0
				if strings.HasPrefix(s.ID, "refresh.") {
					op = "refreshSession"
				}
				if s.ID == "logout.session_gone" {
					op = lifecycleLogoutOperation
					principal = Principal{Class: "authenticated"}
					repeat = 2
				}
				want := s.Request
				want.Path = strings.Replace(r.Path, "/api/v1/", "/api/v2/", 1)
				originalBody := s.Request.Body
				translatedBody := pair.Request.Body
				want.Body = nil
				actual := pair.Request
				actual.Body = nil
				var a, b any
				if len(originalBody) > 0 {
					if err := json.Unmarshal(originalBody, &a); err != nil {
						return nil, err
					}
				}
				if len(translatedBody) > 0 {
					if err := json.Unmarshal(translatedBody, &b); err != nil {
						return nil, err
					}
				}
				if !reflect.DeepEqual(want, actual) || !reflect.DeepEqual(a, b) || s.Request.Path != r.Path || s.Request.Repeat != repeat ||
					!reflect.DeepEqual(s.Principal, principal) || pair.Principal != nil || pair.OperationID != op || pair.Method != http.MethodPost ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Settings) != 0 {
					return nil, fmt.Errorf("%s: changed lifecycle request or authority", s.ID)
				}
				for _, req := range s.Requires {
					if req != frozenDatabaseRequirement {
						return nil, fmt.Errorf("%s: unsupported requirement", s.ID)
					}
				}
			}
		}
	}
	return selected, nil
}
