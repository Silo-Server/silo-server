package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// AdminNodeKeysService issues transcode node keys.
type AdminNodeKeysService interface {
	IssueAdminNodeKey(ctx context.Context, id int) (string, error)
}

type AdminNodeKey struct {
	NodeKey string `json:"node_key" doc:"The transcode node's new key, for its SILO_NODE_KEY setting. Shown only in this response; it is never returned again."`
}

// adminNodeKeyCacheControl keeps the one response that carries a node key out
// of every cache.
const adminNodeKeyCacheControl = "no-store"

type AdminNodeKeyOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         AdminNodeKey
}

func registerAdminNodeKey(reg *Registry) {
	op := Operation{Operation: humaOp("POST", Prefix+"/admin/nodes/{id}/key", "issueAdminNodeKey", "admin-nodes", "Issue a new key for a transcode node and return it once. Each call rotates: the previous key stops working on this server at once and on other servers and proxies within seconds. A disabled node can take a key."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	op.DefaultStatus = http.StatusCreated
	// A proxy node signs with the shared secret and takes no key.
	op.Errors = []int{http.StatusConflict}
	Register(reg, op, func(ctx context.Context, in *AdminNodeCommandInput) (*AdminNodeKeyOutput, error) {
		id, err := adminNodeCommandID(in)
		if err != nil {
			return nil, err
		}
		if reg.deps.AdminNodeKeys == nil {
			return nil, unavailable("administrator nodes")
		}
		key, err := reg.deps.AdminNodeKeys.IssueAdminNodeKey(ctx, id)
		if errors.Is(err, nodepool.ErrNodeKeyNotIssuable) {
			return nil, NewProblem(TypeConflict, "Only transcode nodes take a node key.")
		}
		if err != nil {
			return nil, adminNodeCommandProblem(err)
		}
		return &AdminNodeKeyOutput{CacheControl: adminNodeKeyCacheControl, Body: AdminNodeKey{NodeKey: key}}, nil
	})
}
