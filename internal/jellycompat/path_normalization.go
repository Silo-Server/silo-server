package jellycompat

import (
	"context"
	"net/http"
	"strings"
)

type requestContextKey string

const originalPathKey requestContextKey = "jellycompat_original_path"

var compatPathSegments = map[string]string{
	compatThemeAudioLower: compatThemeAudio,
	compatThemeUniversal:  compatThemeUniversal,

	"system":             "System",
	"info":               "Info",
	"public":             "Public",
	"branding":           "Branding",
	"configuration":      "Configuration",
	"quickconnect":       "QuickConnect",
	"enabled":            "Enabled",
	"users":              "Users",
	"authenticatebyname": "AuthenticateByName",
	"me":                 "Me",
	"views":              "Views",
	"items":              "Items",
	"latest":             "Latest",
	"suggestions":        "Suggestions",
	"similar":            "Similar",
	"thememedia":         "ThemeMedia",
	"themesongs":         "ThemeSongs",
	"specialfeatures":    "SpecialFeatures",
	"intros":             "Intros",
	"download":           "Download",
	"images":             "Images",
	"primary":            "Primary",
	"backdrop":           "Backdrop",
	"logo":               "Logo",
	"genres":             "Genres",
	"shows":              "Shows",
	"seasons":            "Seasons",
	"episodes":           "Episodes",
	"nextup":             "NextUp",
	"useritems":          "UserItems",
	"resume":             "Resume",
	"userdata":           "UserData",
	"userviews":          "UserViews",
	"userfavoriteitems":  "UserFavoriteItems",
	"userplayeditems":    "UserPlayedItems",
	"favoriteitems":      "FavoriteItems",
	"playeditems":        "PlayedItems",
	"search":             "Search",
	"hints":              "Hints",
	"sessions":           "Sessions",
	"capabilities":       "Capabilities",
	"full":               "Full",
	"videos":             "Videos",
	"hls":                "hls",
	"subtitles":          "Subtitles",
	"logout":             "Logout",
	"playing":            "Playing",
	"progress":           "Progress",
	"stopped":            "Stopped",
	"ping":               "Ping",
	"filters":            "Filters",
	"mediasegments":      "MediaSegments",
	"episode":            "Episode",
	"timestamps":         "Timestamps",
	"introtimestamps":    "IntroTimestamps",
	"playback":           "Playback",
	"bitratetest":        "BitrateTest",
	"playbackinfo":       "PlaybackInfo",
	"displaypreferences": "DisplayPreferences",
	"persons":            "Persons",
	"studios":            "Studios",
	"movies":             "Movies",
	"recommendations":    "Recommendations",
	"groupingoptions":    "GroupingOptions",
	"library":            "Library",
	"virtualfolders":     "VirtualFolders",
	"socket":             "socket",
}

func normalizeCompatPathMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		normalized := canonicalizeCompatPath(r.URL.Path)
		if normalized == r.URL.Path {
			next.ServeHTTP(w, r)
			return
		}

		ctx := context.WithValue(r.Context(), originalPathKey, r.URL.Path)
		clone := r.Clone(ctx)
		urlCopy := *clone.URL
		urlCopy.Path = normalized
		urlCopy.RawPath = normalized
		clone.URL = &urlCopy
		next.ServeHTTP(w, clone)
	})
}

func canonicalizeCompatPath(path string) string {
	if strings.HasPrefix(path, "/api/v2/artwork/") {
		return path
	}
	if path == "" || path == "/" {
		return path
	}

	parts := strings.Split(path, "/")
	if len(parts) > 1 {
		switch strings.ToLower(parts[1]) {
		case "emby", "jellyfin":
			parts = append([]string{""}, parts[2:]...)
		}
	}
	// Match discovery routes as whole paths: these words can also be opaque
	// IDs (for example /DisplayPreferences/list), whose case must not change.
	switch strings.ToLower(strings.Join(parts, "/")) {
	case "/localization/cultures":
		return "/Localization/Cultures"
	case "/syncplay/list":
		return "/SyncPlay/List"
	}
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			continue
		}
		// LocalTrailers is matched only in its route position,
		// /Items/{id}/LocalTrailers, so a DisplayPreferences id or other
		// opaque value that happens to read "localtrailers" keeps its case.
		if i >= 3 && strings.EqualFold(part, "localtrailers") && strings.EqualFold(parts[i-2], "items") {
			parts[i] = "LocalTrailers"
			continue
		}
		parts[i] = canonicalizeCompatSegment(part)
	}
	return strings.Join(parts, "/")
}

func canonicalizeCompatSegment(segment string) string {
	lower := strings.ToLower(segment)
	if mapped, ok := compatPathSegments[lower]; ok {
		return mapped
	}
	if lower == "master.m3u8" {
		return lower
	}
	if strings.HasPrefix(lower, "stream.") {
		return lower
	}
	return segment
}

func originalPathFromContext(ctx context.Context) string {
	path, _ := ctx.Value(originalPathKey).(string)
	return path
}
