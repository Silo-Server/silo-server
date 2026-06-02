package requests

import "github.com/Silo-Server/silo-server/internal/access"

// plannedTarget is a routing decision: which instance, at which quality, with
// which profile/folder/tags resolved (standard vs anime).
type plannedTarget struct {
	Instance Integration
	Quality  Quality
	IsAnime  bool
}

func integrationKindForMediaType(mediaType MediaType) string {
	if mediaType == MediaTypeSeries {
		return "sonarr"
	}
	return "radarr"
}

// routeTargets decides the fulfillment targets for an approved request.
// 1080p is always desired; 2160p is added when the requester's ceiling allows
// 4K OR force-dual is on. A quality is emitted only if its default instance
// exists for the kind.
func routeTargets(req Request, ceiling string, settings Settings, instances []Integration) []plannedTarget {
	kind := integrationKindForMediaType(req.MediaType)

	wants4K := settings.ForceDualQuality || access.CompareQuality(ceiling, access.PlaybackQuality4K) >= 0

	var hd, uhd *Integration
	for i := range instances {
		in := instances[i]
		if in.Kind != kind || !in.Enabled {
			continue
		}
		if in.IsDefault && hd == nil {
			hd = &instances[i]
		}
		if in.IsDefault4K && uhd == nil {
			uhd = &instances[i]
		}
	}

	var out []plannedTarget
	if hd != nil {
		out = append(out, plannedTarget{Instance: *hd, Quality: Quality1080p, IsAnime: req.IsAnime && hd.AnimeEnabled})
	}
	if wants4K && uhd != nil {
		out = append(out, plannedTarget{Instance: *uhd, Quality: Quality2160p, IsAnime: req.IsAnime && uhd.AnimeEnabled})
	}
	return out
}

// resolveInstance returns a copy of the instance with root folder / quality
// profile / tags (and Sonarr series_type) set for standard vs anime fulfillment.
func resolveInstance(pt plannedTarget) Integration {
	in := pt.Instance
	if in.Options == nil {
		in.Options = map[string]any{}
	} else {
		clone := make(map[string]any, len(in.Options))
		for k, v := range in.Options {
			clone[k] = v
		}
		in.Options = clone
	}
	if pt.IsAnime {
		// Anime fields are overrides: only replace the standard value when the
		// anime counterpart is set, so an admin can enable anime detection while
		// reusing the standard root folder / quality profile / tags for any field
		// they leave blank (rather than clearing them into an invalid submission).
		if in.AnimeRootFolder != "" {
			in.RootFolder = in.AnimeRootFolder
		}
		if in.AnimeQualityProfileID != nil {
			in.QualityProfileID = in.AnimeQualityProfileID
		}
		if len(in.AnimeTags) > 0 {
			in.Tags = in.AnimeTags
		}
		if in.Kind == "sonarr" {
			in.Options["series_type"] = "anime"
		}
	}
	return in
}
