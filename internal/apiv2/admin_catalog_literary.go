package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/literaryworks"
)

// AdminLiteraryWorkService is the existing literary administration domain service.
type AdminLiteraryWorkService interface {
	ListCandidates(context.Context, string, int) ([]literaryworks.Candidate, error)
	LinkItems(context.Context, string, []string) (string, error)
	UnlinkItem(context.Context, string, string) error
	ConfirmMatch(context.Context, string, string, int) (string, error)
	IgnoreMatch(context.Context, string, string, int) error
}

type AdminLiteraryCandidatesInput struct {
	ContentID string `path:"content_id" minLength:"1" maxLength:"512"`
	Limit     int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
}
type AdminLiteraryCandidate struct {
	SourceContentID string            `json:"source_content_id"`
	TargetContentID string            `json:"target_content_id"`
	TargetWorkID    string            `json:"target_work_id,omitempty"`
	Score           float64           `json:"score"`
	LinkSource      string            `json:"link_source"`
	Evidence        map[string]string `json:"evidence" doc:"Scoring evidence keyed by signal name; empty, never null."`
}
type AdminLiteraryCandidates struct {
	Candidates []AdminLiteraryCandidate `json:"candidates" doc:"Top ranked candidates, bounded by limit; empty, never null."`
}
type AdminLiteraryCandidatesOutput struct{ Body AdminLiteraryCandidates }
type AdminLiteraryLinkInput struct {
	Body struct {
		WorkID     string   `json:"work_id,omitempty" maxLength:"512" doc:"Existing work; omit to reuse a linked work or create one."`
		ContentIDs []string `json:"content_ids" minItems:"1" maxItems:"100"`
	}
}
type AdminLiteraryLink struct {
	WorkID string `json:"work_id"`
}
type AdminLiteraryLinkOutput struct{ Body AdminLiteraryLink }
type AdminLiteraryDecisionInput struct {
	Body struct {
		SourceContentID string `json:"source_content_id" minLength:"1" maxLength:"512"`
		TargetContentID string `json:"target_content_id" minLength:"1" maxLength:"512"`
	}
}
type AdminLiteraryDecision struct {
	Status string `json:"status" enum:"ok"`
	WorkID string `json:"work_id,omitempty"`
}
type AdminLiteraryDecisionOutput struct{ Body AdminLiteraryDecision }
type AdminLiteraryUnlinkInput struct {
	WorkID    string `path:"work_id" minLength:"1" maxLength:"512"`
	ContentID string `path:"content_id" minLength:"1" maxLength:"512"`
}

