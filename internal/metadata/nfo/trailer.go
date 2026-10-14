package nfo

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	videoSiteYouTube = "youtube"
	videoSiteVimeo   = "vimeo"
)

var (
	youTubeVideoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	vimeoVideoIDRe   = regexp.MustCompile(`^\d+$`)
)

// trailerVideos maps <trailer> entries to remote trailer videos. Kodi's
// scrapers write a YouTube trailer as a YouTube add-on link
// (plugin://plugin.video.youtube/?action=play_video&videoid=ID, or the newer
// /play/?video_id=ID); other writers use a plain YouTube or Vimeo web link.
// Each becomes a video on its site, as Jellyfin turns the add-on link into a
// YouTube link. A link to any other site is dropped: a video is stored as a
// site and a key, and clients only play the sites they know.
func trailerVideos(values []string) []metadata.RemoteVideo {
	var videos []metadata.RemoteVideo
	seen := make(map[string]struct{})
	for _, raw := range values {
		site, key := trailerSiteKey(raw)
		if key == "" {
			continue
		}
		providerKey := site + ":" + key
		if _, dup := seen[providerKey]; dup {
			continue
		}
		seen[providerKey] = struct{}{}
		videos = append(videos, metadata.RemoteVideo{
			Provider:    providerSlug,
			ProviderKey: providerKey,
			Kind:        models.ExtraKindTrailer,
			Site:        site,
			SiteKey:     key,
			Name:        "Trailer",
		})
	}
	return videos
}

// trailerSiteKey returns the site and video key a trailer link names, or
// empty strings.
func trailerSiteKey(raw string) (site, key string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", ""
	}
	host := strings.ToLower(u.Hostname())
	segments := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	switch strings.ToLower(u.Scheme) {
	case "plugin":
		if host != "plugin.video.youtube" {
			return "", ""
		}
		query := u.Query()
		return youTubeKey(firstNonBlank(query.Get("videoid"), query.Get("video_id")))
	case "http", "https":
	default:
		return "", ""
	}
	switch {
	case host == "youtu.be":
		if len(segments) > 0 {
			return youTubeKey(segments[0])
		}
	case hostIs(host, "youtube.com"), hostIs(host, "youtube-nocookie.com"):
		if v := u.Query().Get("v"); v != "" {
			return youTubeKey(v)
		}
		// /embed/ID, /v/ID, /shorts/ID
		if len(segments) == 2 {
			switch strings.ToLower(segments[0]) {
			case "embed", "v", "shorts":
				return youTubeKey(segments[1])
			}
		}
	case hostIs(host, "vimeo.com"):
		// vimeo.com/ID, player.vimeo.com/video/ID
		if len(segments) > 0 && vimeoVideoIDRe.MatchString(segments[len(segments)-1]) {
			return videoSiteVimeo, segments[len(segments)-1]
		}
	}
	return "", ""
}

func youTubeKey(id string) (site, key string) {
	id = strings.TrimSpace(id)
	if !youTubeVideoIDRe.MatchString(id) {
		return "", ""
	}
	return videoSiteYouTube, id
}
