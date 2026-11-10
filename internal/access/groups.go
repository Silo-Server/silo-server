package access

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// GroupPolicyProvider loads the access-group restriction layer for a user.
type GroupPolicyProvider interface {
	GetPolicyForUser(ctx context.Context, userID int) (*GroupPolicy, error)
}

// GroupPolicy is the access group's policy layer. Every user-level policy
// field has a group counterpart here; the group value applies to each member
// whose own field is unset (inherits).
type GroupPolicy struct {
	ID                         int64
	LibraryIDs                 []int // nil = unrestricted
	MaxPlaybackQuality         string
	DownloadAllowed            bool
	DownloadTranscodeAllowed   bool
	TranscodeAllowed           bool
	AudioTranscodeAllowed      bool
	MaxStreams                 int // 0 = no cap
	MaxTranscodes              int
	MaxRemoteStreamBitrateKbps int      // 0 = unlimited
	MaxLocalStreamBitrateKbps  int      // 0 = unlimited
	MaxProfiles                int      // household profiles per account; at least 1
	AllowedPermissions         []string // nil = all assignable
	RequestsAllowed            bool
}

// EffectiveUserPolicy is the fully resolved policy for an account: every field
// carries a concrete value (user override when set, otherwise the group value,
// otherwise the permissive no-group default).
type EffectiveUserPolicy struct {
	LibraryIDs                 []int // nil = unrestricted
	MaxPlaybackQuality         string
	DownloadAllowed            bool
	DownloadTranscodeAllowed   bool
	TranscodeAllowed           bool
	AudioTranscodeAllowed      bool
	MaxStreams                 int
	MaxTranscodes              int
	MaxRemoteStreamBitrateKbps int
	MaxLocalStreamBitrateKbps  int
	MaxProfiles                int
	Permissions                []string
	RequestsAllowed            bool
}

// AdminPolicy is the policy under an admin account's own overrides: full
// access, the same as the server Owner has. Admins never belong to an access
// group, so this is the only layer beneath their overrides; an override on an
// admin account still restricts it.
func AdminPolicy() GroupPolicy {
	policy := NoGroupPolicy()
	policy.DownloadTranscodeAllowed = true
	return policy
}

// DefaultMaxProfiles is the household profile limit of an account outside
// every access group, and of a new group that does not set one. It was the
// stored default on every account before groups set the limit.
const DefaultMaxProfiles = 5

// NoGroupPolicy is the policy applied to a regular account with no access
// group. It is permissive so that an unset field on such an account keeps
// today's unrestricted behavior. DownloadTranscodeAllowed is the exception: it
// defaults to false because that was the old column default on users (and is
// the seeded Default Group's value), so an account that never had the gate
// turned on does not silently gain it. MaxProfiles is DefaultMaxProfiles, the
// limit such an account always had: profiles were never unlimited. Admins use
// AdminPolicy instead.
func NoGroupPolicy() GroupPolicy {
	return GroupPolicy{
		LibraryIDs:                 nil,
		MaxPlaybackQuality:         "",
		DownloadAllowed:            true,
		DownloadTranscodeAllowed:   false,
		TranscodeAllowed:           true,
		AudioTranscodeAllowed:      true,
		MaxStreams:                 0,
		MaxTranscodes:              0,
		MaxRemoteStreamBitrateKbps: 0,
		MaxLocalStreamBitrateKbps:  0,
		MaxProfiles:                DefaultMaxProfiles,
		AllowedPermissions:         nil,
		RequestsAllowed:            true,
	}
}

// GroupApplies reports whether an access group contributes to the user's
// effective policy. Admin accounts are never capped by a group: the repository
// keeps them ungrouped, and a row that still carries a group (written before
// that rule existed) is resolved as if it did not.
func GroupApplies(user *models.User) bool {
	return user != nil && user.AccessGroupID != nil && user.Role != models.RoleAdmin
}

