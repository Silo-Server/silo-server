import { useCallback, useEffect, useRef, useState } from "react";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import { PlayerFetchError, playerFetch } from "../player-fetch";
import {
  useCodecDetection,
  QUALITY_TO_RESOLUTION,
  RESOLUTION_ORDER,
  matchByTraits,
  getPlaybackEnvironmentSnapshot,
} from "./useCodecDetection";
import type {
  ChangeAudioResponse,
  PlaybackSessionPlaybackInfo,
  PlaybackSessionResponse,
  PlayMethod,
  PlayerFileVersion,
  PlayerPlaybackVariant,
  PlayerSubtitleInfo,
  ResumeHints,
} from "../types";

interface PlaybackSessionState {
  streamUrl: string | null;
  playMethod: PlayMethod | null;
  sessionId: string | null;
  mediaFileId: number | null;
  initialPosition: number;
  audioTrackIndex: number;
  durationSeconds: number | null;
  subtitleUrls: PlayerSubtitleInfo[];
  playbackInfo: PlaybackSessionPlaybackInfo | null;
  loading: boolean;
  replacing: boolean;
  errorTitle: string | null;
  error: string | null;
}

interface PlaybackSessionErrorState {
  title: string;
  message: string;
}

interface DownloadedSubtitle {
  id: number;
  media_file_id: number;
  provider: string;
  language: string;
  format: string;
  release_name: string;
  score: number;
  hearing_impaired: boolean;
}

interface UsePlaybackSessionResult extends PlaybackSessionState {
  switchVersion: (fileId: number, currentPosition: number) => void;
  switchAudioTrack: (index: number, currentPosition: number) => void;
  refreshSubtitles: () => void;
}

export interface StartPlaybackRequestPayload {
  file_id: number;
  profile_id: string;
  start_position?: number;
  audio_track_index?: number;
  codecs_video: string[];
  codecs_audio: string[];
  containers: string[];
  max_resolution: string;
  hdr: boolean;
}

export function buildStartPlaybackRequestPayload({
  targetFileId,
  profileId,
  position,
  forceInitialPosition,
  codecsVideo,
  codecsAudio,
  containers,
  maxResolution,
  hdr,
  explicitAudioTrackIndex,
}: {
  targetFileId: number;
  profileId: string;
  position: number;
  forceInitialPosition: boolean;
  codecsVideo: string[];
  codecsAudio: string[];
  containers: string[];
  maxResolution: string;
  hdr: boolean;
  explicitAudioTrackIndex?: number | null;
}): StartPlaybackRequestPayload {
  const payload: StartPlaybackRequestPayload = {
    file_id: targetFileId,
    profile_id: profileId,
    codecs_video: codecsVideo,
    codecs_audio: codecsAudio,
    containers,
    max_resolution: maxResolution,
    hdr,
  };

  if (forceInitialPosition || position > 0) {
    payload.start_position = position;
  }
  if (explicitAudioTrackIndex != null && explicitAudioTrackIndex >= 0) {
    payload.audio_track_index = explicitAudioTrackIndex;
  }

  return payload;
}

function buildStreamUrl(
  apiBaseUrl: string,
  streamPath: string,
  token: string | null,
  playMethod: PlayMethod,
  initialPosition: number,
): string {
  const params = new URLSearchParams();

  if (token) {
    params.set("token", token);
  }

  if (playMethod === "remux" && initialPosition > 0) {
    params.set("seek", initialPosition.toFixed(3));
  }

  const query = params.toString();
  // If the backend returned an absolute URL (proxy mode), use it directly.
  const base =
    streamPath.startsWith("http://") || streamPath.startsWith("https://")
      ? streamPath
      : `${apiBaseUrl}${streamPath}`;
  return `${base}${query ? `?${query}` : ""}`;
}

