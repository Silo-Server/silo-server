package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
)

const virtualPlaybackPrefix = "virtual://"

const maxVirtualPlaybackStreams = 50

type VirtualPlaybackResolver interface {
	ResolveVirtualPlayback(ctx context.Context, virtualPath string, userID int, profileID string) (string, error)
}

// VirtualPlaybackStream is the provider-neutral candidate shape used by the
// just-in-time picker. Implementations must never expose provider URLs here.
type VirtualPlaybackStream struct {
	ID                  string   `json:"id"`
	Label               string   `json:"label"`
	URI                 string   `json:"uri"`
	Resolution          string   `json:"resolution,omitempty"`
	CodecVideo          string   `json:"codec_video,omitempty"`
	CodecAudio          string   `json:"codec_audio,omitempty"`
	HDR                 string   `json:"hdr,omitempty"`
	SourceType          string   `json:"source_type,omitempty"`
	FileSize            int64    `json:"file_size,omitempty"`
	Container           string   `json:"container,omitempty"`
	Bitrate             int      `json:"bitrate,omitempty"`
	AudioLanguages      []string `json:"audio_languages,omitempty"`
	SubtitleLanguages   []string `json:"subtitle_languages,omitempty"`
	OwnerInstallationID int      `json:"-"`
}

type VirtualPlaybackStreamLister interface {
	ListVirtualPlaybackStreams(ctx context.Context, virtualPath string) ([]VirtualPlaybackStream, error)
}

type VirtualPlaybackStreamListerFunc func(context.Context, string) ([]VirtualPlaybackStream, error)

func (f VirtualPlaybackStreamListerFunc) ListVirtualPlaybackStreams(ctx context.Context, path string) ([]VirtualPlaybackStream, error) {
	return f(ctx, path)
}

// VirtualPlaybackStreamSink persists JIT candidates as selectable virtual
// files.
type VirtualPlaybackStreamSink func(context.Context, *models.MediaFile, []VirtualPlaybackStream) error

func isVirtualPlaybackFile(file *models.MediaFile) bool {
	return file != nil && strings.HasPrefix(file.FilePath, virtualPlaybackPrefix)
}

func isHLSVirtualStreamURL(streamURL string) bool {
	u, err := url.Parse(streamURL)
	if err != nil {
		return strings.Contains(strings.ToLower(streamURL), ".m3u8")
	}
	return strings.Contains(strings.ToLower(u.Path), ".m3u8") || strings.Contains(strings.ToLower(u.RawQuery), ".m3u8")
}

func (h *PlaybackHandler) resolveVirtualPlayback(r *http.Request, file *models.MediaFile, profileID string) (string, error) {
	if !isVirtualPlaybackFile(file) {
		return "", nil
	}
	if h.VirtualPlaybackResolver == nil {
		return "", errors.New("virtual playback resolver is not configured")
	}
	return h.VirtualPlaybackResolver.ResolveVirtualPlayback(r.Context(), file.FilePath, apimw.GetUserID(r.Context()), profileID)
}

func (h *PlaybackHandler) loadVirtualPlaybackCandidates(ctx context.Context, file *models.MediaFile) {
	if h.VirtualPlaybackStreamLister == nil || h.VirtualPlaybackStreamSink == nil || file == nil {
		return
	}
	// A result URI is already an explicit selection and its sibling results were
	// populated when the parent profile/base URI was played. Profile URIs still
	// need listing: the resolver has already fetched the provider response, and
	// the plugin returns that cached response without another provider request.
	if parsed, err := url.Parse(file.FilePath); err == nil {
		query := parsed.Query()
		if strings.TrimSpace(query.Get("result")) != "" {
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	// ResolveVirtualPlayback has already performed the provider request. Keep
	// this bounded and synchronous so the response contains the extra versions
	// immediately; the plugin-side cache makes this listing a host-local call.
	listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	streams, err := h.VirtualPlaybackStreamLister.ListVirtualPlaybackStreams(listCtx, file.FilePath)
	if err != nil || len(streams) == 0 {
		return
	}
	if len(streams) > maxVirtualPlaybackStreams {
		streams = streams[:maxVirtualPlaybackStreams]
	}
	_ = h.VirtualPlaybackStreamSink(listCtx, file, streams)
}
