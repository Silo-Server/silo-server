package auth

import "time"

type AccessRequest struct {
	UserID         int
	Action         ACLAction
	ResourceType   ACLResourceType
	ResourceID     string
	LibraryIDs     []int
	MediaType      string
	ProfileID      string
	PrimaryProfile bool
}

type ACLRule struct {
	ID           int64
	SubjectType  ACLSubjectType
	SubjectID    string
	Action       ACLAction
	ResourceType ACLResourceType
	ResourceID   string
	Effect       ACLEffect
	Conditions   ACLCondition
	Priority     int
	Name         string
	Description  string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type ACLCondition struct {
	LibraryIDs                 []int
	MediaTypes                 []string
	PrimaryProfileRequired     *bool
	MaxPlaybackQuality         string
	MaxStreams                 *int
	MaxTranscodes              *int
	DirectDownloadsAllowed     *bool
	TranscodedDownloadsAllowed *bool
	MaxContentRating           string
}

type AccessDecision struct {
	Allowed         bool
	ReasonCode      string
	WinningRule     *ACLRule
	MatchedRules    []ACLRule
	EffectivePolicy EffectivePolicy
}

type AccessExplanation struct {
	Request        AccessRequest
	Decision       AccessDecision
	EvaluatedRules []ACLRule
}

type EffectivePolicy struct {
	LibraryIDs                 []int
	MediaTypes                 []string
	MaxPlaybackQuality         string
	MaxStreams                 int
	MaxTranscodes              int
	DirectDownloadsAllowed     bool
	TranscodedDownloadsAllowed bool
	MaxContentRating           string
}