function describePlaybackSessionError(
  error: unknown,
  fallbackMessage: string,
): PlaybackSessionErrorState {
  if (error instanceof PlayerFetchError) {
    if (
      error.status === 404 &&
      error.code === "not_found" &&
      error.message === "Source media file is missing"
    ) {
      return {
        title: "This video is no longer available",
        message:
          "The file needed to play it can't be found right now. Go back and try another version if one is available.",
      };
    }

    if (error.status === 404 && error.code === "not_found") {
      return {
        title: "This item is no longer available",
        message:
          "The file needed to play this item can't be found right now. Go back and try another version if one is available.",
      };
    }

    if (error.status === 403) {
      return {
        title: "Playback unavailable",
        message: "You do not have permission to play this item.",
      };
    }

    if (error.status >= 500) {
      return {
        title: "Playback unavailable",
        message: "Silo could not start playback right now. Please try again.",
      };
    }

    return {
      title: "Playback unavailable",
      message: error.message || fallbackMessage,
    };
  }

  if (error instanceof Error && error.message === "No compatible file version found") {
    return {
      title: "No compatible version found",
      message: "Silo could not find a playable version for this device.",
    };
  }

  if (error instanceof Error && error.message.trim().length > 0) {
    return {
      title: "Playback unavailable",
      message: error.message,
    };
  }

  return {
    title: "Playback unavailable",
    message: fallbackMessage,
  };
}

/**
 * Manages the playback session lifecycle:
 * 1. On mount: detect codecs → select best version → POST /playback/start → get stream_url
 * 2. switchVersion(): stop current session → start new session at same position
 * 3. On unmount: DELETE /playback/{session_id} + keepalive fallback
 */
