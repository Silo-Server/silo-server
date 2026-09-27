package apiv2

import (
	"context"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type AdminAutoscanRewritesService interface {
	ReadAdminAutoscanRewriteSuggestions(context.Context, string) (autoscan.RewriteSuggestions, error)
}
type AdminAutoscanRewriteSuggestionsInput struct {
	ID string `path:"id" minLength:"1" maxLength:"256"`
}
type AdminAutoscanProposedRewrite struct {
	From       string `json:"from"`
	To         string `json:"to"`
	MatchDepth int    `json:"match_depth"`
}
type AdminAutoscanAmbiguousRoot struct {
	Root       string   `json:"root"`
	Candidates []string `json:"candidates"`
}
type AdminAutoscanRewriteSuggestions struct {
	Proposed  []AdminAutoscanProposedRewrite `json:"proposed"`
	Unmatched []string                       `json:"unmatched"`
	Ambiguous []AdminAutoscanAmbiguousRoot   `json:"ambiguous"`
	Covered   []string                       `json:"covered"`
}
type AdminAutoscanRewriteSuggestionsOutput struct {
	Body AdminAutoscanRewriteSuggestions
}

func registerAdminAutoscanRewrites(reg *Registry) {
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/autoscan/sources/{id}/rewrite-suggestions", "getAdminAutoscanRewriteSuggestions", "admin-autoscan", "Synchronously compare the bound provider's root folders with library paths. Returns a preview only; no source write or durable job."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanRewriteSuggestionsInput) (*AdminAutoscanRewriteSuggestionsOutput, error) {
		if reg.deps.AdminAutoscanRewrites == nil {
			return nil, unavailable("autoscan rewrite suggestions")
		}
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return nil, NewProblem(TypeValidationFailed, "A source ID is required.")
		}
		value, err := reg.deps.AdminAutoscanRewrites.ReadAdminAutoscanRewriteSuggestions(ctx, id)
		if err != nil {
			switch {
			case errors.Is(err, autoscan.ErrNotFound):
				return nil, NewProblem(TypeNotFound, "Source or bound connection not found.")
			case errors.Is(err, autoscan.ErrNoConnection):
				return nil, NewProblem(TypeValidationFailed, "Bind a connection before requesting rewrite suggestions.")
			default:
				return nil, NewProblem(TypeInternalError, "Could not read rewrite suggestions.")
			}
		}
		out := AdminAutoscanRewriteSuggestions{Proposed: make([]AdminAutoscanProposedRewrite, 0, len(value.Proposed)), Unmatched: append([]string{}, value.Unmatched...), Ambiguous: make([]AdminAutoscanAmbiguousRoot, 0, len(value.Ambiguous)), Covered: append([]string{}, value.Covered...)}
		for _, p := range value.Proposed {
			out.Proposed = append(out.Proposed, AdminAutoscanProposedRewrite{p.From, p.To, p.MatchDepth})
		}
		for _, a := range value.Ambiguous {
			out.Ambiguous = append(out.Ambiguous, AdminAutoscanAmbiguousRoot{a.Root, append([]string{}, a.Candidates...)})
		}
		return &AdminAutoscanRewriteSuggestionsOutput{Body: out}, nil
	})
}
