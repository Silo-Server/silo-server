package handlers

import (
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

var (
	errSubtitleIdentityInvalid     = errors.New("invalid subtitle identity")
	errSubtitleIdentityUnavailable = errors.New("the selected subtitle identity is unavailable")
)

// subtitleRouteIndex resolves a pinned identity before applying the combined
// ordinal dispatch. Old URLs without a pin retain their original behavior.
func subtitleRouteIndex(file *models.MediaFile, index int, query url.Values) (int, error) {
	pins := 0
	for _, key := range []string{
		playback.EmbeddedSubtitleStreamIndexParamV3,
		playback.ExternalSubtitleKeyParamV3,
		playback.DownloadedSubtitleIDParamV3,
	} {
		if values, ok := query[key]; ok {
			if len(values) != 1 || values[0] == "" {
				return 0, errSubtitleIdentityInvalid
			}
			pins++
		}
	}
	if pins > 1 {
		return 0, errSubtitleIdentityInvalid
	}
	if value := query.Get(playback.EmbeddedSubtitleStreamIndexParamV3); value != "" {
		streamIndex, err := strconv.Atoi(value)
		if err != nil || streamIndex < 0 {
			return 0, errSubtitleIdentityInvalid
		}
		match := -1
		for ordinal, track := range file.SubtitleTracks {
			if track.Index == streamIndex {
				if match >= 0 {
					return 0, errSubtitleIdentityUnavailable
				}
				match = len(file.ExternalSubtitles) + ordinal
			}
		}
		if match < 0 {
			return 0, errSubtitleIdentityUnavailable
		}
		return match, nil
	}
	if key := query.Get(playback.ExternalSubtitleKeyParamV3); key != "" {
		if _, err := hex.DecodeString(key); len(key) != 64 || err != nil {
			return 0, errSubtitleIdentityInvalid
		}
		match := -1
		for ordinal, subtitle := range file.ExternalSubtitles {
			if playback.ExternalSubtitlePathKeyV3(subtitle.Path) == key {
				if match >= 0 {
					return 0, errSubtitleIdentityUnavailable
				}
				match = ordinal
			}
		}
		if match < 0 {
			return 0, errSubtitleIdentityUnavailable
		}
		return match, nil
	}
	return index, nil
}
