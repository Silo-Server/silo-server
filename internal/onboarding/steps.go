package onboarding

// Step IDs referenced by more than one place (tests, handoff routing).
const (
	StepIDWelcome         = "welcome"
	StepIDPlaybackQuality = "playback-quality"
	StepIDWatchTogether   = "watch-together"
	StepIDRequests        = "requests"
	StepIDRecommendations = "recommendations"
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
		Title:        "Favorites teach, the watchlist remembers",
		Body:         "Favorites tell Silo what you love and shape your recommendations. The watchlist is the get-to-it-eventually pile — and Silo tells you when something on it arrives.",
		Illustration: "watchlist",
	},
	{
		ID:           StepIDWatchTogether,
		Kind:         KindFeatureCard,
		Title:        "Same movie, different couches",
		Body:         "Start a room and send the link. Play, pause, and seek stay in sync for everyone — no browser extension, no screen share.",
		Illustration: StepIDWatchTogether,
		Route:        "/rooms/join",
		ActionLabel:  "Show me",
		gate:         gateWatchTogether,
	},
	{
		ID:           StepIDRequests,
		Kind:         KindFeatureCard,
		Title:        "Ask for what's missing",
		Body:         "Browse everything — not just what's on this server — and request what you want. The admin sees requests in one place instead of a text message.",
		Illustration: StepIDRequests,
		Route:        "/requests",
		ActionLabel:  "Browse",
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
		Title:        "Know when things land",
		Body:         "The calendar shows what's coming; notifications tell you when it arrives — new episodes of what you follow, watchlist arrivals, request updates. Email or push, your call.",
		Illustration: "calendar",
		Route:        "/calendar",
		ActionLabel:  "Open calendar",
		gate:         gateNotifications,
	},
	{
		ID:    StepIDHandoffTaste,
		Kind:  KindHandoff,
		Title: "Pick a few favorites",
		Body:  "Choose a handful of titles you love so your recommendations aren't starting cold.",
		Route: "/taste-seed",
	},
}
