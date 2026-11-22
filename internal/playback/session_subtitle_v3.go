package playback

// How a session's selected subtitle reaches the screen, named for the plan's
// subtitle decision: burned into the video, carried in the stream the client
// plays as the source has it (subtitle.embedded), a WebVTT rendition of the
// HLS stream (subtitle.hls_rendition), or a sidecar the client fetches
// (subtitle.artifact).
const (
	SessionSubtitleDeliveryBurnIn       = "burn_in"
	SessionSubtitleDeliveryEmbedded     = "embedded"
	SessionSubtitleDeliveryHLSRendition = "hls_rendition"
	SessionSubtitleDeliverySidecar      = "sidecar"
)

// SessionSubtitle is the subtitle a session's current plan delivers, as the
// admin views report it. It describes the server's last plan: a client that
// turns an already delivered sidecar on or off without a replan changes what
// is on screen without changing this.
type SessionSubtitle struct {
	// TrackID is the inventory track_id the plan selected.
	TrackID  string `json:"track_id"`
	Language string `json:"language,omitempty"`
	// Codec is the track's format in its source; Format the format the
	// client receives, set only for a sidecar or an HLS rendition.
	Codec           string `json:"codec,omitempty"`
	Format          string `json:"format,omitempty"`
	Source          string `json:"source"`
	Delivery        string `json:"delivery"`
	Forced          bool   `json:"forced"`
	HearingImpaired bool   `json:"hearing_impaired"`
}

// SessionSubtitleFromPlanV3 is the subtitle a plan delivers, or nil when it
// shows none.
func SessionSubtitleFromPlanV3(plan *PlanV3) *SessionSubtitle {
	if plan == nil || plan.Subtitle.Mode == SubtitleOffV3 || plan.Subtitle.TrackID == "" {
		return nil
	}
	decision := plan.Subtitle
	out := &SessionSubtitle{TrackID: decision.TrackID}
	switch {
	case decision.Mode == SubtitleBurnInV3:
		out.Delivery = SessionSubtitleDeliveryBurnIn
	case decision.Embedded != nil:
		out.Delivery = SessionSubtitleDeliveryEmbedded
	case decision.HLSRendition != nil:
		out.Delivery, out.Format = SessionSubtitleDeliveryHLSRendition, decision.HLSRendition.Format
	case decision.Artifact != nil:
		out.Delivery, out.Format = SessionSubtitleDeliverySidecar, decision.Artifact.Format
	default:
		return nil
	}
	for _, track := range decision.Inventory {
		if track.TrackID == decision.TrackID {
			out.Language, out.Codec, out.Source = track.Language, track.Codec, track.Source
			out.Forced, out.HearingImpaired = track.Forced, track.HearingImpaired
			return out
		}
	}
	// A plan always lists the track it selects; one that does not names
	// nothing the views could show.
	return nil
}
