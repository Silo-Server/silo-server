package apiv2

import "github.com/Silo-Server/silo-server/internal/playback"

// PlaybackSessionSubtitle is the subtitle a playback's plan delivers, in the
// names the playback plan's subtitle decision and inventory use. It is what
// the server planned: a client that turns an already delivered sidecar on or
// off without a replan changes what is on screen and not this.
type PlaybackSessionSubtitle struct {
	TrackID         string  `json:"track_id" doc:"The track's inventory track_id, as the plan's subtitle.track_id names it" example:"file:42:subtitle:2"`
	Language        *string `json:"language" nullable:"true" doc:"The track's language as the inventory names it (BCP 47; older rows may carry a legacy value); null when unknown" example:"en"`
	Codec           *string `json:"codec" nullable:"true" doc:"The track's format in its source, as the inventory's codec names it; null when unknown" example:"subrip"`
	Format          *string `json:"format" nullable:"true" doc:"Format the client receives the track in: the sidecar artifact's or the HLS rendition's format. Null when it is burned in or carried in the stream as the source has it" example:"vtt"`
	Source          string  `json:"source" enum:"embedded,external,downloaded" doc:"Where the track comes from, as the inventory's source names it: a track in the media file, a subtitle file beside it, or one the server downloaded or stored"`
	Delivery        string  `json:"delivery" enum:"burn_in,embedded,hls_rendition,sidecar" doc:"How the plan delivers it: burned into the video (subtitle.mode burn_in), carried in the stream the client plays (subtitle.embedded), a WebVTT rendition of the HLS stream (subtitle.hls_rendition), or a sidecar file the client fetches (subtitle.artifact). Clients ignore values they do not know"`
	Forced          bool    `json:"forced"`
	HearingImpaired bool    `json:"hearing_impaired"`
}

func playbackSessionSubtitleOf(s *playback.SessionSubtitle) NullableObject[PlaybackSessionSubtitle] {
	if s == nil {
		return NullableObject[PlaybackSessionSubtitle]{}
	}
	return NullableObject[PlaybackSessionSubtitle]{&PlaybackSessionSubtitle{
		TrackID: s.TrackID, Language: optionalString(s.Language), Codec: optionalString(s.Codec),
		Format: optionalString(s.Format), Source: s.Source, Delivery: s.Delivery,
		Forced: s.Forced, HearingImpaired: s.HearingImpaired,
	}}
}

// optionalString is s, or nil when it is empty.
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
