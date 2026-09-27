package apiv2

import (
	"encoding/json"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/danielgtaylor/huma/v2"
)

// PolicyJSON is an administrator-authored or policy-produced JSON value. Its
// contents belong to the selected policy domain rather than the HTTP contract.
type PolicyJSON json.RawMessage

func (v PolicyJSON) MarshalJSON() ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	return v, nil
}
func (v *PolicyJSON) UnmarshalJSON(b []byte) error { *v = append((*v)[:0], b...); return nil }
func (PolicyJSON) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Description: "Arbitrary JSON input or output of the selected policy domain, including null.", Extensions: map[string]any{extExtensionBag: "policy-value"}}
}

type AdminPolicyDocument struct {
	ID              ID      `json:"id"`
	Domain          string  `json:"domain"`
	Name            string  `json:"name"`
	Enabled         bool    `json:"enabled"`
	ActiveVersionID *ID     `json:"active_version_id" nullable:"true"`
	CreatedAt       Instant `json:"created_at"`
	UpdatedAt       Instant `json:"updated_at"`
}
type AdminPolicySnapshot struct {
	AdminPolicyDocument
	ActiveVersion *AdminPolicyVersion `json:"active_version,omitempty" doc:"Present when the document has an active version."`
}
type AdminPolicyVersion struct {
	ID              ID      `json:"id"`
	DocumentID      ID      `json:"document_id"`
	VersionNumber   int     `json:"version_number"`
	SourceSHA256    string  `json:"source_sha256"`
	CompiledOK      bool    `json:"compiled_ok"`
	CompileError    *string `json:"compile_error" nullable:"true"`
	CreatedByUserID *ID     `json:"created_by_user_id" nullable:"true"`
	Comment         *string `json:"comment" nullable:"true"`
	CreatedAt       Instant `json:"created_at"`
	Source          *string `json:"source,omitempty" nullable:"false" doc:"Present on single-version reads and creation; omitted from version lists."`
}
type AdminPolicyDocumentCreate struct {
	Domain string `json:"domain" enum:"scope,permission,action"`
	Name   string `json:"name" minLength:"1" maxLength:"500"`
}
type AdminPolicyVersionCreate struct {
	Source  string `json:"source" maxLength:"262144"`
	Comment string `json:"comment,omitempty" maxLength:"10000"`
}
type AdminPolicySource struct {
	Domain string `json:"domain" enum:"scope,permission,action"`
	Source string `json:"source" maxLength:"262144"`
}
type AdminPolicySimulation struct {
	Domain string     `json:"domain" enum:"scope,permission,action"`
	Source string     `json:"source,omitempty" maxLength:"262144"`
	Input  PolicyJSON `json:"input"`
}
type AdminPolicyIDInput struct {
	ID          ID     `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminPolicyVersionInput struct {
	ID      ID `path:"id"`
	Version ID `path:"version"`
}
type AdminPolicyCreateInput struct {
	Body    AdminPolicyDocumentCreate
	RawBody []byte
}
type AdminPolicyVersionCreateInput struct {
	ID      ID `path:"id"`
	Body    AdminPolicyVersionCreate
	RawBody []byte
}
type AdminPolicyEnabled struct {
	Enabled bool `json:"enabled"`
}
type AdminPolicyActivation struct {
	VersionID ID `json:"version_id"`
}
type AdminPolicyEnabledInput struct {
	ID          ID     `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminPolicyEnabled
	RawBody     []byte
}
type AdminPolicyActivateInput struct {
	ID          ID     `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminPolicyActivation
	RawBody     []byte
}
type AdminPolicyValidateInput struct {
	Body    AdminPolicySource
	RawBody []byte
}
type AdminPolicySimulateInput struct {
	Body    AdminPolicySimulation
	RawBody []byte
}
type AdminPolicyDocumentOutput struct {
	ETag string `header:"ETag"`
	Body AdminPolicySnapshot
}
type AdminPolicyCreatedOutput struct {
	ETag     string `header:"ETag"`
	Location string `header:"Location"`
	Body     AdminPolicyDocument
}
type AdminPolicyDocumentsOutput struct {
	Body Collection[AdminPolicyDocument]
}
type AdminPolicyVersionsOutput struct {
	Body Collection[AdminPolicyVersion]
}
type AdminPolicyVersionOutput struct{ Body AdminPolicyVersion }
type AdminPolicyVersionCreatedOutput struct {
	Location string `header:"Location"`
	Body     AdminPolicyVersion
}
type AdminPolicyVendor struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}
type AdminPolicyVendorOutput struct{ Body Collection[AdminPolicyVendor] }
type AdminPolicyValidation struct {
	CompiledOK bool                  `json:"compiled_ok"`
	Errors     []policy.CompileIssue `json:"errors"`
}
type AdminPolicyValidateOutput struct{ Body AdminPolicyValidation }
type AdminPolicySimulationResult struct {
	Decision   PolicyJSON `json:"decision"`
	EvalTimeNS int64      `json:"eval_time_ns"`
	Generation int64      `json:"generation"`
}
type AdminPolicySimulateOutput struct{ Body AdminPolicySimulationResult }

// PublicationFailed reports notification failure, not cluster-wide application.
// A loaded generation can differ from the persisted generation after concurrent writes.
type AdminPolicyApplication struct {
	LocalApplied      bool  `json:"local_applied"`
	LoadedGeneration  int64 `json:"loaded_generation"`
	PublicationFailed bool  `json:"publication_failed"`
}
type AdminPolicyApplyResult struct {
	Persisted           bool                   `json:"persisted"`
	Document            AdminPolicyDocument    `json:"document"`
	PersistedGeneration int64                  `json:"persisted_generation"`
	Application         AdminPolicyApplication `json:"application"`
}
type AdminPolicyApplyOutput struct {
	ETag string `header:"ETag"`
	Body AdminPolicyApplyResult
}
type AdminPolicyDecision struct {
	ID               ID         `json:"id"`
	Timestamp        Instant    `json:"timestamp"`
	DecisionName     string     `json:"decision_name"`
	PolicyGeneration int64      `json:"policy_generation"`
	UserID           *ID        `json:"user_id" nullable:"true"`
	ProfileID        *ID        `json:"profile_id" nullable:"true"`
	SessionID        *ID        `json:"session_id" nullable:"true"`
	RequestID        *ID        `json:"request_id" nullable:"true"`
	NodeID           *ID        `json:"node_id" nullable:"true"`
	Allowed          *bool      `json:"allowed" nullable:"true"`
	EvalTimeNS       int64      `json:"eval_time_ns"`
	InputDigest      string     `json:"input_digest"`
	InputSample      PolicyJSON `json:"input_sample,omitempty"`
	ResultSample     PolicyJSON `json:"result_sample,omitempty"`
	Error            string     `json:"error,omitempty"`
}
type AdminPolicyDecisionsInput struct {
	DecisionName string `query:"decision_name"`
	UserID       ID     `query:"user_id"`
	Allowed      string `query:"allowed" enum:"true,false"`
	From         string `query:"from" format:"date-time"`
	To           string `query:"to" format:"date-time"`
	Limit        int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
	Cursor       string `query:"cursor"`
}
type AdminPolicyDecisionsOutput struct {
	Body Collection[AdminPolicyDecision]
}
type AdminPolicyDecisionOutput struct{ Body AdminPolicyDecision }

func policyID(n int64) ID { return ID(strconv.FormatInt(n, 10)) }
func adminPolicyDocumentOf(v policy.Document) AdminPolicyDocument {
	out := AdminPolicyDocument{ID: policyID(v.ID), Domain: v.Domain, Name: v.Name, Enabled: v.Enabled, CreatedAt: NewInstant(v.CreatedAt), UpdatedAt: NewInstant(v.UpdatedAt)}
	if v.ActiveVersionID != nil {
		out.ActiveVersionID = new(policyID(*v.ActiveVersionID))
	}
	return out
}
func adminPolicyVersionOf(v policy.Version, source bool) AdminPolicyVersion {
	out := AdminPolicyVersion{ID: policyID(v.ID), DocumentID: policyID(v.DocumentID), VersionNumber: v.VersionNumber, SourceSHA256: v.SourceSHA256, CompiledOK: v.CompiledOK, CompileError: v.CompileError, Comment: v.Comment, CreatedAt: NewInstant(v.CreatedAt)}
	if v.CreatedByUserID != nil {
		out.CreatedByUserID = new(ID(strconv.Itoa(*v.CreatedByUserID)))
	}
	if source {
		out.Source = new(v.RegoSource)
	}
	return out
}
func adminPolicyDecisionOf(v policy.Entry, samples bool) AdminPolicyDecision {
	out := AdminPolicyDecision{ID: policyID(v.ID), Timestamp: NewInstant(v.Timestamp), DecisionName: string(v.DecisionName), PolicyGeneration: v.PolicyGeneration, Allowed: v.Allowed, EvalTimeNS: v.EvalTimeNS, InputDigest: v.InputDigest, Error: v.Error}
	if v.UserID != nil {
		out.UserID = new(ID(strconv.Itoa(*v.UserID)))
	}
	out.ProfileID = optionalPolicyID(v.ProfileID)
	out.SessionID = optionalPolicyID(v.SessionID)
	out.RequestID = optionalPolicyID(v.RequestID)
	out.NodeID = optionalPolicyID(v.NodeID)
	if samples {
		out.InputSample = PolicyJSON(v.InputSample)
		out.ResultSample = PolicyJSON(v.ResultSample)
	}
	return out
}
func optionalPolicyID(s string) *ID {
	if s == "" {
		return nil
	}
	return new(ID(s))
}

type AdminPolicyDocumentsInput struct {
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
	Cursor string `query:"cursor"`
}
type AdminPolicyVersionsInput struct {
	ID     ID     `path:"id"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
	Cursor string `query:"cursor"`
}
