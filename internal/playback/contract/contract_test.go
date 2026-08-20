// Package contract holds the conformance gate for the checked-in playback
// protocol v3 JSON Schemas.
//
// The schemas under docs/design/schemas/playback-v3 are what Android, Apple and
// web vendor to prove conformance, so they are only worth anything while they
// still describe the Go types that actually serve traffic. Nothing else forces
// that: a schema is data, and data does not fail to compile when a struct tag
// or a bound moves. These tests are the force — they compile every schema,
// validate the server-generated golden bodies against it, and pin the enums and
// required-field sets to the Go contract they were derived from.
package contract

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	schemaRootV3 = "../../../docs/design/schemas/playback-v3"
	goldenRootV3 = "../testdata/protocol_v3"
)

// fixtureSchemasV3 dispatches a fixture to its schema by filename suffix, which
// is what lets an invalid fixture carry a descriptive prefix
// ("bandwidth-below-minimum-start_request.json") without a second index to keep
// in step.
var fixtureSchemasV3 = map[string]string{
	"start_request.json":       "start-request.schema.json",
	"replan_request.json":      "replan-request.schema.json",
	"decision_response.json":   "decision-response.schema.json",
	"capability_response.json": "capability-response.schema.json",
	"error_response.json":      "error-response.schema.json",
	"route_event.json":         "route-event.schema.json",
}

// nonWireGoldenFixturesV3 are generator outputs that pin cross-message
// behavior rather than a single request or response body: opaque attempt-key
// echo scenarios and the combined-ordinal subtitle inventory. They travel with
// the wire fixtures because clients consume them, but no endpoint carries the
// has no schema. Listing them explicitly keeps the golden sweep exhaustive: a
// new wire body added to the generator without a schema fails rather than being
// skipped.
var nonWireGoldenFixturesV3 = []string{
	"attempt_keys.json",
	"conformance_matrix.json",
	"subtitle_inventory.json",
}

// Enum members the schemas publish, sourced from the Go constants that produce
// them so a value change breaks this package rather than a client.
var (
	deliveriesV3 = []string{
		string(playback.DeliveryOriginalHTTPV3),
		string(playback.DeliveryRemuxProgressiveV3),
		string(playback.DeliveryRemuxHLSV3),
		string(playback.DeliveryTranscodeHLSV3),
	}
	outcomesV3 = []string{
		string(playback.OutcomePlayableV3),
		string(playback.OutcomeAdaptationUnavailableV3),
	}
	executorsV3 = []string{playback.ExecutorClientV3, playback.ExecutorServerV3}
	evidenceV3  = []string{
		string(playback.EvidenceExactV3),
		string(playback.EvidencePlatformAttestedV3),
		string(playback.EvidenceDeclaredV3),
	}
	streamProtocolsV3 = []string{
		string(playback.StreamHTTPProgressiveV3),
		string(playback.StreamHLSV3),
	}
	headerRefreshModesV3 = []string{
		string(playback.HeaderRefreshNoneV3),
		string(playback.HeaderRefreshSessionV3),
	}
	subtitleModesV3 = []string{
		string(playback.SubtitleOffV3),
		string(playback.SubtitleRenderV3),
		string(playback.SubtitleConvertV3),
		string(playback.SubtitleBurnInV3),
	}
	subtitleFidelityV3 = []string{
		string(playback.SubtitleFidelityPreserveV3),
		string(playback.SubtitleFidelityCompatibleV3),
	}
	progressPersistenceV3 = []string{
		string(playback.ProgressPersistenceServerV3),
		string(playback.ProgressPersistenceClientV3),
	}
	subtitleSourcesV3 = []string{
		playback.SubtitleSourceExternalV3,
		playback.SubtitleSourceEmbeddedV3,
		playback.SubtitleSourceDownloadedV3,
	}
	subtitleDeliveriesV3 = []string{
		playback.SubtitleDeliverySidecarV3,
		playback.SubtitleDeliveryBurnInOnlyV3,
	}
	enhancementLayersV3 = []string{
		string(playback.EnhancementNoneV3),
		string(playback.EnhancementMELV3),
		string(playback.EnhancementFELV3),
		string(playback.EnhancementUnknownV3),
	}
	replanOperationsV3 = []string{
		string(playback.ReplanOperationFailureRecoveryV3),
		string(playback.ReplanOperationSeekReanchorV3),
		string(playback.ReplanOperationSeekFailureRecoveryV3),
		string(playback.ReplanOperationTrackChangeV3),
		string(playback.ReplanOperationQualityChangeV3),
		string(playback.ReplanOperationOutputChangeV3),
	}
	// The two operations ReplanRequestV3.Validate rejects without a failure
	// classification. The schema expresses the same rule as a conditional.
	classificationRequiredOperationsV3 = []string{
		string(playback.ReplanOperationFailureRecoveryV3),
		string(playback.ReplanOperationSeekFailureRecoveryV3),
	}
	// Written as literals because the planner writes them as literals too:
	// subtitlePolicyNameV3 in plan_v3.go, and the timeline assignments in
	// plan_v3.go and internal/api/handlers/playback_v3.go.
	subtitleFidelityPoliciesV3 = []string{"require_authored_fidelity", "allow_simplified_rendering"}
	seekRestorationsV3         = []string{"player_position", "source_position"}
)