export function usePlaybackSession(
  requestKey: string,
  versions: PlayerFileVersion[],
  playbackVariants: PlayerPlaybackVariant[] = [],
  fileId?: number,
  initialPosition = 0,
  forceInitialPosition = false,
  qualityPreference?: string | null,
  resumeHints?: ResumeHints,
  explicitAudioTrackIndex?: number | null,
): UsePlaybackSessionResult {
  const config = usePlayerConfig();
  const { capabilities, selectBestVersion } = useCodecDetection();
  const [state, setState] = useState<PlaybackSessionState>({
    streamUrl: null,
    playMethod: null,
    sessionId: null,
    mediaFileId: null,
    initialPosition: 0,
    audioTrackIndex: 0,
    durationSeconds: null,
    subtitleUrls: [],
    playbackInfo: null,
    loading: true,
    replacing: false,
    errorTitle: null,
    error: null,
  });

  const sessionIdRef = useRef<string | null>(null);
  const stateRef = useRef(state);
  const activeRequestKeyRef = useRef<string | null>(null);
  const switchingRef = useRef(false);
  const loadSequenceRef = useRef(0);

  useEffect(() => {
    stateRef.current = state;
  }, [state]);

  const createSessionState = useCallback(
    async (targetFileId: number, position: number, forceStartPosition: boolean) => {
      const profileId = config.getProfileId();

      // Cap max_resolution with quality preference (use the lower of the two).
      let effectiveMaxRes = capabilities.max_resolution;
      if (qualityPreference && qualityPreference !== "auto") {
        const prefRes = QUALITY_TO_RESOLUTION[qualityPreference];
        if (prefRes) {
          const screenRank = RESOLUTION_ORDER[effectiveMaxRes] ?? 0;
          const prefRank = RESOLUTION_ORDER[prefRes] ?? 0;
          if (prefRank > 0 && prefRank < screenRank) {
            effectiveMaxRes = prefRes;
          }
        }
      }

      const body = JSON.stringify(
        buildStartPlaybackRequestPayload({
          targetFileId,
          profileId: profileId ?? "",
          position,
          forceInitialPosition: forceStartPosition,
          codecsVideo: capabilities.codecs_video,
          codecsAudio: capabilities.codecs_audio,
          containers: capabilities.containers,
          maxResolution: effectiveMaxRes,
          hdr: capabilities.hdr,
          explicitAudioTrackIndex,
        }),
      );

      const environment = getPlaybackEnvironmentSnapshot();
      console.info("[playback/start] client capabilities", {
        targetFileId,
        qualityPreference: qualityPreference ?? "auto",
        effectiveMaxRes,
        capabilities,
        environment,
      });

      const session = await playerFetch<PlaybackSessionResponse>(config, "/playback/start", {
        method: "POST",
        body,
      });

      // Append token as query param for native media elements
      // that can't set Authorization headers.
      const token = config.getAccessToken();
      const restoredPosition = session.position ?? 0;

      return {
        streamUrl: buildStreamUrl(
          config.apiBaseUrl,
          session.stream_url,
          token,
          session.play_method,
          restoredPosition,
        ),
        playMethod: session.play_method,
        sessionId: session.session_id,
        mediaFileId: session.media_file_id,
        initialPosition: restoredPosition,
        audioTrackIndex: session.audio_track_index ?? 0,
        durationSeconds: session.duration_seconds ?? null,
        subtitleUrls: (session.subtitle_urls ?? []).map((s) => ({
          ...s,
          url: buildStreamUrl(config.apiBaseUrl, s.url, token, "direct", 0),
        })),
        playbackInfo: session.playback_info ?? null,
        loading: false,
        replacing: false,
        errorTitle: null,
        error: null,
      } satisfies PlaybackSessionState;
    },
    [config, capabilities, explicitAudioTrackIndex, qualityPreference],
  );

  const stopSession = useCallback(
    async (sessionId: string) => {
      await playerFetch(config, `/playback/${sessionId}`, {
        method: "DELETE",
      });
    },
    [config],
  );

  const selectFileId = useCallback(
    (preferredFileId?: number) => {
      let selectedFileId: number | null | undefined = preferredFileId;
      if (!selectedFileId && resumeHints?.lastFileId) {
        const exact = versions.find((v) => v.file_id === resumeHints.lastFileId);
        if (exact) selectedFileId = exact.file_id;
      }
      if (!selectedFileId && resumeHints) {
        const traitMatch = matchByTraits(versions, resumeHints);
        if (traitMatch) selectedFileId = traitMatch.file_id;
      }
      if (!selectedFileId) {
        selectedFileId = selectDefaultVariantFile(
          playbackVariants,
          versions,
          resumeHints,
          qualityPreference,
          selectBestVersion,
        );
      }
      if (!selectedFileId) {
        const best = selectBestVersion(versions, qualityPreference);
        if (!best) {
          return null;
        }
        selectedFileId = best.file_id;
      }

      return selectedFileId;
    },
    [playbackVariants, qualityPreference, resumeHints, selectBestVersion, versions],
  );

  const loadSession = useCallback(
    async ({
      preferredFileId,
      position,
      forceStartPosition,
      allowPreserveExistingSessionOnError,
      replacementErrorMessage,
      initialErrorMessage,
    }: {
      preferredFileId?: number;
      position: number;
      forceStartPosition: boolean;
      allowPreserveExistingSessionOnError: boolean;
      replacementErrorMessage: string;
      initialErrorMessage: string;
    }) => {
      const previousState = stateRef.current;
      const previousSessionId = sessionIdRef.current;
      const hasExistingSession = !!previousState.sessionId && !!previousState.streamUrl;
      const loadSequence = ++loadSequenceRef.current;

      setState((current) => ({
        ...current,
        loading: !hasExistingSession,
        replacing: hasExistingSession,
        errorTitle: hasExistingSession ? current.errorTitle : null,
        error: hasExistingSession ? current.error : null,
      }));

      try {
        const selectedFileId = selectFileId(preferredFileId);
        if (!selectedFileId) {
          throw new Error("No compatible file version found");
        }

        const nextState = await createSessionState(selectedFileId, position, forceStartPosition);

        if (loadSequence !== loadSequenceRef.current) {
          await stopSession(nextState.sessionId!).catch(() => {
            // Best effort cleanup for stale session starts.
          });
          return;
        }

        sessionIdRef.current = nextState.sessionId;
        setState(nextState);

        if (previousSessionId && previousSessionId !== nextState.sessionId) {
          void stopSession(previousSessionId).catch(() => {
            // Best effort — stale session will time out server-side.
          });
        }
      } catch (err) {
        if (loadSequence !== loadSequenceRef.current) {
          return;
        }

        if (hasExistingSession && allowPreserveExistingSessionOnError) {
          console.error(replacementErrorMessage, err);
          setState((current) => ({
            ...current,
            loading: false,
            replacing: false,
          }));
          return;
        }

        const nextError = describePlaybackSessionError(err, initialErrorMessage);
        setState((current) => ({
          ...current,
          loading: false,
          replacing: false,
          errorTitle: nextError.title,
          error: nextError.message,
        }));
      }
    },
    [createSessionState, selectFileId, stopSession],
  );

  useEffect(() => {
    if (activeRequestKeyRef.current === requestKey) {
      return;
    }
    activeRequestKeyRef.current = requestKey;

    void loadSession({
      preferredFileId: fileId,
      position: initialPosition,
      forceStartPosition: forceInitialPosition,
      allowPreserveExistingSessionOnError: false,
      replacementErrorMessage: "Failed to replace playback request",
      initialErrorMessage: "Failed to start playback",
    });
  }, [fileId, forceInitialPosition, initialPosition, loadSession, requestKey]);

  // Clean up session on unmount.
  useEffect(() => {
    return () => {
      const sid = sessionIdRef.current;
      if (!sid) return;

      const token = config.getAccessToken();
      const profileId = config.getProfileId();
      const url = `${config.apiBaseUrl}/playback/${sid}`;

      const headers: Record<string, string> = {};
      if (token) headers["Authorization"] = `Bearer ${token}`;
      if (profileId) headers["X-Profile-Id"] = profileId;
      const profileToken = config.getProfileToken?.();
      if (profileToken) headers["X-Profile-Token"] = profileToken;

      // sendBeacon doesn't support DELETE, so use fetch with keepalive.
      fetch(url, {
        method: "DELETE",
        headers,
        keepalive: true,
      }).catch(() => {
        // Best effort — if fetch fails, session will time out server-side.
      });
    };
  }, [config]);

  const refreshSubtitles = useCallback(() => {
    const mediaFile = state.mediaFileId;
    const sid = sessionIdRef.current;
    if (!mediaFile || !sid) return;

    (async () => {
      try {
        const resp = await playerFetch<{ subtitles: DownloadedSubtitle[] }>(
          config,
          `/subtitles/${mediaFile}`,
        );
        const downloaded = resp.subtitles ?? [];
        if (downloaded.length === 0) return;

        setState((prev) => {
          // Filter out any previously added downloaded tracks
          const existing = prev.subtitleUrls.filter((s) => s.source !== "downloaded");
          const baseIndex = existing.length > 0 ? Math.max(...existing.map((s) => s.index)) + 1 : 0;
          const token = config.getAccessToken();
          const newTracks: PlayerSubtitleInfo[] = downloaded.map((dl, i) => ({
            index: baseIndex + i,
            id: dl.id,
            language: dl.language,
            codec: dl.format,
            label: `${dl.release_name} (${dl.provider})`,
            source: "downloaded" as const,
            hearing_impaired: dl.hearing_impaired,
            url: buildStreamUrl(
              config.apiBaseUrl,
              `/stream/${sid}/subtitles/${baseIndex + i}`,
              token,
              "direct",
              0,
            ),
          }));
          return { ...prev, subtitleUrls: [...existing, ...newTracks] };
        });
      } catch {
        // Best effort — subtitle refresh failure shouldn't disrupt playback.
      }
    })();
  }, [config, state.mediaFileId]);

  const switchAudioTrack = useCallback(
    (index: number, currentPosition: number) => {
      const sid = sessionIdRef.current;
      if (!sid) return;

      (async () => {
        try {
          const resp = await playerFetch<ChangeAudioResponse>(config, `/playback/${sid}/audio`, {
            method: "PATCH",
            body: JSON.stringify({
              audio_track_index: index,
              position: currentPosition,
            }),
          });

          const token = config.getAccessToken();
          setState((prev) => ({
            ...prev,
            streamUrl: buildStreamUrl(
              config.apiBaseUrl,
              resp.stream_url,
              token,
              resp.play_method,
              currentPosition,
            ),
            playMethod: resp.play_method,
            audioTrackIndex: resp.audio_track_index,
            playbackInfo: resp.playback_info ?? prev.playbackInfo,
            initialPosition: currentPosition,
          }));
        } catch (err) {
          console.error("Failed to switch audio track:", err);
        }
      })();
    },
    [config],
  );

  const switchVersion = useCallback(
    (newFileId: number, currentPosition: number) => {
      if (switchingRef.current) return;
      if (newFileId === state.mediaFileId) return;
      switchingRef.current = true;

      (async () => {
        try {
          await loadSession({
            preferredFileId: newFileId,
            position: currentPosition,
            forceStartPosition: false,
            allowPreserveExistingSessionOnError: true,
            replacementErrorMessage: "Failed to switch playback version",
            initialErrorMessage: "Failed to switch version",
          });
        } finally {
          switchingRef.current = false;
        }
      })();
    },
    [loadSession, state.mediaFileId],
  );

  return { ...state, switchVersion, switchAudioTrack, refreshSubtitles };
}