// EffectivePolicyForUser loads a user's group policy and returns the resolved
// policy. Nil providers are treated as "no group". An account whose group does
// not apply (see GroupApplies) resolves without querying the provider.
func EffectivePolicyForUser(ctx context.Context, user *models.User, provider GroupPolicyProvider) (EffectiveUserPolicy, error) {
	if provider == nil || !GroupApplies(user) {
		return ApplyGroupPolicy(user, nil), nil
	}
	group, err := provider.GetPolicyForUser(ctx, user.ID)
	if err != nil {
		return EffectiveUserPolicy{}, err
	}
	return ApplyGroupPolicy(user, group), nil
}

// ApplyGroupPolicy resolves the user's account policy against the optional
// access group: each field takes the user's explicit override when set and
// the group's value otherwise. A nil group means the permissive
// NoGroupPolicy. An admin account resolves against AdminPolicy and ignores
// any group. Permissions are the one mask-style field: the group's
// allowed_permissions (when set) intersects the user's permissions.
func ApplyGroupPolicy(user *models.User, group *GroupPolicy) EffectiveUserPolicy {
	if user == nil {
		return EffectiveUserPolicy{RequestsAllowed: true}
	}
	base := NoGroupPolicy()
	if user.Role == models.RoleAdmin {
		base, group = AdminPolicy(), nil
	}
	if group != nil {
		base = *group
	}

	effective := EffectiveUserPolicy{
		LibraryIDs:                 inheritLibraryIDs(user.LibraryIDs, base.LibraryIDs),
		MaxPlaybackQuality:         NormalizePlaybackQuality(inheritString(user.MaxPlaybackQuality, base.MaxPlaybackQuality)),
		DownloadAllowed:            inheritBool(user.DownloadAllowed, base.DownloadAllowed),
		DownloadTranscodeAllowed:   inheritBool(user.DownloadTranscodeAllowed, base.DownloadTranscodeAllowed),
		TranscodeAllowed:           inheritBool(user.TranscodeAllowed, base.TranscodeAllowed),
		AudioTranscodeAllowed:      inheritBool(user.AudioTranscodeAllowed, base.AudioTranscodeAllowed),
		MaxStreams:                 inheritInt(user.MaxStreams, base.MaxStreams),
		MaxTranscodes:              inheritInt(user.MaxTranscodes, base.MaxTranscodes),
		MaxRemoteStreamBitrateKbps: inheritInt(user.MaxRemoteStreamBitrateKbps, base.MaxRemoteStreamBitrateKbps),
		MaxLocalStreamBitrateKbps:  inheritInt(user.MaxLocalStreamBitrateKbps, base.MaxLocalStreamBitrateKbps),
		MaxProfiles:                inheritInt(user.MaxProfiles, base.MaxProfiles),
		Permissions:                cloneStrings(user.Permissions),
		RequestsAllowed:            inheritBool(user.RequestsAllowed, base.RequestsAllowed),
	}
	if group != nil && group.AllowedPermissions != nil {
		effective.Permissions = intersectStrings(user.Permissions, group.AllowedPermissions)
	}
	return effective
}

func inheritLibraryIDs(userLibraryIDs, groupLibraryIDs []int) []int {
	if userLibraryIDs != nil {
		return sortedUniqueInts(userLibraryIDs)
	}
	if groupLibraryIDs != nil {
		return sortedUniqueInts(groupLibraryIDs)
	}
	return nil
}

func inheritInt(override *int, inherited int) int {
	if override != nil {
		if *override < 0 {
			return 0
		}
		return *override
	}
	return inherited
}

func inheritBool(override *bool, inherited bool) bool {
	if override != nil {
		return *override
	}
	return inherited
}