func TestValidFixtures(t *testing.T) {
	schemas := compileSchemasV3(t)
	fixtures := mustGlob(t, filepath.Join(schemaRootV3, "v3", "fixtures", "valid", "*.json"))
	if len(fixtures) != len(fixtureSchemasV3) {
		t.Fatalf("valid fixtures = %d, want one per schema (%d)", len(fixtures), len(fixtureSchemasV3))
	}

	for _, fixture := range fixtures {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			if err := validateFixture(t, schemas, fixture); err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}

func TestClientProgressPersistenceRequiresExplicitStartPosition(t *testing.T) {
	schemas := compileSchemasV3(t)
	request := decodeJSONValue(t, mustReadFile(t, filepath.Join(goldenRootV3, "start_request.json"))).(map[string]any)
	delete(request, "start_position")
	if err := schemas["start-request.schema.json"].Validate(request); err == nil {
		t.Fatal("client progress_persistence without start_position satisfied the schema")
	}
}

func TestIntentOnlyReplansRejectFailureEvidence(t *testing.T) {
	schemas := compileSchemasV3(t)
	for _, operation := range []playback.ReplanOperationV3{
		playback.ReplanOperationTrackChangeV3,
		playback.ReplanOperationQualityChangeV3,
		playback.ReplanOperationOutputChangeV3,
	} {
		t.Run(string(operation), func(t *testing.T) {
			request := decodeJSONValue(t, mustReadFile(t, filepath.Join(goldenRootV3, "replan_request.json"))).(map[string]any)
			request["operation"] = string(operation)
			request["quality_preference"] = "720p"
			request["failure"] = map[string]any{"classification": "decoder_failure"}
			if err := schemas["replan-request.schema.json"].Validate(request); err == nil {
				t.Fatal("intent-only replan with failure satisfied the schema")
			}
		})
	}
}

// TestGoldenFixturesSatisfyTheSchemas closes the loop the schemas exist for: a
// schema that no longer accepts what cmd/playbackfixtures emits is a schema
// that describes a server nobody runs.
func TestGoldenFixturesSatisfyTheSchemas(t *testing.T) {
	schemas := compileSchemasV3(t)
	fixtures := mustGlob(t, filepath.Join(goldenRootV3, "*.json"))
	if len(fixtures) == 0 {
		t.Fatal("golden fixtures missing; run make playback-fixtures")
	}

	for _, fixture := range fixtures {
		name := filepath.Base(fixture)
		t.Run(name, func(t *testing.T) {
			if slices.Contains(nonWireGoldenFixturesV3, name) {
				t.Skip("not a wire body")
			}
			if err := validateFixture(t, schemas, fixture); err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}

// The conformance matrix embeds complete wire requests and responses inside a
// larger cross-message document. Validate those nested bodies too: decoding
// through Go structs first would normalize JSON null arrays to nil and conceal
// a corpus that strict client decoders cannot consume.
func TestConformanceMatrixEmbeddedWireBodiesSatisfySchemas(t *testing.T) {
	schemas := compileSchemasV3(t)
	matrix := decodeJSONValue(t, mustReadFile(t, filepath.Join(goldenRootV3, "conformance_matrix.json"))).(map[string]any)

	validate := func(scenarioName, schemaName string, value any) {
		t.Helper()
		if err := schemas[schemaName].Validate(value); err != nil {
			t.Errorf("scenario %q embedded %s: %v", scenarioName, schemaName, err)
		}
	}
	reject := func(scenarioName, schemaName string, value any) {
		t.Helper()
		if err := schemas[schemaName].Validate(value); err == nil {
			t.Errorf("scenario %q embedded %s is expected to be rejected", scenarioName, schemaName)
		}
	}
	for _, raw := range matrix["planner_scenarios"].([]any) {
		scenario := raw.(map[string]any)
		validate(scenario["name"].(string), "start-request.schema.json", scenario["request"])
	}
	for _, raw := range matrix["replan_scenarios"].([]any) {
		scenario := raw.(map[string]any)
		name := scenario["name"].(string)
		request := scenario["request"].(map[string]any)
		validate(name, "replan-request.schema.json", request)
		switch request["operation"] {
		case string(playback.ReplanOperationTrackChangeV3), string(playback.ReplanOperationQualityChangeV3), string(playback.ReplanOperationOutputChangeV3), string(playback.ReplanOperationSeekReanchorV3):
			if failure, ok := request["failure"]; ok {
				t.Errorf("scenario %q intent-only replan carries failure = %#v", name, failure)
			}
		}
	}
	protocolSchemas := map[string]string{
		"start_request":      "start-request.schema.json",
		"replan_request":     "replan-request.schema.json",
		"route_event":        "route-event.schema.json",
		"persisted_decision": "decision-response.schema.json",
	}
	for _, raw := range matrix["protocol_scenarios"].([]any) {
		scenario := raw.(map[string]any)
		input := scenario["input"].(map[string]any)
		expected := scenario["expected"].(map[string]any)
		expectsRejection := expected["error"] != nil && expected["http_status"].(float64) >= http.StatusBadRequest
		for field, schemaName := range protocolSchemas {
			if value, ok := input[field]; ok {
				if expectsRejection {
					reject(scenario["name"].(string), schemaName, value)
				} else {
					validate(scenario["name"].(string), schemaName, value)
				}
			}
		}
	}
}

// TestVendoredFixturesMatchGolden keeps the copies clients vendor honest. They
// are published as server output, so a hand-edit here would hand every client a
// body the server never produced.
func TestVendoredFixturesMatchGolden(t *testing.T) {
	for name := range fixtureSchemasV3 {
		t.Run(name, func(t *testing.T) {
			vendored := mustReadFile(t, filepath.Join(schemaRootV3, "v3", "fixtures", "valid", name))
			golden := mustReadFile(t, filepath.Join(goldenRootV3, name))
			if !bytes.Equal(vendored, golden) {
				t.Fatalf("vendored fixture differs from %s; run make playback-fixtures", filepath.Join(goldenRootV3, name))
			}
		})
	}
}

// TestInvalidFixturesAreRejected requires each negative fixture to fail for its
// own reason. Distinct messages are the point: the corpus is how a client
// proves its validator rejects the same bodies for the same causes, and two
// fixtures collapsing onto one error would let a whole class of violation go
// untested on every platform.
func TestInvalidFixturesAreRejected(t *testing.T) {
	schemas := compileSchemasV3(t)
	fixtures := mustGlob(t, filepath.Join(schemaRootV3, "v3", "fixtures", "invalid", "*.json"))
	// A floor rather than an exact count: adding a negative case is free,
	// while quietly deleting the corpus would leave this test passing on
	// nothing.
	if len(fixtures) < 12 {
		t.Fatalf("invalid fixtures = %d, want at least 12", len(fixtures))
	}

	seenErrors := map[string]string{}
	for _, fixture := range fixtures {
		name := filepath.Base(fixture)
		t.Run(name, func(t *testing.T) {
			err := validateFixture(t, schemas, fixture)
			if err == nil {
				t.Fatal("validate error = nil, want failure")
			}
			msg := err.Error()
			if prior := seenErrors[msg]; prior != "" {
				t.Fatalf("error %q also used by %s", msg, prior)
			}
			seenErrors[msg] = name
		})
	}
}

func TestSchemaEnumsStayInSync(t *testing.T) {
	start := mustReadObject(t, filepath.Join(schemaRootV3, "v3", "start-request.schema.json"))
	assertConstInt(t, "start.protocol_version.const", schemaValue(t, start, "properties", "protocol_version", "const"), playback.ProtocolV3)
	assertConstInt(t, "start.client_playback_context.protocol_version.const", schemaValue(t, start, "$defs", "client_playback_context", "properties", "protocol_version", "const"), playback.ProtocolV3)
	assertStringsEqual(t, "start.subtitle_fidelity_preference.enum", schemaStrings(t, start, "properties", "subtitle_fidelity_preference", "enum"), subtitleFidelityV3)
	assertStringsEqual(t, "start.progress_persistence.enum", schemaStrings(t, start, "properties", "progress_persistence", "enum"), progressPersistenceV3)
	assertStringsEqual(t, "start.capability_evidence.enum", schemaStrings(t, start, "$defs", "capability_evidence", "enum"), evidenceV3)
	assertStringsEqual(t, "start.transformation_capability.executor.enum", schemaStrings(t, start, "$defs", "transformation_capability", "properties", "executor", "enum"), executorsV3)

	replan := mustReadObject(t, filepath.Join(schemaRootV3, "v3", "replan-request.schema.json"))
	assertConstInt(t, "replan.protocol_version.const", schemaValue(t, replan, "properties", "protocol_version", "const"), playback.ProtocolV3)
	assertConstInt(t, "replan.client_playback_context.protocol_version.const", schemaValue(t, replan, "$defs", "client_playback_context", "properties", "protocol_version", "const"), playback.ProtocolV3)
	assertStringsEqual(t, "replan.operation.enum", schemaStrings(t, replan, "properties", "operation", "enum"), replanOperationsV3)
	assertStringsEqual(t, "replan.capability_evidence.enum", schemaStrings(t, replan, "$defs", "capability_evidence", "enum"), evidenceV3)
	assertStringsEqual(t, "replan.transformation_capability.executor.enum", schemaStrings(t, replan, "$defs", "transformation_capability", "properties", "executor", "enum"), executorsV3)
	assertStringsEqual(t, "replan.classification-required operations", schemaStrings(t, replan, "allOf", "0", "if", "anyOf", "1", "properties", "operation", "enum"), classificationRequiredOperationsV3)
	if got := schemaValue(t, replan, "allOf", "1", "if", "properties", "operation", "const"); got != string(playback.ReplanOperationQualityChangeV3) {
		t.Fatalf("replan quality-change conditional = %v, want %q", got, playback.ReplanOperationQualityChangeV3)
	}

	decision := mustReadObject(t, filepath.Join(schemaRootV3, "v3", "decision-response.schema.json"))
	assertConstInt(t, "decision.protocol_version.const", schemaValue(t, decision, "properties", "protocol_version", "const"), playback.ProtocolV3)
	assertConstInt(t, "decision.plan.protocol_version.const", schemaValue(t, decision, "$defs", "plan", "properties", "protocol_version", "const"), playback.ProtocolV3)
	assertStringsEqual(t, "decision.outcome.enum", schemaStrings(t, decision, "properties", "outcome", "enum"), outcomesV3)
	assertStringsEqual(t, "decision.transformation.executor.enum", schemaStrings(t, decision, "$defs", "transformation", "properties", "executor", "enum"), executorsV3)
	assertStringsEqual(t, "decision.plan.delivery.enum", schemaStrings(t, decision, "$defs", "plan", "properties", "delivery", "enum"), deliveriesV3)
	assertStringsEqual(t, "decision.plan.subtitle_fidelity_policy.enum", schemaStrings(t, decision, "$defs", "plan", "properties", "subtitle_fidelity_policy", "enum"), subtitleFidelityPoliciesV3)
	assertStringsEqual(t, "decision.stream.protocol.enum", schemaStrings(t, decision, "$defs", "stream", "properties", "protocol", "enum"), streamProtocolsV3)
	assertStringsEqual(t, "decision.stream.header_refresh.enum", schemaStrings(t, decision, "$defs", "stream", "properties", "header_refresh", "enum"), headerRefreshModesV3)
	assertStringsEqual(t, "decision.timeline.seek_restoration.enum", schemaStrings(t, decision, "$defs", "timeline", "properties", "seek_restoration", "enum"), seekRestorationsV3)
	assertStringsEqual(t, "decision.subtitle_decision.mode.enum", schemaStrings(t, decision, "$defs", "subtitle_decision", "properties", "mode", "enum"), subtitleModesV3)
	assertStringsEqual(t, "decision.subtitle_inventory_item.source.enum", schemaStrings(t, decision, "$defs", "subtitle_inventory_item", "properties", "source", "enum"), subtitleSourcesV3)
	assertStringsEqual(t, "decision.subtitle_inventory_item.delivery.enum", schemaStrings(t, decision, "$defs", "subtitle_inventory_item", "properties", "delivery", "enum"), subtitleDeliveriesV3)
	assertStringsEqual(t, "decision.source_descriptor.dv_enhancement_layer.enum", schemaStrings(t, decision, "$defs", "source_descriptor", "properties", "dv_enhancement_layer", "enum"), enhancementLayersV3)

	capability := mustReadObject(t, filepath.Join(schemaRootV3, "v3", "capability-response.schema.json"))
	if got := schemaValue(t, capability, "properties", "enabled", "const"); got != true {
		t.Fatalf("capability enabled.const = %v, want true", got)
	}
	assertStringsEqual(t, "capability.deliveries.enum", schemaStrings(t, capability, "properties", "deliveries", "items", "enum"), deliveriesV3)
	if got := schemaValue(t, capability, "$defs", "transformation", "properties", "executor", "const"); got != playback.ExecutorServerV3 {
		t.Fatalf("capability transformation executor.const = %v, want %q", got, playback.ExecutorServerV3)
	}

	routeEvent := mustReadObject(t, filepath.Join(schemaRootV3, "v3", "route-event.schema.json"))
	assertConstInt(t, "route_event.protocol_version.const", schemaValue(t, routeEvent, "properties", "protocol_version", "const"), playback.ProtocolV3)
	assertStringsEqual(t, "route_event.event.enum", schemaStrings(t, routeEvent, "properties", "event", "enum"), playback.RouteEventNamesV3())
}

// TestAdvertisedListsMatchTheGoldenFixtures covers the lists the schemas
// deliberately leave open. server_features and features carry no enum because a
// client must tolerate entries a newer server adds; the fixtures are therefore
// the only published record of what this server advertises, and clients read
// them as exactly that.
func TestAdvertisedListsMatchTheGoldenFixtures(t *testing.T) {
	var capability struct {
		ProtocolVersions []int    `json:"protocol_versions"`
		Features         []string `json:"features"`
		Deliveries       []string `json:"deliveries"`
	}
	mustUnmarshalGolden(t, "capability_response.json", &capability)
	if !slices.Equal(capability.ProtocolVersions, []int{playback.ProtocolV3}) {
		t.Fatalf("capability protocol_versions = %v, want [%d]", capability.ProtocolVersions, playback.ProtocolV3)
	}
	assertStringsEqual(t, "capability fixture features", capability.Features, playback.ServerFeaturesV3())
	assertStringsEqual(t, "capability fixture deliveries", capability.Deliveries, deliveriesV3)

	var decision struct {
		ServerFeatures []string `json:"server_features"`
	}
	mustUnmarshalGolden(t, "decision_response.json", &decision)
	assertStringsEqual(t, "decision fixture server_features", decision.ServerFeatures, playback.ServerFeaturesV3())
}

// TestResponseSchemaRequiredFieldsMatchGoTags derives the required set from the
// Go structs instead of restating it. A response field without `omitempty` is
// always on the wire, so it must be required; one with `omitempty` can vanish,
// so it must not be. That equivalence is what makes adding a field to a
// response struct a compile-clean but test-failing change until the schema
// catches up.
func TestResponseSchemaRequiredFieldsMatchGoTags(t *testing.T) {
	cases := []struct {
		schema string
		path   []string
		value  any
	}{
		{"decision-response.schema.json", nil, playback.DecisionResponseV3{}},
		{"decision-response.schema.json", []string{"$defs", "terminal"}, playback.TerminalV3{}},
		{"decision-response.schema.json", []string{"$defs", "track_identity"}, playback.TrackIdentityV3{}},
		{"decision-response.schema.json", []string{"$defs", "transformation"}, playback.TransformationV3{}},
		{"decision-response.schema.json", []string{"$defs", "plan"}, playback.PlanV3{}},
		{"decision-response.schema.json", []string{"$defs", "plan", "properties", "selected_tracks"}, playback.SelectedTracksV3{}},
		{"decision-response.schema.json", []string{"$defs", "stream"}, playback.StreamV3{}},
		{"decision-response.schema.json", []string{"$defs", "timeline"}, playback.TimelineV3{}},
		{"decision-response.schema.json", []string{"$defs", "effective_recipe"}, playback.EffectiveRecipeV3{}},
		{"decision-response.schema.json", []string{"$defs", "claims"}, playback.ValidationClaimsV3{}},
		{"decision-response.schema.json", []string{"$defs", "claims", "properties", "video"}, playback.VideoClaimsV3{}},
		{"decision-response.schema.json", []string{"$defs", "claims", "properties", "audio"}, playback.AudioClaimsV3{}},
		{"decision-response.schema.json", []string{"$defs", "claims", "properties", "subtitles"}, playback.SubtitleClaimsV3{}},
		{"decision-response.schema.json", []string{"$defs", "subtitle_decision"}, playback.SubtitleDecisionV3{}},
		{"decision-response.schema.json", []string{"$defs", "subtitle_decision", "properties", "artifact"}, playback.SubtitleArtifactV3{}},
		{"decision-response.schema.json", []string{"$defs", "subtitle_inventory_item"}, playback.SubtitleInventoryItemV3{}},
		{"decision-response.schema.json", []string{"$defs", "applied_quirk"}, playback.AppliedQuirkV3{}},
		{"decision-response.schema.json", []string{"$defs", "available_quality"}, playback.AvailableQualityV3{}},
		{"decision-response.schema.json", []string{"$defs", "degradation_warning"}, playback.DegradationWarningV3{}},
		{"decision-response.schema.json", []string{"$defs", "source_descriptor"}, playback.SourceDescriptorV3{}},
		{"capability-response.schema.json", nil, playback.CapabilityResponseV3{}},
		{"capability-response.schema.json", []string{"$defs", "transformation"}, playback.TransformationV3{}},
		{"error-response.schema.json", nil, playback.ErrorResponseV3{}},
	}

	for _, tc := range cases {
		label := tc.schema
		if len(tc.path) > 0 {
			label += ":" + strings.Join(tc.path, ".")
		}
		t.Run(label, func(t *testing.T) {
			node := schemaValue(t, mustReadObject(t, filepath.Join(schemaRootV3, "v3", tc.schema)), tc.path...)
			assertStringsEqual(t, label+".required", optionalSchemaStrings(t, node, "required"), alwaysSerializedFields(t, tc.value))
		})
	}
}

func TestResponseSchemasEnforcePublishedInvariants(t *testing.T) {
	schemas := compileSchemasV3(t)

	capability := decodeJSONValue(t, mustReadFile(t, filepath.Join(goldenRootV3, "capability_response.json"))).(map[string]any)
	capability["protocol_versions"] = []any{}
	if err := schemas["capability-response.schema.json"].Validate(capability); err == nil {
		t.Fatal("capability schema accepted a response that omitted protocol v3")
	}

	capability = decodeJSONValue(t, mustReadFile(t, filepath.Join(goldenRootV3, "capability_response.json"))).(map[string]any)
	features := capability["features"].([]any)
	capability["features"] = features[:len(features)-1]
	if err := schemas["capability-response.schema.json"].Validate(capability); err == nil {
		t.Fatal("capability schema accepted a response that omitted a baseline feature")
	}

	capability = decodeJSONValue(t, mustReadFile(t, filepath.Join(goldenRootV3, "capability_response.json"))).(map[string]any)
	capability["deliveries"] = []any{"original_http"}
	if err := schemas["capability-response.schema.json"].Validate(capability); err == nil {
		t.Fatal("capability schema accepted a partial delivery registry")
	}

	capability = decodeJSONValue(t, mustReadFile(t, filepath.Join(goldenRootV3, "capability_response.json"))).(map[string]any)
	transformations := capability["transformations"].([]any)
	transformations[0].(map[string]any)["executor"] = "client"
	if err := schemas["capability-response.schema.json"].Validate(capability); err == nil {
		t.Fatal("capability schema accepted a client-executed server transformation")
	}

	decision := decodeJSONValue(t, mustReadFile(t, filepath.Join(goldenRootV3, "decision_response.json"))).(map[string]any)
	plan := decision["playback_plan"].(map[string]any)
	plan["plan_id"] = "plan:opaque-but-not-the-server-shape"
	if err := schemas["decision-response.schema.json"].Validate(decision); err == nil {
		t.Fatal("decision schema accepted a malformed server plan identity")
	}

	decision = decodeJSONValue(t, mustReadFile(t, filepath.Join(goldenRootV3, "decision_response.json"))).(map[string]any)
	plan = decision["playback_plan"].(map[string]any)
	plan["available_qualities"] = []any{}
	if err := schemas["decision-response.schema.json"].Validate(decision); err == nil {
		t.Fatal("decision schema accepted a playable plan with no source quality rung")
	}
}

// TestRequestSchemaRequiredFieldsMatchValidators restates the required sets the
// request validators enforce. They cannot be derived from struct tags the way
// response sets can: a request field is required because a validator rejects
// its absence, not because Go would marshal it.
func TestRequestSchemaRequiredFieldsMatchValidators(t *testing.T) {
	start := mustReadObject(t, filepath.Join(schemaRootV3, "v3", "start-request.schema.json"))
	assertStringsEqual(t, "start.required", schemaStrings(t, start, "required"), []string{
		"protocol_version",
		"file_id",
		"profile_id",
		"playback_attempt_id",
		"subtitle_fidelity_preference",
		"client_capabilities",
		"client_playback_context",
	})
	assertStringsEqual(t, "start.client_capabilities.required", schemaStrings(t, start, "$defs", "client_capabilities", "required"), []string{"video_evidence", "audio_evidence"})
	assertStringsEqual(t, "start.client_playback_context.required", schemaStrings(t, start, "$defs", "client_playback_context", "required"), []string{"protocol_version"})

	replan := mustReadObject(t, filepath.Join(schemaRootV3, "v3", "replan-request.schema.json"))
	assertStringsEqual(t, "replan.required", schemaStrings(t, replan, "required"), []string{
		"protocol_version",
		"playback_attempt_id",
		"replan_request_id",
		"failed_plan_id",
		"plan_attempt_id",
		"plan_attempt_key",
		"attempt_count",
		"client_capabilities",
		"client_playback_context",
	})
	assertStringsEqual(t, "replan.client_capabilities.required", schemaStrings(t, replan, "$defs", "client_capabilities", "required"), []string{"video_evidence", "audio_evidence"})

	routeEvent := mustReadObject(t, filepath.Join(schemaRootV3, "v3", "route-event.schema.json"))
	assertStringsEqual(t, "route_event.required", schemaStrings(t, routeEvent, "required"), []string{"protocol_version", "playback_attempt_id", "event"})
}

// TestRequestSchemasShareIdenticalDefs pins the one thing two copies of a
// definition can never be trusted to keep on their own. Start and replan
// deserialize the *same* Go types — ClientCodecCapabilitiesV3 and
// ClientPlaybackContextV3 — through the same validator, so any $def they both
// declare must be byte-identical. Adding a field to one schema and not the
// other compiles, validates, and ships a contract that lies about one of the
// two endpoints; this is what catches it.
func TestRequestSchemasShareIdenticalDefs(t *testing.T) {
	start := schemaValue(t, mustReadObject(t, filepath.Join(schemaRootV3, "v3", "start-request.schema.json")), "$defs").(map[string]any)
	replan := schemaValue(t, mustReadObject(t, filepath.Join(schemaRootV3, "v3", "replan-request.schema.json")), "$defs").(map[string]any)

	shared := make([]string, 0, len(start))
	for name := range start {
		if _, ok := replan[name]; ok {
			shared = append(shared, name)
		}
	}
	sort.Strings(shared)
	if !slices.Contains(shared, "client_playback_context") || !slices.Contains(shared, "client_capabilities") {
		t.Fatalf("shared $defs = %v, want the client contract definitions in both request schemas", shared)
	}
	for _, name := range shared {
		if !reflect.DeepEqual(start[name], replan[name]) {
			t.Errorf("$defs.%s differs between start-request and replan-request; both deserialize the same Go type", name)
		}
	}
}

func compileSchemasV3(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()

	paths := mustGlob(t, filepath.Join(schemaRootV3, "v3", "*.schema.json"))
	if len(paths) != len(fixtureSchemasV3) {
		t.Fatalf("schemas = %d, want %d", len(paths), len(fixtureSchemasV3))
	}

	compiled := make(map[string]*jsonschema.Schema, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		compiler := jsonschema.NewCompiler()
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(mustReadFile(t, path)))
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if err := compiler.AddResource(name, doc); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		schema, err := compiler.Compile(name)
		if err != nil {
			t.Fatalf("compile %s: %v", name, err)
		}
		compiled[name] = schema
	}
	return compiled
}

func validateFixture(t *testing.T, schemas map[string]*jsonschema.Schema, path string) error {
	t.Helper()

	name := filepath.Base(path)
	schemaName := ""
	for suffix, candidate := range fixtureSchemasV3 {
		if strings.HasSuffix(name, suffix) {
			schemaName = candidate
			break
		}
	}
	if schemaName == "" {
		t.Fatalf("no schema dispatches %s", name)
	}
	schema, ok := schemas[schemaName]
	if !ok {
		t.Fatalf("schema %s was not compiled", schemaName)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(mustReadFile(t, path)))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return schema.Validate(instance)
}

// alwaysSerializedFields returns the JSON names encoding/json always writes for
// value: every tagged field without `omitempty`.
func alwaysSerializedFields(t *testing.T, value any) []string {
	t.Helper()

	typ := reflect.TypeOf(value)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%T is not a struct", value)
	}
	names := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		parts := strings.Split(field.Tag.Get("json"), ",")
		if parts[0] == "-" || parts[0] == "" {
			t.Fatalf("%s.%s has no json name", typ.Name(), field.Name)
		}
		if slices.Contains(parts[1:], "omitempty") {
			continue
		}
		names = append(names, parts[0])
	}
	return names
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	sort.Strings(matches)
	return matches
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func decodeJSONValue(t *testing.T, body []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func mustReadObject(t *testing.T, path string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(mustReadFile(t, path), &obj); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return obj
}

func mustUnmarshalGolden(t *testing.T, name string, target any) {
	t.Helper()
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(goldenRootV3, name)), target); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
}

func schemaStrings(t *testing.T, root any, path ...string) []string {
	t.Helper()
	value := schemaValue(t, root, path...)
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s is %T, want array", strings.Join(path, "."), value)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("%s item is %T, want string", strings.Join(path, "."), item)
		}
		out = append(out, s)
	}
	return out
}

