package models

import "time"

// ScannedMediaGroup is the persisted group-inference snapshot for one logical
// content group inside a library folder.
type ScannedMediaGroup struct {
	MediaFolderID          int
	GroupKeyVersion        int
	ContentGroupKey        string
	State                  string
	InferredType           string
	TypeConfidence         string
	BaseTitle              string
	BaseYear               int
	TmdbID                 string
	ImdbID                 string
	TvdbID                 string
	ObservedFileCount      int
	SampleFilePath         string
	SampleObservedRootPath string
	EvidenceJSON           []byte
	OverrideSource         string
	FirstSeenAt            time.Time
	LastSeenAt             time.Time
}

// MediaGroupOverride stores an operator-provided override for a scanned group.
type MediaGroupOverride struct {
	MediaFolderID   int
	GroupKeyVersion int
	ContentGroupKey string
	ForcedType      string
	ForcedTitle     string
	ForcedYear      int
	ForcedTmdbID    string
	ForcedImdbID    string
	ForcedTvdbID    string
	Note            string
	CreatedByUserID *int
	UpdatedByUserID *int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Scopes for MediaIdentityOverride rows.
const (
	IdentityOverrideScopeRoot = "root"
	IdentityOverrideScopeFile = "file"
)

// MediaIdentityOverride stores an operator-provided identity forced onto all
// files under a root path (scope "root") or one file (scope "file"). Applied
// per file during group inference, before bucketing, so overridden files form
// their own content group — this is what makes a version split durable across
// rescans. File scope wins over root scope.
type MediaIdentityOverride struct {
	ID              int64
	MediaFolderID   int
	Scope           string // "root" or "file"
	RootPath        string // scope="root"
	FilePath        string // scope="file"
	ForcedType      string
	ForcedTitle     string
	ForcedYear      int
	ForcedTmdbID    string
	ForcedImdbID    string
	ForcedTvdbID    string
	Note            string
	CreatedByUserID *int
	UpdatedByUserID *int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ObservedMediaLocation tracks one physical path-scoped media location.
type ObservedMediaLocation struct {
	MediaFolderID          int
	ObservedRootPath       string
	LocationType           string
	SampleFilePath         string
	ObservedFileCount      int
	ContentGroupCount      int
	PrimaryGroupKeyVersion int
	PrimaryContentGroupKey string
	State                  string
	EvidenceJSON           []byte
	FirstSeenAt            time.Time
	LastSeenAt             time.Time
}

// MediaGroupLocation links a logical content group to a physical location.
type MediaGroupLocation struct {
	MediaFolderID    int
	GroupKeyVersion  int
	ContentGroupKey  string
	ObservedRootPath string
	IsPrimary        bool
	FirstSeenAt      time.Time
	LastSeenAt       time.Time
}

// SeriesRootMatchQueueEntry represents one pending initial series-root job.
type SeriesRootMatchQueueEntry struct {
	MediaFolderID    int
	ObservedRootPath string
	FirstQueuedAt    time.Time
	AvailableAt      time.Time
	LastAttemptedAt  *time.Time
	AttemptCount     int
	LastError        string
	UpdatedAt        time.Time
}

// SeriesRootMatchJob is the claimed work payload returned to the matcher.
type SeriesRootMatchJob struct {
	MediaFolderID     int
	ObservedRootPath  string
	SampleFilePath    string
	ObservedFileCount int
}

// MovieMatchQueueEntry represents one pending initial movie-file job.
type MovieMatchQueueEntry struct {
	MediaFileID     int
	MediaFolderID   int
	FilePath        string
	FirstQueuedAt   time.Time
	AvailableAt     time.Time
	LastAttemptedAt *time.Time
	AttemptCount    int
	LastError       string
	UpdatedAt       time.Time
}
