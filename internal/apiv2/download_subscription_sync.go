package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

type DownloadSubscriptionSyncService interface {
	SyncSubscriptionPage(context.Context, int, string, string, string, *catalogpkg.EpisodePagePosition, int, catalogpkg.AccessFilter, func(*downloads.Subscription) error) (downloads.SubscriptionSyncPage, error)
}
type DownloadSubscriptionSyncInput struct {
	DeviceID string `header:"X-Silo-Device-Id" required:"true" minLength:"1" maxLength:"128"`
	Limit    int    `query:"limit" default:"50" minimum:"1" maximum:"100"`
	Cursor   string `query:"cursor"`
	Body     struct {
		SubscriptionID string `json:"subscription_id" minLength:"1" maxLength:"128"`
		ETag           string `json:"etag" minLength:"1" maxLength:"128" doc:"Monitor validator captured before sync; retain it and the cursor on retry."`
	}
}
type DownloadSubscriptionSync struct {
	SubscriptionID ID       `json:"subscription_id"`
	Registered     int      `json:"registered" doc:"Newly registered episodes on this request; a repeated page may report zero."`
	Examined       int      `json:"examined" doc:"Examined catalog episodes, including out-of-scope or unavailable-file entries."`
	Page           PageInfo `json:"page"`
}
type DownloadSubscriptionSyncOutput struct{ Body DownloadSubscriptionSync }

func registerDownloadSubscriptionSync(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/downloads/subscriptions/sync", "syncDownloadSubscription", "downloads", "Register one bounded episode page for a current device monitor. Continue every page, even if no episodes were registered."), Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.MaxBodyBytes = 4096
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *DownloadSubscriptionSyncInput) (*DownloadSubscriptionSyncOutput, error) {
		return reg.syncDownloadSubscription(ctx, cursors, in)
	})
}
func (reg *Registry) syncDownloadSubscription(ctx context.Context, cursors *Cursors, in *DownloadSubscriptionSyncInput) (*DownloadSubscriptionSyncOutput, error) {
	if reg.deps.DownloadSubscriptionSync == nil {
		return nil, unavailable("bounded download subscription sync")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	filter, _ := json.Marshal([]string{in.DeviceID, in.Body.SubscriptionID, in.Body.ETag})
	scope := CursorScope{OperationID: "syncDownloadSubscription", Security: strconv.Itoa(user) + "/" + profile + "/" + viewerScopeDigest(ctx), Filter: string(filter), Sort: "season,episode,id", Tiebreaker: "id"}
	var after *catalogpkg.EpisodePagePosition
	if in.Cursor != "" {
		after = &catalogpkg.EpisodePagePosition{}
		if p := cursors.Decode(scope, in.Cursor, after); p != nil {
			return nil, p
		}
	}
	page, err := reg.deps.DownloadSubscriptionSync.SyncSubscriptionPage(ctx, user, profile, in.DeviceID, in.Body.SubscriptionID, after, in.Limit, handlers.AccessFilterFromContext(ctx, ""), func(row *downloads.Subscription) error {
		if downloadSubscriptionOf(row).ETag != in.Body.ETag {
			return NewProblem(TypeConflict, "The monitor changed; reload it before starting a new sync.")
		}
		return nil
	})
	if err != nil {
		return nil, subscriptionMutationProblem(err)
	}
	out := DownloadSubscriptionSync{SubscriptionID: ID(in.Body.SubscriptionID), Registered: page.Registered, Examined: page.Examined}
	if page.Next != nil {
		next, err := cursors.Encode(scope, *page.Next)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out.Page = PageInfo{HasMore: true, NextCursor: next}
	}
	return &DownloadSubscriptionSyncOutput{Body: out}, nil
}
