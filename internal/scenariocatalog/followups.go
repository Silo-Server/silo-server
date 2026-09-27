package scenariocatalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
)

const bindingAPIKeyPrincipal = "api_key"
const capturedIfMatch = "If-Match"
const MaxV2Exchanges = 16
const MaxResponseBindings = 8
const MaxCapturedStringBytes = 16384

var captureName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// ValidateV2Sequence also protects manually constructed executor inputs.
func ValidateV2Sequence(pair *V2Expectation) error {
	return validateV2Sequence(pair, MaxV2Exchanges)
}

func validateV2Sequence(pair *V2Expectation, limit int) error {
	if pair == nil {
		return fmt.Errorf("missing v2 expectation")
	}
	count := max(1, pair.Request.Repeat)
	if count > limit {
		return fmt.Errorf("v2 sequence exceeds %d exchanges", limit)
	}
	for i, step := range pair.Then {
		if step.Request.Body != nil && (step.Request.BodyRef != "" || step.Request.RawBody != nil || step.Request.Multipart != nil) {
			return fmt.Errorf("then[%d]: multiple body sources", i)
		}
		if err := validateOperation(step.OperationID, step.Method, step.Request.Path); err != nil {
			return fmt.Errorf("then[%d]: %w", i, err)
		}
		if step.Principal != nil && step.Principal.Class != bindingAPIKeyPrincipal && len(step.Principal.Scopes) > 0 {
			return fmt.Errorf("then[%d]: scopes require API-key principal", i)
		}
		if err := ValidateV2Bindings(step); err != nil {
			return fmt.Errorf("then[%d]: %w", i, err)
		}
		if max(1, step.Request.Repeat) > limit-count {
			return fmt.Errorf("v2 sequence exceeds %d exchanges", limit)
		}
		count += max(1, step.Request.Repeat)
	}
	return nil
}

// ValidateV2Bindings restricts query destinations to declared string parameters
// of this operation. Integer/body/path capture is outside this checkpoint.
func ValidateV2Bindings(step V2Step) error {
	if err := ValidateResponseBindings(step.Request, step.FromPrevious); err != nil {
		return err
	}
	paths, err := openAPIPaths()
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, methods := range paths {
		for _, op := range methods {
			if op.OperationID == step.OperationID {
				for _, p := range op.Parameters {
					if p.In == "query" && p.Schema.Type == "string" {
						allowed[p.Name] = true
					}
				}
			}
		}
	}
	for _, b := range step.FromPrevious {
		if b.Query != "" && !allowed[b.Query] {
			return fmt.Errorf("captured query is not a declared string parameter of %s", step.OperationID)
		}
	}
	return nil
}

func ValidateResponseBindings(request Request, bindings []ResponseBinding) error {
	if len(bindings) > MaxResponseBindings {
		return fmt.Errorf("too many response bindings")
	}
	destinations := map[string]bool{}
	for _, b := range bindings {
		if (b.Header == "") == (b.Pointer == nil) {
			return fmt.Errorf("binding needs exactly one response header or pointer")
		}
		if b.Header != "" && !captureName.MatchString(b.Header) {
			return fmt.Errorf("invalid response header name")
		}
		if b.Pointer != nil && !validCapturePointer(*b.Pointer) {
			return fmt.Errorf("invalid response JSON pointer")
		}
		if (b.RequestHeader == "") == (b.Query == "") {
			return fmt.Errorf("binding needs exactly one request header or query destination")
		}
		target := "query:" + b.Query
		if b.RequestHeader != "" {
			// Only conditional headers are needed by this checkpoint. Credentials,
			// profile selection, framing and destination authority cannot be captured.
			name := http.CanonicalHeaderKey(b.RequestHeader)
			if name != capturedIfMatch && name != "If-None-Match" {
				return fmt.Errorf("unsupported captured request header")
			}
			for k := range request.Headers {
				if strings.EqualFold(k, name) {
					return fmt.Errorf("captured header already specified")
				}
			}
			target = "header:" + name
		} else {
			if !captureName.MatchString(b.Query) || strings.EqualFold(b.Query, "token") {
				return fmt.Errorf("unsupported captured query name")
			}
			if _, ok := request.Query[b.Query]; ok {
				return fmt.Errorf("captured query already specified")
			}
			u, err := url.Parse(request.Path)
			if err != nil {
				return err
			}
			if u.Query().Has(b.Query) {
				return fmt.Errorf("captured query already in path")
			}
		}
		if destinations[target] {
			return fmt.Errorf("duplicate captured destination")
		}
		destinations[target] = true
	}
	return nil
}

func validCapturePointer(pointer string) bool {
	if pointer != "" && !strings.HasPrefix(pointer, "/") {
		return false
	}
	for i := 0; i < len(pointer); i++ {
		if pointer[i] == '~' {
			i++
			if i == len(pointer) || (pointer[i] != '0' && pointer[i] != '1') {
				return false
			}
		}
	}
	return true
}

// Frozen originals from 06dae49ec, without any v2 declarations. This allowlist
// binds the exceptional budget to the entire original, not a caller-supplied ID.
//
//go:embed sequence_originals.json
var sequenceOriginals []byte

type sequenceOriginal struct {
	Row       Row
	Scenario  Scenario
	Operation string
}

// frozenSequenceOriginals parses the embedded allowlist exactly once; callers
// only read the result, so the parsed originals are shared safely.
var frozenSequenceOriginals = sync.OnceValues(func() ([]sequenceOriginal, error) {
	var originals []sequenceOriginal
	if err := json.Unmarshal(sequenceOriginals, &originals); err != nil {
		return nil, err
	}
	return originals, nil
})

// ValidateScenarioPairing retains the default sequence budget except for the two
// exact frozen device rate-limit bursts. Call before replacing the v1 request.
func ValidateScenarioPairing(row Row, scenario Scenario) error {
	pair := scenario.V2Expectation
	if pair == nil {
		return fmt.Errorf("missing v2 expectation")
	}
	originals, err := frozenSequenceOriginals()
	if err != nil {
		return err
	}
	for _, original := range originals {
		if scenario.ID != original.Scenario.ID {
			continue
		}
		unpaired := scenario
		unpaired.V2Expectation = nil
		request := original.Scenario.Request
		request.Path = strings.Replace(request.Path, "/api/v1/", "/api/v2/", 1)
		if row.Key() != original.Row.Key() || !sameSequenceShape(unpaired, original.Scenario) ||
			pair.OperationID != original.Operation || pair.Method != original.Row.Method ||
			!sameSequenceShape(pair.Request, request) || pair.Principal != nil || len(pair.Then) != 0 || pair.Expect.Status != http.StatusTooManyRequests {
			return fmt.Errorf("%s: exceptional sequence must match the frozen original and exact v2 request", scenario.ID)
		}
		if err := validateV2Sequence(pair, original.Scenario.Request.Repeat); err != nil {
			return err
		}
		return validateOperation(pair.OperationID, pair.Method, pair.Request.Path)
	}
	return ValidatePairing(pair)
}

// Compare JSON values so raw JSON whitespace cannot change request identity.
func sameSequenceShape(a, b any) bool {
	normalize := func(value any) (any, error) {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var result any
		err = json.Unmarshal(encoded, &result)
		return result, err
	}
	left, err := normalize(a)
	if err != nil {
		return false
	}
	right, err := normalize(b)
	return err == nil && reflect.DeepEqual(left, right)
}
