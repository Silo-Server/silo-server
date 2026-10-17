package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type workerExecutionAuthority struct {
	pool    *pgxpool.Pool
	deny    *playback.StreamDeny
	recipes *noderecipe.Store
	content *workerContentAuthority
	service func() *workercontrolcore.Service
}

func (a workerExecutionAuthority) status(ctx context.Context, node workercontrolcore.Node, assignment workerwire.Assignment) (bool, error) {
	allowed, err := a.statuses(ctx, node, []workerwire.Assignment{assignment})
	if err != nil {
		return false, err
	}
	return allowed[0], nil
}

// Redis recipe readers retain their publication-generation checks. Limit
// concurrent readers rather than starting one goroutine per active work; SQL
// and shared request facts are consumed only after collection completes.
func (a workerExecutionAuthority) statuses(ctx context.Context, node workercontrolcore.Node, assignments []workerwire.Assignment) ([]bool, error) {
	allowed := make([]bool, len(assignments))
	cards := make([]*playback.RecipeCard, len(assignments))
	group, readCtx := errgroup.WithContext(ctx)
	group.SetLimit(8)
	for i, assignment := range assignments {
		if assignment.Path == workerPreparePath {
			continue
		}
		group.Go(func() error {
			denied, err := a.deny.DeniedStatus(readCtx, assignment.WorkID)
			if err != nil || denied {
				return err
			}
			card, found, err := a.recipes.GetStatus(readCtx, assignment.WorkID)
			if err != nil {
				return err
			}
			if found && card != nil {
				if !workeridentity.SameOrigin(card.TranscodeNodeURL, node.URL, node.AllowHTTP) {
					return nil
				}
				denied, err = a.deny.DeniedStatus(readCtx, card.SessionID)
				if err == nil && !denied {
					cards[i] = card
				}
				return err
			}
			if a.service == nil {
				return workercontrolcore.ErrUnavailable
			}
			service := a.service()
			if service == nil {
				return workercontrolcore.ErrUnavailable
			}
			allowed[i], err = service.HasInFlightStatus(readCtx, node.ID, assignment.WorkID)
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	content, err := a.content.checkRecipes(ctx, cards, make([]*streamtoken.Claims, len(cards)))
	if err != nil {
		return nil, err
	}
	for i, card := range cards {
		if card != nil {
			allowed[i] = content[i]
		}
	}
	prepared, err := workerDownloadStatuses(ctx, a.pool, node.ID, assignments)
	if err != nil {
		return nil, err
	}
	for i, assignment := range assignments {
		if assignment.Path == workerPreparePath {
			allowed[i] = prepared[i]
		}
	}
	return allowed, ctx.Err()
}
