package auth

import "sort"

func MergeACLPolicies(base EffectivePolicy, policies []ACLPolicy) EffectivePolicy {
	out := cloneEffectivePolicy(base)

	var (
		librarySet          map[int]struct{}
		mediaTypeSet        map[string]struct{}
		hasLibraryIDs       bool
		hasMediaTypes       bool
		hasMaxQuality       bool
		hasMaxStreams       bool
		hasMaxTranscodes    bool
		hasMaxProfiles      bool
		hasDirectDownloads  bool
		hasTransDownloads   bool
		maxPlaybackQuality  string
		maxStreams          int
		maxTranscodes       int
		maxProfiles         int
		directDownloads     bool
		transcodedDownloads bool
	)

	for _, policy := range policies {
		if policy.LibraryIDs != nil {
			hasLibraryIDs = true
			if librarySet == nil {
				librarySet = map[int]struct{}{}
			}
			for _, id := range policy.LibraryIDs {
				librarySet[id] = struct{}{}
			}
		}
		if policy.MediaTypes != nil {
			hasMediaTypes = true
			if mediaTypeSet == nil {
				mediaTypeSet = map[string]struct{}{}
			}
			for _, mediaType := range policy.MediaTypes {
				mediaTypeSet[mediaType] = struct{}{}
			}
		}
		if policy.MaxPlaybackQuality != "" {
			if !hasMaxQuality || playbackQualityMorePermissive(policy.MaxPlaybackQuality, maxPlaybackQuality) {
				maxPlaybackQuality = policy.MaxPlaybackQuality
			}
			hasMaxQuality = true
		}
		if policy.MaxStreams != nil {
			if !hasMaxStreams || *policy.MaxStreams > maxStreams {
				maxStreams = *policy.MaxStreams
			}
			hasMaxStreams = true
		}
		if policy.MaxTranscodes != nil {
			if !hasMaxTranscodes || *policy.MaxTranscodes > maxTranscodes {
				maxTranscodes = *policy.MaxTranscodes
			}
			hasMaxTranscodes = true
		}
		if policy.MaxProfiles != nil {
			if !hasMaxProfiles || *policy.MaxProfiles > maxProfiles {
				maxProfiles = *policy.MaxProfiles
			}
			hasMaxProfiles = true
		}
		if policy.DirectDownloadsAllowed != nil {
			directDownloads = directDownloads || *policy.DirectDownloadsAllowed
			hasDirectDownloads = true
		}
		if policy.TranscodedDownloadsAllowed != nil {
			transcodedDownloads = transcodedDownloads || *policy.TranscodedDownloadsAllowed
			hasTransDownloads = true
		}
	}

	if hasLibraryIDs {
		out.LibraryIDs = make([]int, 0, len(librarySet))
		for id := range librarySet {
			out.LibraryIDs = append(out.LibraryIDs, id)
		}
		sort.Ints(out.LibraryIDs)
	}
	if hasMediaTypes {
		out.MediaTypes = make([]string, 0, len(mediaTypeSet))
		for mediaType := range mediaTypeSet {
			out.MediaTypes = append(out.MediaTypes, mediaType)
		}
		sort.Strings(out.MediaTypes)
	}
	if hasMaxQuality {
		out.MaxPlaybackQuality = maxPlaybackQuality
	}
	if hasMaxStreams {
		out.MaxStreams = maxStreams
	}
	if hasMaxTranscodes {
		out.MaxTranscodes = maxTranscodes
	}
	if hasMaxProfiles {
		out.MaxProfiles = maxProfiles
	}
	if hasDirectDownloads {
		out.DirectDownloadsAllowed = directDownloads
	}
	if hasTransDownloads {
		out.TranscodedDownloadsAllowed = transcodedDownloads
	}
	return out
}

func playbackQualityMorePermissive(left, right string) bool {
	leftRank, leftOK := playbackQualityRank(left)
	rightRank, rightOK := playbackQualityRank(right)
	if !leftOK {
		return false
	}
	if !rightOK {
		return true
	}
	return leftRank > rightRank
}
