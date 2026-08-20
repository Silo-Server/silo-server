package playback

// ErrorResponseV3 is the common non-2xx response envelope for protocol-v3
// endpoints. The machine code is stable; Message is for people and may be
// reworded without changing client behavior.
type ErrorResponseV3 struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

const ErrorClientUpgradeRequiredV3 = "client_upgrade_required"

func LegacyUpgradeErrorV3() ErrorResponseV3 {
	return ErrorResponseV3{
		Error:   ErrorClientUpgradeRequiredV3,
		Message: "This server requires playback protocol v3. Update the app to continue.",
	}
}

// ConformanceMatrixV3 is the generated cross-message corpus consumed by every
// client. These types deliberately avoid map[string]any: a misspelled expected
// field must fail to compile instead of silently becoming a new fixture key.
type ConformanceMatrixV3 struct {
	SchemaVersion int                  `json:"schema_version"`
	Planner       []PlannerScenarioV3  `json:"planner_scenarios"`
	Replans       []ReplanScenarioV3   `json:"replan_scenarios"`
	Protocol      []ProtocolScenarioV3 `json:"protocol_scenarios"`
}

type PlannerScenarioV3 struct {
	Name          string               `json:"name"`
	Category      string               `json:"category"`
	Request       StartRequestV3       `json:"request"`
	Source        SourceDescriptorV3   `json:"source"`
	AttemptedKeys []string             `json:"attempted_plan_keys,omitempty"`
	Expected      PlannerExpectationV3 `json:"expected"`
}

type PlannerExpectationV3 struct {
	Outcome            DecisionOutcomeV3    `json:"outcome"`
	Delivery           DeliveryV3           `json:"delivery,omitempty"`
	DecisionReason     string               `json:"decision_reason,omitempty"`
	PlanID             string               `json:"plan_id,omitempty"`
	PlanAttemptKey     string               `json:"plan_attempt_key,omitempty"`
	SelectedTracks     *SelectedTracksV3    `json:"selected_tracks,omitempty"`
	Subtitle           *SubtitleDecisionV3  `json:"subtitle,omitempty"`
	Claims             *ValidationClaimsV3  `json:"claims,omitempty"`
	Transformations    []TransformationV3   `json:"transformations,omitempty"`
	AvailableQualities []AvailableQualityV3 `json:"available_qualities,omitempty"`
	TerminalReason     string               `json:"terminal_reason,omitempty"`
}

type ReplanScenarioV3 struct {
	Name     string              `json:"name"`
	Category string              `json:"category"`
	Request  ReplanRequestV3     `json:"request"`
	Expected ReplanExpectationV3 `json:"expected"`
}

type ReplanExpectationV3 struct {
	HTTPStatus                  int     `json:"http_status,omitempty"`
	PositionSeconds             float64 `json:"position_seconds,omitempty"`
	PositionPreserved           bool    `json:"position_preserved,omitempty"`
	PreserveUnmodifiedTracks    bool    `json:"preserve_unmodified_tracks,omitempty"`
	SelectedQuality             string  `json:"selected_quality,omitempty"`
	SameRequestAndBodyStatus    int     `json:"same_request_and_body_status,omitempty"`
	ResponseReplayedVerbatim    bool    `json:"response_replayed_verbatim,omitempty"`
	ChangedBodyStatus           int     `json:"changed_body_status,omitempty"`
	ChangedBodyError            string  `json:"changed_body_error,omitempty"`
	WhileFirstLeaseActiveStatus int     `json:"while_first_lease_active_status,omitempty"`
	ConcurrentError             string  `json:"concurrent_error,omitempty"`
	AfterCompletionStatus       int     `json:"after_completion_status,omitempty"`
}

type ProtocolScenarioV3 struct {
	Name     string                  `json:"name"`
	Category string                  `json:"category"`
	Input    ProtocolScenarioInputV3 `json:"input"`
	Expected ProtocolExpectationV3   `json:"expected"`
}

type LegacyStartBodyV3 struct {
	ProtocolVersion    *int                       `json:"protocol_version,omitempty"`
	FileID             int                        `json:"file_id"`
	ClientCapabilities *DraftClientCapabilitiesV3 `json:"client_capabilities,omitempty"`
}

// DraftClientCapabilitiesV3 is the typed pre-finalization shape used only by
// the upgrade-required conformance vector. Its missing evidence markers are
// what distinguish draft v3 from a malformed finalized request.
type DraftClientCapabilitiesV3 struct {
	CodecsVideo []string `json:"codecs_video"`
}

type ProtocolScenarioInputV3 struct {
	StartRequest          *StartRequestV3     `json:"start_request,omitempty"`
	ReplanRequest         *ReplanRequestV3    `json:"replan_request,omitempty"`
	RouteEvent            *RouteEventV3       `json:"route_event,omitempty"`
	PersistedDecision     *DecisionResponseV3 `json:"persisted_decision,omitempty"`
	LegacyStartBody       *LegacyStartBodyV3  `json:"body,omitempty"`
	PlanID                string              `json:"plan_id,omitempty"`
	FirstOutputContextID  string              `json:"first_output_context_id,omitempty"`
	SecondOutputContextID string              `json:"second_output_context_id,omitempty"`
	FirstPlanAttemptKey   string              `json:"first_plan_attempt_key,omitempty"`
	SecondPlanAttemptKey  string              `json:"second_plan_attempt_key,omitempty"`
	ServerPlanAttemptKey  string              `json:"server_plan_attempt_key,omitempty"`
	ReplanEcho            string              `json:"replan_echo,omitempty"`
	AttemptedPlanKeys     []string            `json:"attempted_plan_keys,omitempty"`
	Restarted             bool                `json:"restarted,omitempty"`
	CapacityAvailable     *bool               `json:"capacity_available,omitempty"`
}

type ProtocolExpectationV3 struct {
	HTTPStatus               int               `json:"http_status,omitempty"`
	Error                    string            `json:"error,omitempty"`
	Outcome                  DecisionOutcomeV3 `json:"outcome,omitempty"`
	TerminalReason           string            `json:"terminal_reason,omitempty"`
	PlanIDUnchanged          bool              `json:"plan_id_unchanged,omitempty"`
	PlanAttemptKeyChanged    bool              `json:"plan_attempt_key_changed,omitempty"`
	SelectionPreserved       bool              `json:"selection_preserved,omitempty"`
	PositionPreserved        bool              `json:"position_preserved,omitempty"`
	ResponseReplayedVerbatim bool              `json:"response_replayed_verbatim,omitempty"`
	CapacityDelta            *int              `json:"capacity_delta,omitempty"`
	CleanupComplete          bool              `json:"cleanup_complete,omitempty"`
	Action                   string            `json:"action,omitempty"`
}
