package onboarding

// Step IDs referenced by more than one place (tests, handoff routing).
const (
	StepIDWelcome         = "welcome"
	StepIDPlaybackQuality = "playback-quality"
	StepIDWatchTogether   = "watch-together"
	StepIDRequests        = "requests"
	StepIDRecommendations = "recommendations"
	StepIDApps            = "apps"
	StepIDJellyfinCompat  = "jellyfin-compat"
	StepIDHandoffTaste    = "handoff-taste"
)

// valueAuto is the shared "auto" default for playback and subtitle choices.
const valueAuto = "auto"

// tourSteps is the core-2026-07 tour, in display order. Copy lives here on
// the server so a wording fix is a deploy, not three client releases.
var tourSteps = []Step{
	{
		ID:           StepIDWelcome,
		Kind:         KindWelcome,
		Title:        "Silo isn't quite like the others",
		Body:         "You've probably used Plex or Jellyfin. Most of this will feel familiar — but a handful of things work differently here, and they're worth two minutes.",
		Illustration: "welcome",
	},
	{
		ID:    StepIDPlaybackQuality,
		Kind:  KindSettingChoice,
		Title: "Pick a quality ceiling now, change it anywhere",
		Body:  "Silo won't burn your data plan guessing. Set a ceiling and it sticks. You can override it per device, per library, or per show later.",
		Setting: &SettingSpec{
			Target:  TargetProfileField,
			Key:     "quality_preference",
			Control: "segmented",
			Options: []SettingOption{
				{Value: valueAuto, Label: "Auto"},
				{Value: "1080p", Label: "1080p"},
				{Value: "4k", Label: "4K"},
			},
			Default: valueAuto,
		},
		needsInput: true,
	},
	{
		ID:    "subtitles",
		Kind:  KindSettingChoice,
		Title: "Subtitles: set them once, everywhere",
		Body:  "Language and appearance follow your profile across every device. Style them under Settings → Subtitles.",
		Setting: &SettingSpec{
			Target:  TargetProfileField,
			Key:     "subtitle_mode",
			Control: "select",
			Options: []SettingOption{
				{Value: valueAuto, Label: "Only for foreign audio"},
				{Value: "always", Label: "Always on"},
				{Value: "off", Label: "Off unless I turn them on"},
			},
			Default: valueAuto,
		},
		needsInput: true,
	},
	{
		ID:           "favorites-watchlist",
		Kind:         KindFeatureCard,
		Title:        "Favorites teach, your Watchlist remembers",
		Body:         "Favorites tell Silo what you love and shape your recommendations. Your Watchlist is the get-to-it-eventually pile — and Silo tells you when something on it arrives. Both live in the sidebar under Your Stuff.",
		Illustration: "watchlist",
	},
	{
		ID:           StepIDWatchTogether,
		Kind:         KindFeatureCard,
		Title:        "Watch Party: same movie, different couches",
		Body:         "Start a Watch Party, send the link, and play, pause, and seek stay in sync for everyone — no browser extension, no screen share. You'll find it in the sidebar whenever you want a movie night.",
		Illustration: StepIDWatchTogether,
		Route:        "/rooms/join",
		ActionLabel:  "Open Watch Party",
		gate:         gateWatchTogether,
	},
	{
		ID:           StepIDRequests,
		Kind:         KindFeatureCard,
		Title:        "Requests: ask for what's missing",
		Body:         "The Requests page lets you browse everything — not just what's on this server — and request what you want. The admin sees it all in one place instead of a text message.",
		Illustration: StepIDRequests,
		Route:        "/requests",
		ActionLabel:  "Open Requests",
		gate:         gateRequests,
	},
	{
		ID:           StepIDRecommendations,
		Kind:         KindFeatureCard,
		Title:        "Recommendations from taste, not popularity",
		Body:         "Silo learns from what you actually watch and favorite on this server — not a global chart. The more you use it, the better the home screen gets.",
		Illustration: StepIDRecommendations,
		gate:         gateRecommendations,
	},
	{
		ID:           "calendar-notifications",
		Kind:         KindFeatureCard,
		Title:        "Calendar and Notifications: know when things land",
		Body:         "The Calendar shows what's coming; Notifications tell you when it arrives — new episodes of shows you follow, Watchlist arrivals, request updates. Email or push, your call.",
		Illustration: "calendar",
		Route:        "/calendar",
		ActionLabel:  "Open Calendar",
		gate:         gateNotifications,
	},
	{
		ID:           StepIDApps,
		Kind:         KindFeatureCard,
		Title:        "Take Silo with you",
		Body:         "Silo has native apps for iPhone, iPad, Apple TV, Android, and Android TV. Sign in with this same email address and your profile, Watchlist, and watch progress follow you to every screen.",
		Illustration: StepIDApps,
		Links: []StepLink{
			{Label: "iPhone & Apple TV", URL: "https://testflight.apple.com/join/XZy8cu5q"},
			{Label: "Android & Android TV", URL: "https://play.google.com/store/apps/details?id=org.siloserver.silo"},
		},
		webOnly: true,
	},
	{
		ID:           StepIDJellyfinCompat,
		Kind:         KindFeatureCard,
		Title:        "Already use a Jellyfin app? It works here",
		Body:         "Silo speaks the Jellyfin API, so players like Infuse, VidHub, Findroid, and Swiftfin can connect to this server directly — add it as a Jellyfin server with this address and your Silo sign-in.",
		Illustration: "jellyfin",
		webOnly:      true,
		gate:         gateJellyfinCompat,
	},
	{
		ID:    StepIDHandoffTaste,
		Kind:  KindHandoff,
		Title: "Pick a few favorites",
		Body:  "Choose a handful of titles you love so your recommendations aren't starting cold.",
		Route: "/taste-seed",
	},
}
