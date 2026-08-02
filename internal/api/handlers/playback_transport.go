package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// startLocalPlaybackTransport is the shared local ffmpeg launch primitive for
// legacy and protocol-v3 orchestration. Callers retain ownership of lifecycle
// locking and decide whether registration is immediate or transactionally
// staged.
func (h *PlaybackHandler) startLocalPlaybackTransport(ctx context.Context, opts playback.TranscodeOpts) (*playback.TranscodeSession, error) {
	session, err := h.startLocalPlaybackTransportOnce(ctx, opts)
	if err == nil {
		return session, nil
	}
	if playback.IsHardwareTranscode(opts.HWAccel) {
		slog.WarnContext(ctx, "hardware transcode transport failed; falling back to software",
			"component", "api",
			"hw_accel", opts.HWAccel,
			"session_id", opts.SessionID,
			"error", err,
		)
		swOpts := opts
		swOpts.HWAccel = "none"
		swSession, swErr := h.startLocalPlaybackTransportOnce(ctx, swOpts)
		if swErr == nil {
			return swSession, nil
		}
	}
	return nil, err
}

func (h *PlaybackHandler) startLocalPlaybackTransportOnce(ctx context.Context, opts playback.TranscodeOpts) (*playback.TranscodeSession, error) {
	if !strings.HasPrefix(strings.ToLower(opts.InputPath), virtualPlaybackPrefix) {
		session, startErr := playback.StartTranscode(context.WithoutCancel(ctx), opts)
		if startErr != nil {
			return nil, startErr
		}
		if _, readyErr := session.WaitForManifest(8 * time.Second); readyErr != nil {
			_ = session.Close()
			return nil, readyErr
		}
		return session, nil
	}
	if opts.MediaFileID <= 0 || h.fileResolver == nil {
		return nil, errors.New("virtual transcode input is missing its media file identity")
	}
	file, err := h.fileResolver.GetByID(ctx, opts.MediaFileID)
	if err != nil || file == nil {
		return nil, fmt.Errorf("load virtual transcode input: %w", err)
	}
	userID, profileID := 0, ""
	ownerInstallationID := file.VirtualOwnerInstallationID
	if session, sessionErr := h.sessionMgr.GetSession(opts.SessionID); sessionErr == nil && session != nil {
		userID, profileID = session.UserID, session.ProfileID
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(session.VirtualSourceURI)), virtualPlaybackPrefix) {
			copy := *file
			copy.FilePath = session.VirtualSourceURI
			if session.VirtualSourceOwnerInstallationID > 0 {
				copy.VirtualOwnerInstallationID = session.VirtualSourceOwnerInstallationID
			}
			file = &copy
			ownerInstallationID = file.VirtualOwnerInstallationID
			opts.InputPath = file.FilePath
		}
	}
	canonicalPath := opts.InputPath
	opts.CanonicalInputPath = canonicalPath
	opts.VirtualSourceOwnerInstallationID = ownerInstallationID
	opts.RefreshInput = func(refreshCtx context.Context) (string, func(), error) {
		return h.resolveVirtualInputURI(
			refreshCtx, canonicalPath, ownerInstallationID,
			userID, profileID, true,
		)
	}
	var lastErr error
	startupCtx, startupCancel := context.WithTimeout(ctx, virtualStartupBudget)
	defer startupCancel()
	for attempt := 0; attempt < 2; attempt++ {
		relayURL, cleanup, resolveErr := h.resolveVirtualInputURI(
			startupCtx, canonicalPath, ownerInstallationID, userID, profileID, attempt > 0,
		)
		if resolveErr != nil {
			lastErr = resolveErr
			continue
		}
		attemptOpts := opts
		attemptOpts.InputPath = relayURL
		attemptOpts.InputCleanup = cleanup
		session, startErr := playback.StartTranscode(context.WithoutCancel(startupCtx), attemptOpts)
		if startErr == nil {
			if _, readyErr := session.WaitForManifest(20 * time.Second); readyErr == nil {
				return session, nil
			} else {
				startErr = readyErr
			}
			_ = session.Close()
		} else if cleanup != nil {
			cleanup()
		}
		lastErr = startErr
	}
	if lastErr == nil {
		lastErr = errors.New("virtual transcode provider returned no usable stream")
	}
	return nil, lastErr
}

