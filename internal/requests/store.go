package requests

import (
	"context"
	"time"
)

type Store interface {
	GetSettings(ctx context.Context) (Settings, error)
	UpdateSettings(ctx context.Context, settings Settings) (Settings, error)
	GetUserLimit(ctx context.Context, userID int) (*UserLimit, error)
	UpsertUserLimit(ctx context.Context, limit UserLimit) (*UserLimit, error)
	CountUserRequestsSince(ctx context.Context, userID int, since time.Time) (int, error)
	ListActiveByTMDB(ctx context.Context, mediaType MediaType, tmdbIDs []int) (map[int]*Request, error)
	CreateRequest(ctx context.Context, input CreateRequestRecord) (*Request, error)
	GetRequest(ctx context.Context, id string) (*Request, error)
	// ListReconciliationCandidates returns in-flight requests (approved,
	// queued, downloading); ListLibraryWaitCandidates returns the pending and
	// recently failed ones only the library can complete. Both rotate by
	// last_reconciled_at.
	ListReconciliationCandidates(ctx context.Context, limit int) ([]*Request, error)
	ListLibraryWaitCandidates(ctx context.Context, limit int) ([]*Request, error)
	// ListDownloadingRequests returns active requests with a downloading
	// target that has download progress, for the download refresh pass, the
	// least recently asked about first. Targets without progress are ignored.
	ListDownloadingRequests(ctx context.Context, limit int) ([]*Request, error)
	// ListFulfilledUnnotified returns completed requests whose fulfillment
	// notification has not fired yet (presence-gated notify pass).
	ListFulfilledUnnotified(ctx context.Context, limit int) ([]*Request, error)
	// MarkFulfilledNotified stamps a request's fulfillment-notification
	// marker so the notify pass stops considering it. Idempotent; stamped
	// reports whether this call set it.
	MarkFulfilledNotified(ctx context.Context, id string) (stamped bool, err error)
	ListMine(ctx context.Context, userID int, filter ListFilter) ([]*Request, error)
	ListAdmin(ctx context.Context, filter ListFilter) ([]*Request, error)
	CountAdminViews(ctx context.Context) (AdminViewCounts, error)
	ListEvents(ctx context.Context, requestID string, limit int) ([]RequestEvent, error)
	// SetStatus and SetOutcome apply a transition only while the request is in
	// a state the guard accepts; otherwise they return ErrInvalidState.
	// Declining or withdrawing also clears the title's follows in the same
	// transaction.
	SetStatus(ctx context.Context, id string, from StateGuard, status Status, actor Viewer) (*Request, error)
	SetOutcome(ctx context.Context, id string, from StateGuard, outcome Outcome, actor Viewer, message string) (*Request, error)
	// ReopenFailed moves a failed request back to active + approved with a
	// fresh submission budget, in one guarded write.
	ReopenFailed(ctx context.Context, id string, actor Viewer) (*Request, error)
	// ClaimSubmission takes the right to submit an approved request: it
	// succeeds for one caller at a time, only while the request is active,
	// approved, not leased, and past its backoff, and it counts the attempt.
	// The claim holds for lease. claimed is false when another caller holds it
	// or the backoff has not elapsed.
	ClaimSubmission(ctx context.Context, id string, lease time.Duration) (req *Request, claimed bool, err error)
	// DeferSubmission records a failed submission attempt on a still-approved
	// request, releases the claim, and schedules the next attempt after delay.
	// leaseUntil is the claim's SubmitLeaseUntil: a caller whose lease expired
	// and was claimed again gets ErrInvalidState and leaves the newer claim
	// alone.
	DeferSubmission(ctx context.Context, id string, leaseUntil time.Time, delay time.Duration, message string) (*Request, error)
	// FailSubmission marks a still-approved request failed after its last
	// submission attempt and releases the claim. It is fenced on leaseUntil
	// the same way as DeferSubmission, so an attempt that outlived its lease
	// cannot fail a newer claim's attempt.
	FailSubmission(ctx context.Context, id string, leaseUntil time.Time, actor Viewer, message string) (*Request, error)
	// RecordSubmission stores the targets a claimed submission created,
	// releases the claim, and re-derives the request's status from its
	// targets, in one transaction. It is fenced on leaseUntil the same way as
	// DeferSubmission: an attempt that outlived its lease while the request
	// was withdrawn, completed or claimed again gets ErrInvalidState and
	// writes nothing.
	RecordSubmission(ctx context.Context, id string, leaseUntil time.Time, targets []Target, actor Viewer) (*Request, error)
	// MarkReconciled stamps last_reconciled_at so the reconcile pass rotates
	// through every candidate.
	MarkReconciled(ctx context.Context, id string) error
	// MarkAvailable completes a request whose media is already in the library:
	// a pending one (no approval needed once the title exists), an in-flight
	// one, or a failed one none of whose targets completed. It refuses
	// (ErrInvalidState) a request another actor has closed, and an approved
	// request whose submission claim is live, so it cannot race a router call
	// that is creating targets.
	MarkAvailable(ctx context.Context, id string, actor Viewer) (*Request, error)
	// RecomputeStatus re-derives an approved request's status and outcome from
	// its targets, for a submission that found nothing left to send.
	RecomputeStatus(ctx context.Context, id string, actor Viewer) (*Request, error)
	// FollowTitle, UnfollowTitle and FollowedRequests manage a profile's
	// follows, keyed by account, profile and request; ListRequestFollowers and
	// ClearRequestFollowers serve a request's fulfilled notification. All are
	// idempotent. FollowTitle answers ErrNotRequested when the title has no
	// open request.
	FollowTitle(ctx context.Context, mediaType MediaType, tmdbID int, viewer Viewer) error
	UnfollowTitle(ctx context.Context, mediaType MediaType, tmdbID int, viewer Viewer) error
	FollowedRequests(ctx context.Context, requestIDs []string, viewer Viewer) (map[string]bool, error)
	ListRequestFollowers(ctx context.Context, req Request) ([]Follower, error)
	ClearRequestFollowers(ctx context.Context, req Request, followers []Follower) error
	// ListRoutes returns every routing rule, in no particular order;
	// decideRoutes orders them.
	ListRoutes(ctx context.Context) ([]Route, error)
	// SetRoutingFacts stores facts fetched after the request was created (and
	// the anime flag they imply).
	SetRoutingFacts(ctx context.Context, id string, facts RoutingFacts) (*Request, error)
	// SetExternalIDs records a TVDB ID resolved after the request was created,
	// and fills the IMDb ID when the row has none. An existing positive TVDB ID
	// is kept; the TVDB ID the row holds afterwards is returned.
	SetExternalIDs(ctx context.Context, id string, tvdbID int, imdbID string) (int, error)
	ListTargets(ctx context.Context, requestID string) ([]Target, error)
	ListTargetsForRequests(ctx context.Context, requestIDs []string) (map[string][]Target, error)
	CreateTarget(ctx context.Context, target Target) (Target, error)
	DeleteTarget(ctx context.Context, id int64) error
	// UpdateTargetStatus also clears the target's download progress when it
	// completes or fails.
	UpdateTargetStatus(ctx context.Context, targetID int64, status Status, externalID, externalStatus, lastErr string, actor Viewer) (*Request, error)
	// UpdateTargetDownload stores a target's download progress, or clears it
	// when progress is nil, only while the target is queued or downloading. It
	// leaves updated_at, the request aggregate and the request's history alone.
	UpdateTargetDownload(ctx context.Context, targetID int64, progress *DownloadProgress) error
	// MarkTargetDownloadChecked records that a pass asked about a target
	// with progress and got no status back, without changing the progress.
	MarkTargetDownloadChecked(ctx context.Context, targetID int64) error
	// UpdateTargetExternalStatus records the raw status a queued or
	// downloading target's server reported when the target's own status did
	// not change. Like UpdateTargetDownload, it leaves updated_at, the
	// request aggregate and the request's history alone.
	UpdateTargetExternalStatus(ctx context.Context, targetID int64, externalStatus string) error
	ListIntegrations(ctx context.Context) ([]Integration, error)
	GetIntegration(ctx context.Context, id string) (*Integration, error)
	CreateIntegration(ctx context.Context, integration Integration) (*Integration, error)
	UpdateIntegration(ctx context.Context, integration Integration) (*Integration, error)
	// SaveIntegrationWithDefaults clears the conflicting kind default(s) and
	// creates (isCreate) or updates the instance atomically in one transaction.
	SaveIntegrationWithDefaults(ctx context.Context, integration Integration, isCreate bool) (*Integration, error)
	DeleteIntegration(ctx context.Context, id string) error
}

type CreateRequestRecord struct {
	ID        string
	Input     CreateRequestInput
	Status    Status
	Outcome   Outcome
	IsAnime   bool
	Facts     RoutingFacts
	Requester Viewer
	Now       time.Time
	// Quota, when non-nil, instructs the store to atomically verify the
	// requester is below their per-user limit before inserting. The check
	// runs inside the same transaction as the insert with a per-user
	// advisory lock so concurrent submissions cannot both exceed the limit.
	Quota *QuotaCheck
	// ReplaceFailed deletes the requester's own failed requests for the same
	// title in the insert transaction, before the quota check, so a re-request
	// replaces the failed row instead of sitting next to it.
	ReplaceFailed bool
}

type QuotaCheck struct {
	UserID      int
	WindowStart time.Time
	MaxRequests int
}
