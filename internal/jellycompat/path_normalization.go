package jellycompat

import (
	"context"
	"net/http"
	"strings"
)

type requestContextKey string

const originalPathKey requestContextKey = "jellycompat_original_path"

var compatPathSegments = map[string]string{
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
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
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