func registerAdminCatalogLiterary(reg *Registry) {
	op := func(method, path, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/literary-works"+path, id, "admin-catalog", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: isMutatingMethod(method)}
		o.MaxBodyBytes = 1 << 20
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		if method == http.MethodDelete {
			o.DefaultStatus = http.StatusNoContent
		}
		return o
	}
	Register(reg, op(http.MethodGet, "/items/{content_id}/candidates", "listAdminLiteraryCandidates", "Read bounded ranked literary match candidates."), reg.listAdminLiteraryCandidates)
	Register(reg, op(http.MethodPost, "/link", "linkAdminLiteraryItems", "Link literary editions using the existing catalog service."), reg.linkAdminLiteraryItems)
	Register(reg, op(http.MethodPost, "/matches/confirm", "confirmAdminLiteraryMatch", "Link editions then record an administrator match decision."), reg.confirmAdminLiteraryMatch)
	Register(reg, op(http.MethodPost, "/matches/ignore", "ignoreAdminLiteraryMatch", "Record an administrator decision to ignore a literary match."), reg.ignoreAdminLiteraryMatch)
	Register(reg, op(http.MethodDelete, "/{work_id}/items/{content_id}", "unlinkAdminLiteraryItem", "Remove an edition from a literary work."), reg.unlinkAdminLiteraryItem)
}
func adminLiteraryProblem(err error) *Problem {
	if errors.Is(err, literaryworks.ErrWorkNotFound) {
		return NewProblem(TypeNotFound, "Literary item or work not found.")
	}
	return serviceProblem(err)
}
func (reg *Registry) listAdminLiteraryCandidates(ctx context.Context, in *AdminLiteraryCandidatesInput) (*AdminLiteraryCandidatesOutput, error) {
	if reg.deps.AdminLiteraryWorks == nil {
		return nil, unavailable("literary administration")
	}
	id := strings.TrimSpace(in.ContentID)
	if id == "" {
		return nil, NewProblem(TypeValidationFailed, "content_id is required.")
	}
	candidates, err := reg.deps.AdminLiteraryWorks.ListCandidates(ctx, id, in.Limit)
	if err != nil {
		return nil, adminLiteraryProblem(err)
	}
	out := &AdminLiteraryCandidatesOutput{}
	out.Body.Candidates = make([]AdminLiteraryCandidate, 0, len(candidates))
	for _, c := range candidates {
		evidence := c.Evidence
		if evidence == nil {
			evidence = map[string]string{}
		}
		out.Body.Candidates = append(out.Body.Candidates, AdminLiteraryCandidate{SourceContentID: c.SourceContentID, TargetContentID: c.TargetContentID, TargetWorkID: c.TargetWorkID, Score: c.Score, LinkSource: c.LinkSource, Evidence: evidence})
	}
	return out, nil
}
func (reg *Registry) linkAdminLiteraryItems(ctx context.Context, in *AdminLiteraryLinkInput) (*AdminLiteraryLinkOutput, error) {
	if reg.deps.AdminLiteraryWorks == nil {
		return nil, unavailable("literary administration")
	}
	for i, id := range in.Body.ContentIDs {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 512 {
			return nil, NewProblem(TypeValidationFailed, "content_ids must contain nonempty identifiers of at most 512 bytes.")
		}
		in.Body.ContentIDs[i] = id
	}
	id, err := reg.deps.AdminLiteraryWorks.LinkItems(ctx, strings.TrimSpace(in.Body.WorkID), in.Body.ContentIDs)
	if err != nil {
		return nil, adminLiteraryProblem(err)
	}
	out := &AdminLiteraryLinkOutput{}
	out.Body.WorkID = id
	return out, nil
}
func (reg *Registry) confirmAdminLiteraryMatch(ctx context.Context, in *AdminLiteraryDecisionInput) (*AdminLiteraryDecisionOutput, error) {
	return reg.decideAdminLiteraryMatch(ctx, in, true)
}
func (reg *Registry) ignoreAdminLiteraryMatch(ctx context.Context, in *AdminLiteraryDecisionInput) (*AdminLiteraryDecisionOutput, error) {
	return reg.decideAdminLiteraryMatch(ctx, in, false)
}
func (reg *Registry) decideAdminLiteraryMatch(ctx context.Context, in *AdminLiteraryDecisionInput, confirm bool) (*AdminLiteraryDecisionOutput, error) {
	if reg.deps.AdminLiteraryWorks == nil {
		return nil, unavailable("literary administration")
	}
	source, target := strings.TrimSpace(in.Body.SourceContentID), strings.TrimSpace(in.Body.TargetContentID)
	if source == "" || target == "" || source == target {
		return nil, NewProblem(TypeValidationFailed, "Distinct source_content_id and target_content_id are required.")
	}
	out := &AdminLiteraryDecisionOutput{}
	var err error
	if confirm {
		out.Body.WorkID, err = reg.deps.AdminLiteraryWorks.ConfirmMatch(ctx, source, target, claimsFrom(ctx).UserID)
	} else {
		err = reg.deps.AdminLiteraryWorks.IgnoreMatch(ctx, source, target, claimsFrom(ctx).UserID)
	}
	if err != nil {
		return nil, adminLiteraryProblem(err)
	}
	out.Body.Status = "ok"
	return out, nil
}
func (reg *Registry) unlinkAdminLiteraryItem(ctx context.Context, in *AdminLiteraryUnlinkInput) (*struct{}, error) {
	if reg.deps.AdminLiteraryWorks == nil {
		return nil, unavailable("literary administration")
	}
	work, content := strings.TrimSpace(in.WorkID), strings.TrimSpace(in.ContentID)
	if work == "" || content == "" {
		return nil, NewProblem(TypeValidationFailed, "work_id and content_id are required.")
	}
	if err := reg.deps.AdminLiteraryWorks.UnlinkItem(ctx, work, content); err != nil {
		return nil, adminLiteraryProblem(err)
	}
	return nil, nil
}
