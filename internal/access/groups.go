package access

import (
	"context"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// GroupPolicyProvider loads the access-group restriction layer for a user.
type GroupPolicyProvider interface {
	GetPolicyForUser(ctx context.Context, userID int) (*GroupPolicy, error)
}

// GroupPolicy is the restriction layer contributed by a user's access group.
type GroupPolicy struct {
	ID                       int64
	LibraryIDs               []int // nil = unrestricted
	MaxPlaybackQuality       string
	DownloadAllowed          bool
	DownloadTranscodeAllowed bool
	MaxStreams               int // 0 = no group cap
	MaxTranscodes            int
	AllowedPermissions       []string // nil = all assignable
	RequestsAllowed          bool
}

// EffectiveUserPolicy is the account layer after the group restriction is applied.
type EffectiveUserPolicy struct {
	LibraryIDs               []int // nil = unrestricted
	MaxPlaybackQuality       string
	DownloadAllowed          bool
	DownloadTranscodeAllowed bool
	MaxStreams               int
	MaxTranscodes            int
	Permissions              []string
	RequestsAllowed          bool
}

// EffectivePolicyForUser loads a user's group policy and returns the merged
// restriction layer. Nil providers are treated as "no group".
func EffectivePolicyForUser(ctx context.Context, user *models.User, provider GroupPolicyProvider) (EffectiveUserPolicy, error) {
	if provider == nil || user == nil {
		return ApplyGroupPolicy(user, nil), nil
	}
	group, err := provider.GetPolicyForUser(ctx, user.ID)
	if err != nil {
		return EffectiveUserPolicy{}, err
	}
	return ApplyGroupPolicy(user, group), nil
}

// ApplyGroupPolicy restricts user account policy by the optional access group.
func ApplyGroupPolicy(user *models.User, group *GroupPolicy) EffectiveUserPolicy {
	if user == nil {
		return EffectiveUserPolicy{RequestsAllowed: true}
	}

	effective := EffectiveUserPolicy{
		LibraryIDs:               cloneInts(user.LibraryIDs),
		MaxPlaybackQuality:       user.MaxPlaybackQuality,
		DownloadAllowed:          user.DownloadAllowed,
		DownloadTranscodeAllowed: user.DownloadTranscodeAllowed,
		MaxStreams:               user.MaxStreams,
		MaxTranscodes:            user.MaxTranscodes,
		Permissions:              cloneStrings(user.Permissions),
		RequestsAllowed:          true,
	}
	if group == nil {
		return effective
	}

	effective.LibraryIDs = restrictLibraryIDs(user.LibraryIDs, group.LibraryIDs)
	effective.MaxPlaybackQuality = MinQuality(user.MaxPlaybackQuality, group.MaxPlaybackQuality)
	effective.DownloadAllowed = user.DownloadAllowed && group.DownloadAllowed
	effective.DownloadTranscodeAllowed = user.DownloadTranscodeAllowed && group.DownloadTranscodeAllowed
	effective.MaxStreams = strictestPositive(user.MaxStreams, group.MaxStreams)
	effective.MaxTranscodes = strictestPositive(user.MaxTranscodes, group.MaxTranscodes)
	if group.AllowedPermissions != nil {
		effective.Permissions = intersectStrings(user.Permissions, group.AllowedPermissions)
	}
	effective.RequestsAllowed = group.RequestsAllowed
	return effective
}

func restrictLibraryIDs(userLibraryIDs, groupLibraryIDs []int) []int {
	switch {
	case userLibraryIDs != nil && groupLibraryIDs != nil:
		return intersectInts(userLibraryIDs, groupLibraryIDs)
	case userLibraryIDs != nil:
		return cloneInts(userLibraryIDs)
	case groupLibraryIDs != nil:
		return sortedUniqueInts(groupLibraryIDs)
	default:
		return nil
	}
}

func strictestPositive(userValue, groupValue int) int {
	switch {
	case userValue <= 0:
		return groupValue
	case groupValue <= 0:
		return userValue
	case userValue <= groupValue:
		return userValue
	default:
		return groupValue
	}
}

func intersectStrings(left, right []string) []string {
	if len(left) == 0 || len(right) == 0 {
		return []string{}
	}
	allowed := make(map[string]struct{}, len(right))
	for _, raw := range right {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		allowed[value] = struct{}{}
	}
	seen := make(map[string]struct{}, len(left))
	out := make([]string, 0, len(left))
	for _, raw := range left {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, ok := allowed[value]; !ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}