function selectDefaultVariantFile(
  playbackVariants: PlayerPlaybackVariant[],
  versions: PlayerFileVersion[],
  resumeHints?: ResumeHints,
  qualityPreference?: string | null,
  selectBestVersionFn?: (
    versions: PlayerFileVersion[],
    qualityPreference?: string | null,
  ) => PlayerFileVersion | null,
): number | null {
  if (playbackVariants.length === 0) {
    return null;
  }

  let candidateVariants = playbackVariants;
  if (
    resumeHints?.lastEditionKey &&
    playbackVariants.some((variant) => variant.edition_key === resumeHints.lastEditionKey)
  ) {
    candidateVariants = playbackVariants.filter(
      (variant) => variant.edition_key === resumeHints.lastEditionKey,
    );
  } else if (playbackVariants.some((variant) => !variant.edition_key)) {
    candidateVariants = playbackVariants.filter((variant) => !variant.edition_key);
  }

  let selectedVariant: PlayerPlaybackVariant | null = null;
  let selectedVersion: PlayerFileVersion | null = null;

  for (const variant of candidateVariants) {
    const firstPart = [...(variant.parts ?? [])].sort((a, b) => a.part_index - b.part_index)[0];
    if (!firstPart) {
      continue;
    }

    let candidateVersion: PlayerFileVersion | null =
      firstPart.default_file_id != null
        ? (versions.find((version) => version.file_id === firstPart.default_file_id) ?? null)
        : null;
    if (!candidateVersion && selectBestVersionFn) {
      candidateVersion = selectBestVersionFn(firstPart.versions ?? [], qualityPreference);
    }
    if (!candidateVersion) {
      continue;
    }

    if (!selectedVariant || !selectedVersion) {
      selectedVariant = variant;
      selectedVersion = candidateVersion;
      continue;
    }

    if (variant.edition_key && !selectedVariant.edition_key) {
      continue;
    }

    const winner = selectBestVersionFn?.([selectedVersion, candidateVersion], qualityPreference);
    if (winner?.file_id === candidateVersion.file_id) {
      selectedVariant = variant;
      selectedVersion = candidateVersion;
    }
  }

  return selectedVersion?.file_id ?? null;
}
