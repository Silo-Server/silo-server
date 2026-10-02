package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

const (
	decisionAuthenticatedPrincipal = "authenticated"
	decisionApprovePath            = "/api/v1/auth/device/approve"
	decisionPollPath               = "/api/v1/auth/device/poll"
	decisionApproveOperation       = "approveDeviceLogin"
	decisionPollOperation          = "pollDeviceLogin"
	decisionAdminPrincipal         = "admin"
)

var RequiredDeviceDecisionsScenarios = []string{"approve.ok", "approve.by_code", "approve.meaning", "approve.idempotent", "approve.shape", "deny.ok", "deny.idempotent", "deny.approved", "deny.any_account", "deny.shape", "device_poll.approved", "device_poll.consumed", "device_poll.remote_approved"}

func DeviceDecisionsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var selected []*Catalog
	for _, group := range []struct {
		path string
		ids  []string
	}{
		{decisionApprovePath, RequiredDeviceDecisionsScenarios[:5]},
		{"/api/v1/auth/device/deny", RequiredDeviceDecisionsScenarios[5:10]},
		{decisionPollPath, RequiredDeviceDecisionsScenarios[10:]},
	} {
		rows, err := requiredAcceptance(catalogs, http.MethodPost, []string{group.path}, group.ids)
		if err != nil {
			return nil, err
		}
		selected = append(selected, rows...)
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				p := s.V2Expectation
				op, principal := decisionApproveOperation, decisionAuthenticatedPrincipal
				if strings.HasPrefix(s.ID, "deny.") {
					op = "denyDeviceLogin"
				}
				if strings.HasPrefix(s.ID, "device_poll.") {
					op = decisionPollOperation
					principal = lifecyclePublicPrincipal
				}
				if s.ID == "deny.any_account" {
					principal = decisionAdminPrincipal
				}
				repeats := 0
				if s.ID == "approve.idempotent" || s.ID == "device_poll.consumed" {
					repeats = 2
				}
				steps := 0
				if s.ID == "approve.meaning" || s.ID == "deny.approved" {
					steps = 1
				}
				if p.OperationID != op || p.Method != http.MethodPost || p.Principal != nil || !reflect.DeepEqual(s.Principal, Principal{Class: principal}) || s.Request.Repeat != repeats || len(s.Settings) != 0 || len(s.Then) != steps || len(p.Then) != steps || !passwordSessionRequestSame(s.Request, p.Request) {
					return nil, fmt.Errorf("%s: changed device decision sequence", s.ID)
				}
				for _, req := range s.Requires {
					if req != frozenDatabaseRequirement {
						return nil, fmt.Errorf("%s: unsupported requirement", s.ID)
					}
				}
				for i, step := range s.Then {
					v := p.Then[i]
					if v.OperationID != decisionPollOperation || v.Method != step.Method || len(v.FromPrevious) != 0 || !reflect.DeepEqual(v.Principal, step.Principal) || !passwordSessionRequestSame(step.Request, v.Request) {
						return nil, fmt.Errorf("%s: changed follow-up", s.ID)
					}
				}
			}
		}
	}
	return selected, nil
}
