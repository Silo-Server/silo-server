package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	apiv2 "github.com/Silo-Server/silo-server/contracts/api/v2"
)

// openAPIOperation is the slice of one embedded v2 operation the validators read.
type openAPIOperation struct {
	OperationID string `json:"operationId"`
	Class       string `json:"x-silo-class"`
	Parameters  []struct {
		Name   string `json:"name"`
		In     string `json:"in"`
		Schema struct {
			Type string `json:"type"`
		} `json:"schema"`
	} `json:"parameters"`
}

// openAPIPaths parses the embedded v2 document exactly once. The document is
// immutable for the life of the process and is read on every pairing check,
// so re-parsing it per scenario dominated catalog loading.
var openAPIPaths = sync.OnceValues(func() (map[string]map[string]openAPIOperation, error) {
	var spec struct {
		Paths map[string]map[string]openAPIOperation `json:"paths"`
	}
	if err := json.Unmarshal(apiv2.OpenAPI, &spec); err != nil {
		return nil, err
	}
	return spec.Paths, nil
})

const (
	deviceListPath      = "/api/v2/devices"
	deviceListOperation = "listDevices"
)

// ValidatePairing ties the recorded operation to its actual method and route.
// Concrete fixture values may fill path parameters; they cannot change the route.
func ValidatePairing(pair *V2Expectation) error {
	if pair == nil {
		return fmt.Errorf("missing v2 expectation")
	}
	if err := ValidateV2Sequence(pair); err != nil {
		return err
	}
	return validateOperation(pair.OperationID, pair.Method, pair.Request.Path)
}

// OperationClass reports the declared gate class (x-silo-class) of a v2
// operation from the embedded document, so a harness can tell which gates an
// exchange will meet before it is sent.
func OperationClass(operationID string) (string, bool) {
	paths, err := openAPIPaths()
	if err != nil {
		return "", false
	}
	for _, methods := range paths {
		for _, op := range methods {
			if op.OperationID == operationID {
				return op.Class, true
			}
		}
	}
	return "", false
}

func validateOperation(operationID, method, requestPath string) error {
	paths, err := openAPIPaths()
	if err != nil {
		return err
	}
	for path, methods := range paths {
		operation, ok := methods[strings.ToLower(method)]
		if !ok || operation.OperationID != operationID {
			continue
		}
		want, got := strings.Split(path, "/"), strings.Split(requestPath, "/")
		if len(want) != len(got) {
			continue
		}
		matches := true
		for i, segment := range want {
			parameter := strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
			if (parameter && got[i] == "") || (!parameter && segment != got[i]) {
				matches = false
			}
		}
		if matches {
			return nil
		}
	}
	return fmt.Errorf("v2 operation %q does not match %s %s", operationID, method, requestPath)
}

// RequiredProfileListScenarios is deliberately fixed: deleting a pilot case or
// clearing its pairing must fail acceptance rather than shrink the tested set.
var RequiredProfileListScenarios = []string{
	"profiles_list.ok", "profiles_list.meaning", "profiles_list.shape", "profiles_list.sorted",
	"profiles_list.no_profile_needed", "profiles_list.other_account", "profiles_list.api_key",
	"profiles_list.other_account_profile", "profiles_list.no_token", "profiles_list.error_shape",
}

func ProfileListAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	return requiredListAcceptance(catalogs, "/api/v1/profiles/", RequiredProfileListScenarios)
}

// RequiredDeviceListScenarios fixes the bounded device-list acceptance inventory.
var RequiredDeviceListScenarios = []string{
	"devices_list.ok", "devices_list.own_only", "devices_list.current_device",
	"devices_list.sorted", "devices_list.household_scope", "devices_list.household_forbidden",
	"devices_list.scope_case", "devices_list.shape", "devices_list.shape_fields",
	"devices_list.no_profile", "devices_list.other_account_profile", "devices_list.no_token", "devices_list.error_shape",
}

func DeviceListAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	return requiredListAcceptance(catalogs, "/api/v1/devices/", RequiredDeviceListScenarios)
}

var RequiredDeviceMutationScenarios = []string{
	"device_forget.ok", "device_forget.gone", "device_forget.named_profile_forbidden",
	"device_clear.ok", "device_clear.keeps_device", "device_clear.unknown",
}

func DeviceMutationAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodDelete, []string{"/api/v1/devices/{device_id}", "/api/v1/devices/{device_id}/settings"}, RequiredDeviceMutationScenarios)
	if err != nil {
		return nil, err
	}
	for _, catalog := range selected {
		for _, row := range catalog.Rows {
			for _, scenario := range row.Scenarios {
				then := scenario.V2Expectation.Then
				if len(then) != 1 {
					return nil, fmt.Errorf("%s: required household read-after is missing", scenario.ID)
				}
				step := then[0]
				if step.OperationID != deviceListOperation || step.Method != http.MethodGet || step.Request.Path != deviceListPath || step.Request.Query["scope"] != "household" || step.Principal == nil || step.Principal.Class != "primary_profile" || len(step.Expect.Body) == 0 {
					return nil, fmt.Errorf("%s: required household read-after is invalid", scenario.ID)
				}
			}
		}
	}
	return selected, nil
}

func requiredListAcceptance(catalogs []*Catalog, path string, required []string) ([]*Catalog, error) {
	return requiredAcceptance(catalogs, http.MethodGet, []string{path}, required)
}

func requiredAcceptance(catalogs []*Catalog, method string, paths []string, required []string) ([]*Catalog, error) {
	want := make(map[string]bool, len(required))
	for _, id := range required {
		want[id] = false
	}
	var selected []*Catalog
	for _, c := range catalogs {
		copy := *c
		copy.Rows = nil
		for _, row := range c.Rows {
			if row.Listener != listenerAPI || row.Method != method || !slices.Contains(paths, row.Path) || row.RegistrationIndex != 0 {
				continue
			}
			picked := row
			picked.Scenarios = nil
			for _, scenario := range row.Scenarios {
				if _, ok := want[scenario.ID]; !ok {
					continue
				}
				if err := ValidatePairing(scenario.V2Expectation); err != nil {
					return nil, fmt.Errorf("%s: %w", scenario.ID, err)
				}
				if want[scenario.ID] {
					return nil, fmt.Errorf("duplicate required scenario %s", scenario.ID)
				}
				want[scenario.ID] = true
				picked.Scenarios = append(picked.Scenarios, scenario)
			}
			copy.Rows = append(copy.Rows, picked)
		}
		if len(copy.Rows) > 0 {
			selected = append(selected, &copy)
		}
	}
	for _, id := range required {
		if !want[id] {
			return nil, fmt.Errorf("required scenario %s is missing", id)
		}
	}
	return selected, nil
}

// RequiredProfileMutationScenarios fixes the update, deletion and PIN-check slice.
var RequiredProfileMutationScenarios = []string{
	"profiles_update.ok", "profiles_update.partial", "profiles_update.self_service_access_field",
	"profiles_delete.ok", "profiles_delete.meaning", "profiles_delete.primary_protected",
	"verify_pin.ok", "verify_pin.wrong",
}

func ProfileMutationAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var selected []*Catalog
	for _, group := range []struct {
		method, path string
		ids          []string
	}{
		{http.MethodPut, "/api/v1/profiles/{id}", RequiredProfileMutationScenarios[:3]},
		{http.MethodDelete, "/api/v1/profiles/{id}", RequiredProfileMutationScenarios[3:6]},
		{http.MethodPost, "/api/v1/profiles/{id}/verify-pin", RequiredProfileMutationScenarios[6:]},
	} {
		picked, err := requiredAcceptance(catalogs, group.method, []string{group.path}, group.ids)
		if err != nil {
			return nil, err
		}
		selected = append(selected, picked...)
	}
	for _, catalog := range selected {
		for _, row := range catalog.Rows {
			for _, scenario := range row.Scenarios {
				then := scenario.V2Expectation.Then
				if len(then) != 1 {
					return nil, fmt.Errorf("%s: required profile read-after is missing", scenario.ID)
				}
				step := then[0]
				principal := "primary_profile"
				if scenario.ID == "profiles_delete.primary_protected" {
					principal = "acting_admin"
				}
				if step.OperationID != "listProfiles" || step.Method != http.MethodGet || step.Request.Path != "/api/v2/profiles" || step.Principal == nil || step.Principal.Class != principal || len(step.Expect.Body) == 0 {
					return nil, fmt.Errorf("%s: required profile read-after is invalid", scenario.ID)
				}
			}
		}
	}
	return selected, nil
}