func inheritString(override *string, inherited string) string {
	if override != nil {
		return *override
	}
	return inherited
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

// unlimitedValue renders a policy field's open value.
const unlimitedValue = "unlimited"

// PolicyWidening is one field where a policy grants more than a limit does.
type PolicyWidening struct {
	// Field is the policy field, named as EffectiveUserPolicy's JSON
	// projections name it (library_ids, max_streams, ...).
	Field string
	// Policy and Limit render each side's value; "unlimited" and
	// "unrestricted" spell out the open values.
	Policy string
	Limit  string
}

// WiderThan lists every field where policy grants more than limit: a library
// limit lifted or a library added, a higher or lifted quality ceiling, a
// higher or lifted stream, transcode, or bitrate cap (0 is unlimited), more
// household profiles, a permission or capability limit lacks. Both sides
// must name libraries by the same ids. Nil means policy grants nothing
// limit does not.
func WiderThan(policy, limit EffectiveUserPolicy) []PolicyWidening {
	var out []PolicyWidening
	add := func(field, p, l string) { out = append(out, PolicyWidening{Field: field, Policy: p, Limit: l}) }
	libraries := func(ids []int) string {
		if ids == nil {
			return "unrestricted"
		}
		var parts []string
		for _, id := range sortedUniqueInts(ids) {
			parts = append(parts, strconv.Itoa(id))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	if limit.LibraryIDs != nil {
		allowed := map[int]bool{}
		for _, id := range limit.LibraryIDs {
			allowed[id] = true
		}
		wider := policy.LibraryIDs == nil
		for _, id := range policy.LibraryIDs {
			wider = wider || !allowed[id]
		}
		if wider {
			add("library_ids", libraries(policy.LibraryIDs), libraries(limit.LibraryIDs))
		}
	}
	quality := func(q string) string {
		if q == "" {
			return unlimitedValue
		}
		return q
	}
	if p, l := NormalizePlaybackQuality(policy.MaxPlaybackQuality), NormalizePlaybackQuality(limit.MaxPlaybackQuality); l != "" && (p == "" || CompareQuality(p, l) > 0) {
		add("max_playback_quality", quality(p), quality(l))
	}
	capped := func(field string, p, l int) {
		if l > 0 && (p <= 0 || p > l) {
			render := func(n int) string {
				if n <= 0 {
					return unlimitedValue
				}
				return strconv.Itoa(n)
			}
			add(field, render(p), render(l))
		}
	}
	capped("max_streams", policy.MaxStreams, limit.MaxStreams)
	capped("max_transcodes", policy.MaxTranscodes, limit.MaxTranscodes)
	capped("max_remote_stream_bitrate_kbps", policy.MaxRemoteStreamBitrateKbps, limit.MaxRemoteStreamBitrateKbps)
	capped("max_local_stream_bitrate_kbps", policy.MaxLocalStreamBitrateKbps, limit.MaxLocalStreamBitrateKbps)
	if policy.MaxProfiles > limit.MaxProfiles {
		add("max_profiles", strconv.Itoa(policy.MaxProfiles), strconv.Itoa(limit.MaxProfiles))
	}
	allowed := func(field string, p, l bool) {
		if p && !l {
			add(field, "allowed", "not allowed")
		}
	}
	allowed("download_allowed", policy.DownloadAllowed, limit.DownloadAllowed)
	allowed("download_transcode_allowed", policy.DownloadTranscodeAllowed, limit.DownloadTranscodeAllowed)
	allowed("transcode_allowed", policy.TranscodeAllowed, limit.TranscodeAllowed)
	allowed("audio_transcode_allowed", policy.AudioTranscodeAllowed, limit.AudioTranscodeAllowed)
	allowed("requests_allowed", policy.RequestsAllowed, limit.RequestsAllowed)
	granted := map[string]bool{}
	for _, p := range limit.Permissions {
		granted[strings.TrimSpace(p)] = true
	}
	var extra []string
	for _, p := range policy.Permissions {
		if p = strings.TrimSpace(p); p != "" && !granted[p] && !slices.Contains(extra, p) {
			extra = append(extra, p)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		add("permissions", strings.Join(extra, ", "), "not granted")
	}
	return out
}
