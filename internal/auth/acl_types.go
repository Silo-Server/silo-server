package auth

import "time"

type AccessRequest struct {
	UserID                      int
	Action                      ACLAction
	ResourceType                ACLResourceType
	ResourceID                  string
	LibraryIDs                  []int
	MediaType                   string
	ProfileID                   string
	PrimaryProfile              bool
	PlaybackQuality             string
	CurrentStreams              int
	CurrentTranscodes           int
	ContentRating               string
	DirectDownloadRequested     bool
	TranscodedDownloadRequested bool
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

type CreateACLGroupInput struct {
	Slug        string
	Name        string
	Description string
	Policy      ACLPolicy
}

type UpdateACLGroupInput struct {
	Name        string
	Description string
	Policy      ACLPolicy
}

type ACLRuleInput struct {
	Action       ACLAction
	ResourceType ACLResourceType
	ResourceID   string
	Effect       ACLEffect
	Conditions   ACLCondition
	Priority     int
	Name         string
	Description  string
}

type ACLGroup struct {
	ID          int64
	Slug        string
	Name        string
	Description string
	Policy      ACLPolicy
	BuiltIn     bool
	Protected   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type ACLGroupMember struct {
	UserID   int
	Username string
	Email    string
	Role     string
	Enabled  bool
}

type ACLPolicy struct {
	LibraryIDs                 []int    `json:"library_ids,omitempty"`
	MediaTypes                 []string `json:"media_types,omitempty"`
	MaxPlaybackQuality         string   `json:"max_playback_quality,omitempty"`
	MaxStreams                 *int     `json:"max_streams,omitempty"`
	MaxTranscodes              *int     `json:"max_transcodes,omitempty"`
	MaxProfiles                *int     `json:"max_profiles,omitempty"`
	DirectDownloadsAllowed     *bool    `json:"direct_downloads_allowed,omitempty"`
	TranscodedDownloadsAllowed *bool    `json:"transcoded_downloads_allowed,omitempty"`
}

type ACLCondition struct {
	LibraryIDs                 []int    `json:"library_ids"`
	MediaTypes                 []string `json:"media_types"`
	PrimaryProfileRequired     *bool    `json:"primary_profile_required"`
	MaxPlaybackQuality         string   `json:"max_playback_quality"`
	MaxStreams                 *int     `json:"max_streams"`
	MaxTranscodes              *int     `json:"max_transcodes"`
	DirectDownloadsAllowed     *bool    `json:"direct_downloads_allowed"`
	TranscodedDownloadsAllowed *bool    `json:"transcoded_downloads_allowed"`
	MaxContentRating           string   `json:"max_content_rating"`
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
	MaxProfiles                int
	DirectDownloadsAllowed     bool
	TranscodedDownloadsAllowed bool
	MaxContentRating           string
}
