package apiv2

// Wire types of the Devices & Quality breakdowns added to
// getAdminInsightsDevices: app versions, the Silo vs Jellyfin-app split, and
// the subtitles and audio sessions played.

type AdminInsightsClientVersionRow struct {
	ClientName       string          `json:"client_name"`
	DevicePlatform   string          `json:"device_platform"`
	ClientVersion    string          `json:"client_version"`
	IsJellyfinClient bool            `json:"is_jellyfin_client"`
	Sessions         int64           `json:"sessions"`
	LastSeenAt       NullableInstant `json:"last_seen_at"`
}

type AdminInsightsProtocolBucket struct {
	BucketStart      Instant `json:"bucket_start"`
	LabelDate        string  `json:"label_date" format:"date" doc:"Local bucket start date (YYYY-MM-DD in requested tz)"`
	NativeSessions   int64   `json:"native_sessions"`
	JellyfinSessions int64   `json:"jellyfin_sessions"`
}

type AdminInsightsSubtitleUsage struct {
	Sessions                int64                       `json:"sessions" doc:"Sessions with route facts, the ones that record their subtitle"`
	SubtitledSessions       int64                       `json:"subtitled_sessions"`
	ForcedSessions          int64                       `json:"forced_sessions"`
	HearingImpairedSessions int64                       `json:"hearing_impaired_sessions"`
	Languages               []AdminInsightsLabeledCount `json:"languages" doc:"Subtitled sessions by subtitle language (ISO 639-1 where one exists, unknown when untagged)"`
	Deliveries              []AdminInsightsLabeledCount `json:"deliveries" doc:"Subtitled sessions by delivery: embedded, sidecar, hls_rendition or burn_in"`
}

type AdminInsightsAudioUsage struct {
	Sessions          int64                       `json:"sessions" doc:"Sessions with route facts, the ones that record their audio track"`
	Languages         []AdminInsightsLabeledCount `json:"languages" doc:"Sessions by the played audio track's language"`
	Decisions         []AdminInsightsLabeledCount `json:"decisions" doc:"Sessions by audio handling: direct, remux (copied into a new container) or transcode"`
	DeliveredChannels []AdminInsightsLabeledCount `json:"delivered_channels" doc:"Sessions by delivered channel layout: mono, stereo, surround or unknown"`
}