func (h *PlaybackHandler) resolveVirtualInput(ctx context.Context, file *models.MediaFile, userID int, profileID string, forceRefresh bool) (string, func(), error) {
	if file == nil || !isVirtualPlaybackFile(file) {
		return "", nil, errors.New("virtual media file is required")
	}
	return h.resolveVirtualInputURI(
		ctx, file.FilePath, file.VirtualOwnerInstallationID,
		userID, profileID, forceRefresh,
	)
}

func (h *PlaybackHandler) resolveVirtualInputURI(
	ctx context.Context,
	virtualURI string,
	ownerInstallationID int,
	userID int,
	profileID string,
	forceRefresh bool,
) (string, func(), error) {
	var inputPath string
	var err error
	if forceRefresh && h.VirtualMediaRefreshResolver != nil {
		inputPath, err = h.VirtualMediaRefreshResolver.RefreshVirtualMedia(
			ctx, virtualURI, ownerInstallationID, userID, profileID,
		)
	} else {
		inputPath, err = resolveVirtualMediaPath(
			ctx, h.VirtualMediaResolver, virtualURI,
			ownerInstallationID, userID, profileID,
		)
	}
	if err != nil {
		return "", nil, fmt.Errorf("resolve virtual input: %w", err)
	}
	// Feed FFmpeg the resolved provider URL directly instead of routing it
	// through the loopback relay (see playback regression on 2026-08-01).
	return inputPath, nil, nil
}

// startRemotePlaybackTransport is the shared remote-node launch primitive.
// It returns the node's HTTP status separately so legacy and v3 can preserve
// their existing public error envelopes while executing identical transport
// startup and response parsing.
func (h *PlaybackHandler) startRemotePlaybackTransport(ctx context.Context, nodeURL string, request transcodenode.TranscodeStartRequest) (transcodenode.TranscodeStartResponse, int, error) {
	if strings.HasPrefix(strings.ToLower(request.InputPath), virtualPlaybackPrefix) {
		return transcodenode.TranscodeStartResponse{}, 0, errors.New("virtual sources require an integrated transcode transport")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return transcodenode.TranscodeStartResponse{}, 0, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, nodeURL+"/transcode/start", bytes.NewReader(body))
	if err != nil {
		return transcodenode.TranscodeStartResponse{}, 0, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+h.JWTSecret)
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return transcodenode.TranscodeStartResponse{}, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		// Drain the (small) error body so the transport can reuse the
		// connection instead of tearing it down on every failed start.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return transcodenode.TranscodeStartResponse{}, response.StatusCode, nil
	}
	var result transcodenode.TranscodeStartResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		slog.WarnContext(ctx, "remote transcode start response decode failed", "component", "api", "node", nodeURL, "error", err)
	}
	return result, response.StatusCode, nil
}

func fetchRemoteTranscodeCapabilities(ctx context.Context, nodeURL, jwtSecret string) (playback.HWAccelInfo, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(nodeURL, "/")+"/hw-capabilities", nil)
	if err != nil {
		return playback.HWAccelInfo{}, err
	}
	request.Header.Set("Authorization", "Bearer "+jwtSecret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return playback.HWAccelInfo{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return playback.HWAccelInfo{}, fmt.Errorf("node returned %d", response.StatusCode)
	}
	var info playback.HWAccelInfo
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		return playback.HWAccelInfo{}, err
	}
	info.Source = "transcode_node"
	info.NodeURL = nodeURL
	return info, nil
}