// optionalSchemaStrings reads a string array that may be absent, so a schema
// object with no required fields compares as an empty set rather than failing
// the lookup.
func optionalSchemaStrings(t *testing.T, root any, key string) []string {
	t.Helper()
	obj, ok := root.(map[string]any)
	if !ok {
		t.Fatalf("schema node is %T, want object", root)
	}
	if _, present := obj[key]; !present {
		return nil
	}
	return schemaStrings(t, root, key)
}

// schemaValue walks a schema document by literal keys. Array positions are
// addressed by their decimal index ("allOf", "0"), which keeps conditional
// subschemas reachable without a second traversal shape.
func schemaValue(t *testing.T, root any, path ...string) any {
	t.Helper()
	current := root
	for i, key := range path {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[key]
			if !ok {
				t.Fatalf("missing schema path %s", strings.Join(path[:i+1], "."))
			}
			current = next
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(node) {
				t.Fatalf("schema path %s does not index an array of %d", strings.Join(path[:i+1], "."), len(node))
			}
			current = node[index]
		default:
			t.Fatalf("schema path %s traverses a %T", strings.Join(path[:i+1], "."), current)
		}
	}
	return current
}

func assertConstInt(t *testing.T, label string, got any, want int) {
	t.Helper()
	gotFloat, ok := got.(float64)
	if !ok {
		t.Fatalf("%s = %T, want JSON number", label, got)
	}
	if int(gotFloat) != want || gotFloat != float64(want) {
		t.Fatalf("%s = %v, want %d", label, got, want)
	}
}

func assertStringsEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s mismatch\ngot:  %v\nwant: %v", label, got, want)
	}
}
