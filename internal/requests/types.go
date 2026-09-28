package requests

import (
	"time"
)

type MediaType string

const (
	MediaTypeMovie  MediaType = "movie"
	MediaTypeSeries MediaType = "series"
	MediaTypeAll    MediaType = "all"
)

type Status string

const (
	StatusPending     Status = "pending"
	StatusApproved    Status = "approved"
	StatusQueued      Status = "queued"
	StatusDownloading Status = "downloading"
	StatusCompleted   Status = "completed"
)

const StatusFailed Status = "failed" // target-only status; requests use outcome=failed

type Outcome string

const (
	OutcomeActive    Outcome = "active"
	OutcomeDeclined  Outcome = "declined"
	OutcomeCancelled Outcome = "cancelled"
	OutcomeFailed    Outcome = "failed"
)

type Quality string

const (
	Quality1080p Quality = "1080p"
	Quality2160p Quality = "2160p"
)

// Target is one fulfillment of a request against a single instance at a single
// quality. A request fans out to one Target per resolved quality.
type Target struct {
	ID              int64     `json:"id"`
	RequestID       string    `json:"request_id"`
	IntegrationID   string    `json:"integration_id,omitempty"`
	IntegrationKind string    `json:"integration_kind,omitempty"`
	InstanceName    string    `json:"instance_name,omitempty"`
	Quality         Quality   `json:"quality"`
	IsAnime         bool      `json:"is_anime"`
	ExternalID      string    `json:"external_id,omitempty"`
	ExternalStatus  string    `json:"external_status,omitempty"`
	Status          Status    `json:"status"`
	LastError       string    `json:"last_error,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	// RouteID and RouteName record which routing rule sent the target (v2
	// only); empty when the plugin routed it.
	RouteID   string `json:"-"`
	RouteName string `json:"-"`
	// Download is how far the target's downloads are, while its plugin reports
	// any (v2 only).
	Download *DownloadProgress `json:"-"`
}

// DownloadPhase is where a target's downloads are, as its router plugin
// reports them. The set is open: clients read an unknown phase as downloading.
type DownloadPhase string

const (
	DownloadPhaseQueued        DownloadPhase = "queued"
	DownloadPhaseDownloading   DownloadPhase = "downloading"
	DownloadPhasePaused        DownloadPhase = "paused"
	DownloadPhaseStalled       DownloadPhase = "stalled"
	DownloadPhaseImporting     DownloadPhase = "importing"
	DownloadPhaseImportBlocked DownloadPhase = "import_blocked"
)

// downloadPhaseRank orders phases for aggregation, highest first:
// import_blocked > stalled > downloading > importing > paused > queued. A
// download that needs attention outranks the rest; otherwise downloading wins
// while anything still downloads. An unknown phase ranks as downloading.
func downloadPhaseRank(phase DownloadPhase) int {
	switch phase {
	case DownloadPhaseImportBlocked:
		return 5
	case DownloadPhaseStalled:
		return 4
	case DownloadPhaseImporting:
		return 2
	case DownloadPhasePaused:
		return 1
	case DownloadPhaseQueued:
		return 0
	default:
		return 3
	}
}

// DownloadProgress is how far a target's downloads are, as the downstream
// service last reported them. BytesTotal is 0 while the size is unknown.
// UpdatedAt is when the server last heard from the plugin.
type DownloadProgress struct {
	Phase               DownloadPhase
	BytesTotal          int64
	BytesLeft           int64
	EstimatedCompletion *time.Time
	Downloads           int
	UpdatedAt           time.Time
}

// Download aggregates the progress of the request's live (queued or
// downloading) targets, 1080p and 4K together: bytes and downloads are
// summed, the phase is the highest ranked, the estimate is the latest, and
// UpdatedAt is the oldest report, so the figure is only as fresh as its
// stalest part. The total is unknown (0) when any target's is. It is nil when
// no live target reports progress.
func (r *Request) Download() *DownloadProgress {
	var out *DownloadProgress
	sizeKnown := true
	for _, t := range r.Targets {
		if t.Status != StatusQueued && t.Status != StatusDownloading {
			continue
		}
		if t.Download == nil {
			// A live target reporting nothing yet still has a size to come; a
			// percentage without it would cover only part of the request.
			sizeKnown = false
			continue
		}
		d := *t.Download
		if d.BytesTotal <= 0 {
			sizeKnown = false
		}
		if out == nil {
			out = &d
			continue
		}
		if downloadPhaseRank(d.Phase) > downloadPhaseRank(out.Phase) {
			out.Phase = d.Phase
		}
		out.BytesTotal += d.BytesTotal
		out.BytesLeft += d.BytesLeft
		out.Downloads += d.Downloads
		if d.EstimatedCompletion != nil && (out.EstimatedCompletion == nil || d.EstimatedCompletion.After(*out.EstimatedCompletion)) {
			out.EstimatedCompletion = d.EstimatedCompletion
		}
		if d.UpdatedAt.Before(out.UpdatedAt) {
			out.UpdatedAt = d.UpdatedAt
		}
	}
	if out != nil && !sizeKnown {
		out.BytesTotal, out.BytesLeft = 0, 0
	}
	return out
}

type Availability string

const (
	AvailabilityMissing   Availability = "missing"
	AvailabilityAvailable Availability = "available"
)

type LimitMode string

const (
	LimitModeInherit   LimitMode = "inherit"
	LimitModeCustom    LimitMode = "custom"
	LimitModeUnlimited LimitMode = "unlimited"
	LimitModeBlocked   LimitMode = "blocked"
)

type ApprovalMode string

const (
	ApprovalModeInherit ApprovalMode = "inherit"
	ApprovalModeManual  ApprovalMode = "manual"
	ApprovalModeAuto    ApprovalMode = "auto"
	ApprovalModeBlocked ApprovalMode = "blocked"
)

type Viewer struct {
	UserID    int
	ProfileID string
	IsAdmin   bool
}

type Settings struct {
	Revision                  int64     `json:"-"`
	RequestsEnabled           bool      `json:"requests_enabled"`
	GlobalMaxRequests         int       `json:"global_max_requests"`
	GlobalWindowDays          int       `json:"global_window_days"`
	GlobalAutoApprovalEnabled bool      `json:"global_auto_approval_enabled"`
	ForceDualQuality          bool      `json:"force_dual_quality"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

type FeatureStatus struct {
	RequestsEnabled bool `json:"requests_enabled"`
	// RatingRestrictionsEnforced advertises that discovery/search results are
	// filtered by the profile's max content rating and that over-ceiling
	// detail (404) and create (403) requests are rejected. Additive v1
	// capability field so clients can feature-detect instead of version-sniff.
	RatingRestrictionsEnforced bool `json:"rating_restrictions_enforced"`
	// MissingSeasonsRequestable reports whether a series already in the
	// library can be requested for its missing seasons (v2 only).
	MissingSeasonsRequestable bool `json:"-"`
}

type UserLimit struct {
	Revision     int64        `json:"-"`
	UserID       int          `json:"user_id"`
	LimitMode    LimitMode    `json:"limit_mode"`
	MaxRequests  *int         `json:"max_requests,omitempty"`
	WindowDays   *int         `json:"window_days,omitempty"`
	ApprovalMode ApprovalMode `json:"approval_mode"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

type EffectivePolicy struct {
	RequestsEnabled bool
	MaxRequests     int
	WindowDays      int
	Unlimited       bool
	Blocked         bool
	AutoApprove     bool
	Used            int
	Remaining       int
	WindowStart     time.Time
}

type Request struct {
	ID                   string    `json:"id"`
	Provider             string    `json:"provider"`
	MediaType            MediaType `json:"media_type"`
	TMDBID               int       `json:"tmdb_id"`
	TVDBID               *int      `json:"tvdb_id,omitempty"`
	IMDbID               string    `json:"imdb_id,omitempty"`
	Title                string    `json:"title"`
	Year                 *int      `json:"year,omitempty"`
	Overview             string    `json:"overview,omitempty"`
	PosterPath           string    `json:"poster_path,omitempty"`
	BackdropPath         string    `json:"backdrop_path,omitempty"`
	Status               Status    `json:"status"`
	Outcome              Outcome   `json:"outcome"`
	RequestedByUserID    int       `json:"requested_by_user_id,omitempty"`
	RequestedByProfileID string    `json:"requested_by_profile_id,omitempty"`
	RequesterEmail       string    `json:"-"`
	RequesterUsername    string    `json:"-"`
	// OutcomeReason is why the request was declined or withdrawn, when a
	// reason was given. v2 only; the frozen v1 shape does not carry it.
	OutcomeReason    string     `json:"-"`
	IntegrationKind  string     `json:"integration_kind,omitempty"`
	IsAnime          bool       `json:"is_anime"`
	Targets          []Target   `json:"targets,omitempty"`
	ExternalID       string     `json:"external_id,omitempty"`
	ExternalStatus   string     `json:"external_status,omitempty"`
	LibraryContentID string     `json:"library_content_id,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ApprovedAt       *time.Time `json:"approved_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	// SubmitAttempts counts router submissions claimed for the current
	// approval. SubmitLeaseUntil is set while a claimed submission is in
	// flight; NextSubmitAt is the backoff after a failed attempt.
	SubmitAttempts   int        `json:"-"`
	SubmitLeaseUntil *time.Time `json:"-"`
	NextSubmitAt     *time.Time `json:"-"`
	// RoutingFacts is the TMDB snapshot routing rules match on.
	RoutingFacts RoutingFacts `json:"-"`
	// Seasons are the season numbers a series request asks for; empty means
	// the whole series. SeasonProgress is attached on reads: each requested
	// season's episode counts once the series is in the library.
	Seasons        []int            `json:"-"`
	SeasonProgress []SeasonProgress `json:"-"`
	// Followers are the profiles, other than the requester's, that asked to be
	// told when the title is available; loaded for the fulfilled notification.
	Followers []Follower `json:"-"`

	// externalIDsResolved marks a request whose external IDs were just looked
	// up in this call (CreateRequest), so an immediate submission does not
	// repeat the provider searches. tvdbLookupFailed records that the latest
	// TVDB lookup hit a provider error rather than a confirmed miss. Neither is
	// persisted or serialized.
	externalIDsResolved bool
	tvdbLookupFailed    bool
}

// StateGuard names the states a transition may start from. The store applies
// the write only while the row is still in one of them and otherwise answers
// ErrInvalidState, so two actors racing on one request (two admins, or an
// admin and the reconciler) cannot both apply a transition. An empty list
// accepts any value.
type StateGuard struct {
	Statuses []Status
	Outcomes []Outcome
	// UnsentOnly admits an approved request only while nothing has gone
	// downstream for it: it has no target and no submission in flight.
	UnsentOnly bool
}

// guardPending matches a request that is still waiting for an admin.
var guardPending = StateGuard{Statuses: []Status{StatusPending}, Outcomes: []Outcome{OutcomeActive}}

// guardWithdrawable matches a request nobody has sent anywhere yet: pending,
// or approved but waiting for the library (no router), or backing off after a
// failed attempt. Decline and cancel accept these; once a submission is in
// flight or a target exists, the request stays in the pipeline.
var guardWithdrawable = StateGuard{
	Statuses:   []Status{StatusPending, StatusApproved},
	Outcomes:   []Outcome{OutcomeActive},
	UnsentOnly: true,
}

// guardFailed accepts a failed request, which an admin may close instead of
// retrying it.
var guardFailed = StateGuard{Outcomes: []Outcome{OutcomeFailed}}

// State is the one lifecycle state a user sees for a request, derived from
// its status, outcome and library presence. Status and outcome stay on the
// wire for admin detail and older clients.
type State string

const (
	StatePending    State = "pending"
	StateApproved   State = "approved"
	StateProcessing State = "processing"
	// StatePartiallyAvailable: some of a season request's seasons are in the
	// library, not all.
	StatePartiallyAvailable State = "partially_available"
	StateAvailable          State = "available"
	StateDeclined           State = "declined"
	StateCancelled          State = "cancelled" //nolint:misspell // matches the outcome spelling
	StateFailed             State = "failed"
)

// State derives the request's user-facing state. A completed request is
// available once its title is in the library (LibraryContentID attached);
// until the scan finds it, it is still processing. A season request is
// available when every requested season is complete (SeasonProgress
// attached), and partially available while only some of their episodes are
// in.
func (r *Request) State() State {
	switch r.Outcome {
	case OutcomeDeclined:
		return StateDeclined
	case OutcomeCancelled:
		return StateCancelled
	case OutcomeFailed:
		return StateFailed
	}
	if r.Status == StatusPending {
		return StatePending
	}
	if len(r.SeasonProgress) > 0 {
		if seasonsDelivered(r.SeasonProgress, r.Status == StatusCompleted) {
			return StateAvailable
		}
		for _, p := range r.SeasonProgress {
			if p.Have > 0 {
				return StatePartiallyAvailable
			}
		}
	}
	switch r.Status {
	case StatusApproved:
		return StateApproved
	case StatusCompleted:
		if r.LibraryContentID != "" && len(r.Seasons) == 0 {
			return StateAvailable
		}
		return StateProcessing
	default:
		return StateProcessing
	}
}

// requestedBy reports whether the viewer's profile made the request. A profile
// id is unique only within its account, so the account must match too.
func (r *Request) requestedBy(viewer Viewer) bool {
	return r.RequestedByUserID == viewer.UserID && r.RequestedByProfileID == viewer.ProfileID
}

// RequestEvent is one entry of a request's history. ActorUsername is set
// when the actor's account still exists.
type RequestEvent struct {
	ID             int64     `json:"id"`
	RequestID      string    `json:"request_id"`
	EventType      string    `json:"event_type"`
	ActorUserID    *int      `json:"actor_user_id,omitempty"`
	ActorProfileID string    `json:"actor_profile_id,omitempty"`
	ActorUsername  string    `json:"-"`
	Message        string    `json:"message,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type RequestState struct {
	Status      Status `json:"status,omitempty"`
	Requestable bool   `json:"requestable"`
	Reason      string `json:"reason,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	// Following reports that the viewer will be notified when the title
	// becomes available: they requested it or follow it. RequestedByViewer
	// reports that the viewing profile made the active request, so there is
	// nothing to follow. v2 only; the frozen v1 shape carries neither.
	Following         bool `json:"-"`
	RequestedByViewer bool `json:"-"`
	// State is the active request's user-facing state (v2 only).
	State State `json:"-"`
	// Download is the active request's download progress. Only the title
	// detail fills it; search and discovery do not load targets (v2 only).
	Download *DownloadProgress `json:"-"`
}

type MediaResult struct {
	MediaType        MediaType    `json:"media_type"`
	TMDBID           int          `json:"tmdb_id"`
	Title            string       `json:"title"`
	Year             int          `json:"year,omitempty"`
	Overview         string       `json:"overview,omitempty"`
	PosterPath       string       `json:"poster_path,omitempty"`
	BackdropPath     string       `json:"backdrop_path,omitempty"`
	ReleaseDate      string       `json:"release_date,omitempty"`
	Popularity       float64      `json:"popularity,omitempty"`
	VoteAverage      float64      `json:"vote_average,omitempty"`
	Availability     Availability `json:"availability"`
	LibraryContentID string       `json:"library_content_id,omitempty"`
	Request          RequestState `json:"request"`
}

type MediaPage struct {
	Page         int           `json:"page"`
	TotalPages   int           `json:"total_pages"`
	TotalResults int           `json:"total_results"`
	Results      []MediaResult `json:"results"`
}

type MediaCastMember struct {
	Name        string `json:"name"`
	Character   string `json:"character,omitempty"`
	ProfilePath string `json:"profile_path,omitempty"`
	Order       int    `json:"order"`
}

type MediaDetail struct {
	MediaType           MediaType         `json:"media_type"`
	TMDBID              int               `json:"tmdb_id"`
	IMDbID              string            `json:"imdb_id,omitempty"`
	TVDBID              *int              `json:"tvdb_id,omitempty"`
	Title               string            `json:"title"`
	OriginalTitle       string            `json:"original_title,omitempty"`
	Tagline             string            `json:"tagline,omitempty"`
	Overview            string            `json:"overview,omitempty"`
	PosterPath          string            `json:"poster_path,omitempty"`
	BackdropPath        string            `json:"backdrop_path,omitempty"`
	ReleaseDate         string            `json:"release_date,omitempty"`
	Year                int               `json:"year,omitempty"`
	Runtime             int               `json:"runtime,omitempty"`
	Genres              []string          `json:"genres,omitempty"`
	VoteAverage         float64           `json:"vote_average,omitempty"`
	VoteCount           int               `json:"vote_count,omitempty"`
	Status              string            `json:"status,omitempty"`
	Homepage            string            `json:"homepage,omitempty"`
	ContentRating       string            `json:"content_rating,omitempty"`
	ProductionCompanies []string          `json:"production_companies,omitempty"`
	NumberOfSeasons     int               `json:"number_of_seasons,omitempty"`
	NumberOfEpisodes    int               `json:"number_of_episodes,omitempty"`
	FirstAirDate        string            `json:"first_air_date,omitempty"`
	LastAirDate         string            `json:"last_air_date,omitempty"`
	Networks            []string          `json:"networks,omitempty"`
	Cast                []MediaCastMember `json:"cast,omitempty"`
	Director            string            `json:"director,omitempty"`
	Creators            []string          `json:"creators,omitempty"`
	Recommendations     []MediaResult     `json:"recommendations,omitempty"`
	Availability        Availability      `json:"availability"`
	LibraryContentID    string            `json:"library_content_id,omitempty"`
	Request             RequestState      `json:"request"`
	// Seasons lists a series' regular seasons with their library
	// availability and whether the active request covers them.
	Seasons []RequestSeason `json:"-"`
}

type CreateRequestInput struct {
	MediaType    MediaType `json:"media_type"`
	TMDBID       int       `json:"tmdb_id"`
	TVDBID       *int      `json:"tvdb_id,omitempty"`
	IMDbID       string    `json:"imdb_id,omitempty"`
	Title        string    `json:"title"`
	Year         *int      `json:"year,omitempty"`
	Overview     string    `json:"overview,omitempty"`
	PosterPath   string    `json:"poster_path,omitempty"`
	BackdropPath string    `json:"backdrop_path,omitempty"`
	// Seasons are the season numbers a series request asks for; none means
	// every aired season still missing. v2 only; the frozen v1 body does not
	// carry it.
	Seasons []int `json:"-"`
	// WholeSeries keeps the rule from before season requests, for v1: a
	// series request asks for the whole series and is refused once the
	// series is in the library.
	WholeSeries bool `json:"-"`
}

// RequestPageKey identifies the last emitted request in descending creation order.
type RequestPageKey struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

type ListFilter struct {
	Before  *RequestPageKey
	Status  Status
	Outcome Outcome
	// Admin queue filters.
	View              AdminView
	Query             string
	MediaType         MediaType
	RequestedByUserID int
	Limit             int
	Offset            int
}

// AdminView groups the admin queue by what an admin does next.
type AdminView string

const (
	// AdminViewNeedsApproval: pending, waiting for an admin.
	AdminViewNeedsApproval AdminView = "needs_approval"
	// AdminViewInProgress: approved and on its way to the library.
	AdminViewInProgress AdminView = "in_progress"
	// AdminViewFailed: failed; Retry sends it again.
	AdminViewFailed AdminView = "failed"
	// AdminViewDone: completed, or closed by a decline or cancellation.
	AdminViewDone AdminView = "done"
)

// Valid reports whether v names a view.
func (v AdminView) Valid() bool {
	switch v {
	case AdminViewNeedsApproval, AdminViewInProgress, AdminViewFailed, AdminViewDone:
		return true
	}
	return false
}

// AdminViewCounts counts the requests in each admin view.
type AdminViewCounts struct {
	NeedsApproval int
	InProgress    int
	Failed        int
	Done          int
}

type Integration struct {
	Revision            int64          `json:"-"`
	ID                  string         `json:"id"`
	Name                string         `json:"name"`
	Enabled             bool           `json:"enabled"`
	BaseURL             string         `json:"base_url"`
	APIKeyRef           string         `json:"api_key_ref,omitempty"`
	LastCheckAt         *time.Time     `json:"last_check_at,omitempty"`
	LastCheckStatus     string         `json:"last_check_status,omitempty"`
	LastCheckError      string         `json:"last_check_error,omitempty"`
	UpdatedAt           time.Time      `json:"updated_at"`
	CapabilityID        string         `json:"capability_id"`
	InstallationID      *int           `json:"installation_id,omitempty"`
	SupportedMediaTypes []string       `json:"supported_media_types"`
	PluginConfig        map[string]any `json:"plugin_config"`
}

type FulfillmentResult struct {
	IntegrationKind string
	ExternalID      string
	ExternalStatus  string
}

type FulfillmentStatus struct {
	Status          Status
	Outcome         Outcome
	IntegrationKind string
	ExternalID      string
	ExternalStatus  string
	Message         string
}

type ReconcileResult struct {
	Checked     int `json:"checked"`
	Submitted   int `json:"submitted"`
	Downloading int `json:"downloading"`
	Completed   int `json:"completed"`
	Failed      int `json:"failed"`
	Skipped     int `json:"skipped"`
	// Deferred counts submissions that failed and were rescheduled.
	Deferred int `json:"deferred"`
	Errors   int `json:"errors"`
}

// DownloadRefreshResult counts one download refresh pass. Checked counts the
// requests whose targets were asked about, Updated those where a target's
// status moved.
type DownloadRefreshResult struct {
	Checked int `json:"checked"`
	Updated int `json:"updated"`
	Errors  int `json:"errors"`
}
