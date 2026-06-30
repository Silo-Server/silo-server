package handlers

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/auth"
)

var errACLActionDenied = errors.New("acl action denied")

func authorizeACLAction(ctx context.Context, authorizer auth.Authorizer, request auth.AccessRequest) error {
	if authorizer == nil {
		return nil
	}
	decision, err := authorizer.Authorize(ctx, request)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return errACLActionDenied
	}
	return nil
}
