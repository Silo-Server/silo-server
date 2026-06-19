package downloads

import (
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// FormatPolicyResolver decides whether a requested download format is permitted
// and returns the concrete format to record.
//
// The decision tree (per the design):
//  1. original  -> allowed whenever downloads are enabled and user.DownloadAllowed.
//  2. remux     -> allowed whenever downloads are enabled and user.DownloadAllowed.
//  3. transcode -> requires cfg.TranscodeEnabled (server) AND
//     user.DownloadTranscodeAllowed; server gate off -> ErrTranscodeDisabled,
//     user flag off -> ErrDownloadNotAllowed.
//
// The caller is responsible for the downloads-enabled and user.DownloadAllowed
// checks before reaching create; Resolve enforces the per-format gates on top.
//
// Target codec/resolution selection for remux/transcode (via playback.SelectVersion
// against device capabilities) lands with the prepare-to-file pipeline in Phase 3;
// Resolve only classifies and gates the requested format here.
type FormatPolicyResolver struct{}

// Resolve returns the concrete format to record, or an error if the request is
// not permitted. An empty requested format defaults to original.
func (FormatPolicyResolver) Resolve(requested string, user *models.User, cfg config.DownloadConfig) (string, error) {
	switch requested {
	case "", FormatOriginal:
		return FormatOriginal, nil
	case FormatRemux:
		return FormatRemux, nil
	case FormatTranscode:
		if !cfg.TranscodeEnabled {
			return "", ErrTranscodeDisabled
		}
		if !user.DownloadTranscodeAllowed {
			return "", ErrDownloadNotAllowed
		}
		return FormatTranscode, nil
	default:
		return "", ErrInvalidFormat
	}
}
