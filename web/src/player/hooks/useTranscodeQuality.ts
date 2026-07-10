import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import { PlayerFetchError, playerFetch } from "../player-fetch";
import type { PlayMethod, PlayerFileVersion, QualityOption, TranscodeStartRequest } from "../types";
import { QUALITY_TO_RESOLUTION } from "./useCodecDetection";

/** Quality tier definition. ID is frontend-only; backend receives resolution + bitrate separately. */
interface QualityTierDef {
  id: string;
  label: string;
  resolution: string;
  bitrate: number;
}

/** All transcode-able quality tiers in descending quality order. */
const QUALITY_TIERS: QualityTierDef[] = [
  { id: "1080p-high", label: "1080p High", resolution: "1080p", bitrate: 10000 },
  { id: "1080p", label: "1080p", resolution: "1080p", bitrate: 6000 },
  { id: "720p-high", label: "720p High", resolution: "720p", bitrate: 4000 },
  { id: "720p", label: "720p", resolution: "720p", bitrate: 2000 },
  { id: "480p", label: "480p", resolution: "480p", bitrate: 1500 },
  { id: "420p", label: "420p", resolution: "420p", bitrate: 720 },
];

/** Numeric height for each resolution string. */
const RESOLUTION_HEIGHT: Record<string, number> = {
  "2160p": 2160,
  "1080p": 1080,
  "720p": 720,
  "480p": 480,
  "420p": 420,
};

interface TranscodeStartResponse {
  session_id: string;
  status: string;
  switched_file_id?: number;
  manifest_url: string;
  duration_seconds: number | null;
  player_start_seconds: number;
  stream_origin_seconds?: number;
  timeline_offset_seconds: number;
  can_seek_anywhere: boolean;
}

interface UseTranscodeQualityParams {
  sessionId: string | null;
  selectedVersion: PlayerFileVersion | undefined;
  versions: PlayerFileVersion[];
  playMethod: PlayMethod | null;
  initialPosition: number;
  qualityPreference?: string | null;
}

interface UseTranscodeQualityResult {
  qualityOptions: QualityOption[];
  activeQualityId: string;
  switchQuality: (qualityId: string, currentPosition: number, forceRestart?: boolean) => void;
  /**
   * Burn the given embedded bitmap subtitle track (ffmpeg subtitle-stream
   * ordinal) into the video, or stop burning when null. Restarts the transcode
   * at the current position via the same machinery as a quality/audio switch,
   * so the server's segment-boundary timeline alignment applies unchanged.
   */
  setSubtitleBurnIn: (
    ffmpegSubtitleIndex: number | null,
    currentPosition: number,
    subtitleMediaFileId?: number,
  ) => void;
  /** Currently burned-in embedded subtitle ordinal, or null. */
  burnInSubtitleIndex: number | null;
  transcodeStreamUrl: string | null;
  playerStartSeconds: number;
  streamOriginSeconds: number;
  canSeekAnywhere: boolean;
  durationSeconds: number | null;
  isTranscoding: boolean;
  error: string | null;
  switchedFileId: number | null;
  effectiveVersion: PlayerFileVersion | undefined;
}

export const COMPATIBILITY_QUALITY_ID = "compatibility";

// Quality-menu bitrate label: Mbps with collapsed integers ("8 Mbps", not
// "8.0 Mbps") — a deliberately different display policy than the canonical
// formatBitrate/formatMbpsFromKbps in @/lib/mediaFormat.
function formatQualityBitrate(kbps: number): string {
  if (kbps >= 1000) {
    const mbps = kbps / 1000;
    return mbps % 1 === 0 ? `${mbps} Mbps` : `${mbps.toFixed(1)} Mbps`;
  }
  return `${kbps} kbps`;
}

function fallbackBitrateForResolution(resolution: string, sourceBitrate: number): number {
  const tier = QUALITY_TIERS.find((candidate) => candidate.resolution === resolution);
  if (tier) {
    return tier.bitrate;
  }
  if (sourceBitrate > 0) {
    return Math.min(sourceBitrate, 10000);
  }
  return 6000;
}

function playMethodLabel(method: PlayMethod | null): string {
  switch (method) {
    case "direct":
      return "Direct Play";
    case "remux":
      return "Remux";
    case "transcode":
      return "Transcode";
    default:
      return "";
  }
}

function buildQualityOptions(
  version: PlayerFileVersion | undefined,
  playMethod: PlayMethod | null,
): QualityOption[] {
  if (!version) return [];

  const options: QualityOption[] = [];

  // Original quality option.
  const methodLabel = playMethodLabel(playMethod);
  const bitrateLabel = version.bitrate > 0 ? formatQualityBitrate(version.bitrate) : "";
  const sublabelParts = [methodLabel, bitrateLabel].filter(Boolean);

  options.push({
    id: "original",
    label: `Original (${version.resolution === "2160p" ? "4K" : version.resolution})`,
    sublabel: sublabelParts.join(" \u00b7 "),
    resolution: "",
    bitrateKbps: 0,
    isOriginal: true,
  });

  // Auto option — selects best tier at or below screen resolution.
  options.push({
    id: "auto",
    label: "Auto",
    sublabel: "",
    resolution: "",
    bitrateKbps: 0,
    isOriginal: false,
  });

  // Determine the file's native height.
  const nativeHeight = RESOLUTION_HEIGHT[version.resolution] ?? 0;

  // Add transcode options at or below native resolution.
  for (const tier of QUALITY_TIERS) {
    const tierHeight = RESOLUTION_HEIGHT[tier.resolution];
    if (tierHeight == null) continue;
    if (tierHeight >= nativeHeight) continue;

    options.push({
      id: tier.id,
      label: tier.label,
      sublabel: `~${formatQualityBitrate(tier.bitrate)}`,
      resolution: tier.resolution,
      bitrateKbps: tier.bitrate,
      isOriginal: false,
    });
  }

  return options;
}

export function useTranscodeQuality({
  sessionId,
  selectedVersion,
  versions,
  playMethod,
  initialPosition,
  qualityPreference,
}: UseTranscodeQualityParams): UseTranscodeQualityResult {
  const config = usePlayerConfig();
  const [activeQualityId, setActiveQualityId] = useState("original");
  const [transcodeStreamUrl, setTranscodeStreamUrl] = useState<string | null>(null);
  const [playerStartSeconds, setPlayerStartSeconds] = useState(0);
  const [streamOriginSeconds, setStreamOriginSeconds] = useState(0);
  const [canSeekAnywhere, setCanSeekAnywhere] = useState(true);
  const [durationSeconds, setDurationSeconds] = useState<number | null>(null);
  const [isTranscoding, setIsTranscoding] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [switchedFileId, setSwitchedFileId] = useState<number | null>(null);
  const switchAbortRef = useRef<AbortController | null>(null);
  // Pending (macrotask-deferred) transcode start dispatch. Restart triggers can
  // stack up in one tick — e.g. on mount the auto-start effect fires, then
  // subtitle auto-selection restores a burned-in bitmap track and fires again —
  // and each server call kills and respawns ffmpeg. Deferring the network
  // dispatch by one macrotask lets the last call in a tick win, so the server
  // sees a single start with the final parameters.
  const dispatchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const manifestVersionRef = useRef(0);
  const autoStartKeyRef = useRef<string | null>(null);
  // Latest requested quality, updated synchronously before React commits the
  // corresponding state. Bitmap subtitle restoration can request another
  // restart in the same tick and must preserve this pending quality.
  const requestedQualityIdRef = useRef("original");
  // Embedded subtitle ordinal being burned into the video, or null. Kept in a
  // ref (mirrored to state for consumers) so every restart path — quality
  // switch, out-of-window seek, compatibility fallback — reads the current
  // value without stale-closure races.
  const [burnInSubtitleIndex, setBurnInSubtitleIndex] = useState<number | null>(null);
  const burnInRef = useRef<number | null>(null);
  const burnInMediaFileIdRef = useRef<number | null>(null);

  const effectiveVersion = useMemo(() => {
    if (switchedFileId != null) {
      const switched = versions.find((v) => v.file_id === switchedFileId);
      if (switched) return switched;
    }
    return selectedVersion;
  }, [switchedFileId, versions, selectedVersion]);

  const qualityOptions = useMemo(
    () => buildQualityOptions(effectiveVersion, playMethod),
    [effectiveVersion, playMethod],
  );

  useEffect(() => {
    switchAbortRef.current?.abort();
    switchAbortRef.current = null;
    if (dispatchTimerRef.current != null) {
      clearTimeout(dispatchTimerRef.current);
      dispatchTimerRef.current = null;
    }
    autoStartKeyRef.current = null;
    requestedQualityIdRef.current = "original";
    setActiveQualityId("original");
    setTranscodeStreamUrl(null);
    setPlayerStartSeconds(0);
    setStreamOriginSeconds(0);
    setCanSeekAnywhere(true);
    setDurationSeconds(null);
    setIsTranscoding(false);
    setError(null);
    setSwitchedFileId(null);
    burnInRef.current = null;
    burnInMediaFileIdRef.current = null;
    setBurnInSubtitleIndex(null);
  }, [sessionId, selectedVersion?.file_id, playMethod]);

  // On unmount, cancel any in-flight or still-deferred start so a stray
  // transcode/start can't land after the session's exit DELETE.
  useEffect(
    () => () => {
      switchAbortRef.current?.abort();
      if (dispatchTimerRef.current != null) {
        clearTimeout(dispatchTimerRef.current);
      }
    },
    [],
  );

  const startTranscode = useCallback(
    (qualityId: string, currentPosition: number, forceRestart = false) => {
      if (!sessionId) return;
      requestedQualityIdRef.current = qualityId;
      if (!forceRestart && qualityId === activeQualityId) return;

      // Abort any in-progress switch (POST + polling) and supersede any
      // dispatch still waiting on its macrotask.
      switchAbortRef.current?.abort();
      switchAbortRef.current = null;
      if (dispatchTimerRef.current != null) {
        clearTimeout(dispatchTimerRef.current);
        dispatchTimerRef.current = null;
      }

      // Clear the guard's file switch when reverting to original quality.
      // The backend will set a new switched_file_id in the response if needed.
      if (qualityId === "original") {
        setSwitchedFileId(null);
      }

      // Switching back to the original stream only makes sense when the base
      // playback session was direct/remux. If the base session itself requires
      // transcoding, "original" still means an original-resolution transcode.
      // For direct play, switching to "original" means stopping HLS entirely.
      // For remux and transcode, "original" still needs HLS. Remux can keep
      // video copy; transcode must still encode video because the source codec
      // was already classified as browser-incompatible.
      // A burned-in bitmap subtitle always needs an encoding HLS transcode,
      // even on a direct-play base — never fall back to the raw file then.
      if (
        qualityId === "original" &&
        playMethod !== "transcode" &&
        playMethod !== "remux" &&
        burnInRef.current == null
      ) {
        setActiveQualityId("original");
        setTranscodeStreamUrl(null);
        setPlayerStartSeconds(0);
        setStreamOriginSeconds(0);
        setCanSeekAnywhere(true);
        setDurationSeconds(null);
        setIsTranscoding(false);
        setError(null);
        return;
      }

      // Find the quality option to get the bitrate preset.
      const option =
        qualityId === "original"
          ? (qualityOptions.find((o) => o.id === "original") ?? {
              id: "original",
              label: "Original",
              sublabel: "",
              resolution: "",
              bitrateKbps: 0,
              isOriginal: true,
            })
          : qualityId === COMPATIBILITY_QUALITY_ID && effectiveVersion
            ? {
                id: COMPATIBILITY_QUALITY_ID,
                label: "Compatibility mode",
                sublabel: "",
                resolution: effectiveVersion.resolution,
                bitrateKbps: fallbackBitrateForResolution(
                  effectiveVersion.resolution,
                  effectiveVersion.bitrate,
                ),
                isOriginal: false,
              }
            : qualityOptions.find((o) => o.id === qualityId);
      if (!option) return;

      const abortController = new AbortController();
      switchAbortRef.current = abortController;

      setIsTranscoding(true);
      setActiveQualityId(qualityId);
      setError(null);
      // Immediately clear the old transcode URL so React unmounts the
      // current <media-player> before the backend deletes segments.
      // Without this, the old hls.js instance tries to fetch segments
      // from the deleted output directory and throws bufferAppendErrors.
      setTranscodeStreamUrl(null);

      const dispatch = async () => {
        const requestedBurnInIndex = burnInRef.current;
        const requestedBurnInMediaFileId = burnInMediaFileIdRef.current;
        const rollbackFailedBurnIn = () => {
          if (
            requestedBurnInIndex != null &&
            burnInRef.current === requestedBurnInIndex &&
            burnInMediaFileIdRef.current === requestedBurnInMediaFileId
          ) {
            burnInRef.current = null;
            burnInMediaFileIdRef.current = null;
            setBurnInSubtitleIndex(null);
          }
        };
        try {
          // When "Original" is selected on a remux base, use codec copy (no
          // video re-encoding). Transcode bases still encode video because the
          // source video codec is not browser-playable.
          // Audio is always transcoded to AAC — the source may use codecs the
          // browser can't decode (EAC3, DTS, TrueHD, etc.) and audio transcoding
          // adds negligible overhead compared to video.
          const isCompatibilityFallback = option.id === COMPATIBILITY_QUALITY_ID;
          // Burn-in composites the subtitle into the video frames, which
          // requires an encode — codec copy is never allowed while a bitmap
          // subtitle is burned in (the server enforces this too).
          const burnInIndex = requestedBurnInIndex;
          const isCopyOriginal =
            option.isOriginal &&
            !isCompatibilityFallback &&
            playMethod === "remux" &&
            burnInIndex == null;
          const body: TranscodeStartRequest = {
            session_id: sessionId,
            seek_seconds: currentPosition,
            target_resolution: isCopyOriginal ? "" : option.resolution,
            target_codec_video: isCopyOriginal ? "copy" : "h264",
            target_codec_audio: "aac",
            target_bitrate_kbps: isCopyOriginal ? 0 : option.bitrateKbps,
            // Shorter HLS segments reduce startup latency noticeably,
            // especially for remux/copy sessions where a long startup
            // window can delay first frame for several seconds.
            segment_duration: 2,
            subtitle_track_index: burnInIndex ?? -1,
            subtitle_media_file_id:
              burnInIndex != null ? (requestedBurnInMediaFileId ?? undefined) : undefined,
            subtitle_burn_in: burnInIndex != null,
          };

          const resp = await playerFetch<TranscodeStartResponse>(
            config,
            "/playback/transcode/start",
            { method: "POST", body: JSON.stringify(body), signal: abortController.signal },
          );

          if (abortController.signal.aborted) return;

          if (resp?.switched_file_id != null) {
            setSwitchedFileId(resp.switched_file_id);
          }

          // Use the manifest URL from the backend response. In distributed mode
          // this points to the proxy node; in integrated mode it's an API-local path.
          const token = config.getAccessToken();
          const params = new URLSearchParams();
          if (token) params.set("token", token);
          manifestVersionRef.current += 1;
          params.set("v", `${Date.now()}-${manifestVersionRef.current}-${qualityId}`);
          const query = params.toString();

          let manifestUrl = resp.manifest_url;
          if (manifestUrl.startsWith("/")) {
            // Relative path — prepend API base URL.
            manifestUrl = `${config.apiBaseUrl}${manifestUrl}`;
          }
          if (query) {
            manifestUrl += (manifestUrl.includes("?") ? "&" : "?") + query;
          }

          setTranscodeStreamUrl(manifestUrl);
          setPlayerStartSeconds(resp.player_start_seconds ?? currentPosition);
          setStreamOriginSeconds(resp.stream_origin_seconds ?? resp.timeline_offset_seconds ?? 0);
          setCanSeekAnywhere(resp.can_seek_anywhere ?? true);
          setDurationSeconds(resp.duration_seconds ?? null);
          setError(null);
        } catch (err: unknown) {
          if (abortController.signal.aborted) return;
          rollbackFailedBurnIn();
          // 422 = no alternate file version available for 4K transcode protection.
          if (err instanceof PlayerFetchError && err.status === 422) {
            setActiveQualityId("original");
            setTranscodeStreamUrl(null);
            setPlayerStartSeconds(0);
            setStreamOriginSeconds(0);
            setCanSeekAnywhere(true);
            setDurationSeconds(null);
            setError("No lower resolution version available for transcoding");
            return;
          }
          if (playMethod === "transcode") {
            setError(`Couldn't start ${option.label}.`);
          } else {
            setActiveQualityId("original");
            setTranscodeStreamUrl(null);
            setPlayerStartSeconds(0);
            setStreamOriginSeconds(0);
            setCanSeekAnywhere(true);
            setDurationSeconds(null);
            setError(`Couldn't switch to ${option.label}.`);
          }
        } finally {
          if (!abortController.signal.aborted) {
            setIsTranscoding(false);
          }
        }
      };
      dispatchTimerRef.current = setTimeout(() => {
        dispatchTimerRef.current = null;
        void dispatch();
      }, 0);
    },
    [sessionId, activeQualityId, qualityOptions, config, effectiveVersion, playMethod],
  );

  const switchQuality = useCallback(
    (qualityId: string, currentPosition: number, forceRestart?: boolean) => {
      requestedQualityIdRef.current = qualityId;
      // When the user explicitly selects "Original" from the quality menu:
      // - Direct play base: stop HLS and play the raw file (instant), unless a
      //   bitmap subtitle is burned in (burn-in always needs an encode).
      // - Remux base: use codec copy via HLS.
      // - Transcode base: restart HLS at source resolution while encoding video.
      if (qualityId === "original" && playMethod === "direct" && burnInRef.current == null) {
        switchAbortRef.current?.abort();
        switchAbortRef.current = null;
        setActiveQualityId("original");
        setTranscodeStreamUrl(null);
        setPlayerStartSeconds(0);
        setStreamOriginSeconds(0);
        setCanSeekAnywhere(true);
        setDurationSeconds(null);
        setIsTranscoding(false);
        setError(null);
        setSwitchedFileId(null);
        return;
      }
      startTranscode(qualityId, currentPosition, forceRestart ?? false);
    },
    [startTranscode, playMethod],
  );

  // Selecting a bitmap (PGS/DVD/DVB) subtitle track burns it into the video
  // server-side: the transcode restarts with subtitle_burn_in at the current
  // position, riding the exact restart machinery a quality switch uses so the
  // segment-boundary timeline alignment fix is preserved. Deselecting (null)
  // restarts without burn-in — or drops back to the raw file on a direct-play
  // "original" session.
  const setSubtitleBurnIn = useCallback(
    (ffmpegSubtitleIndex: number | null, currentPosition: number, subtitleMediaFileId?: number) => {
      const normalizedMediaFileId = subtitleMediaFileId ?? null;
      if (
        burnInRef.current === ffmpegSubtitleIndex &&
        burnInMediaFileIdRef.current === normalizedMediaFileId
      ) {
        return;
      }
      burnInRef.current = ffmpegSubtitleIndex;
      burnInMediaFileIdRef.current = ffmpegSubtitleIndex == null ? null : normalizedMediaFileId;
      setBurnInSubtitleIndex(ffmpegSubtitleIndex);
      switchQuality(requestedQualityIdRef.current, currentPosition, true);
    },
    [switchQuality],
  );

  useEffect(() => {
    if (!sessionId || (playMethod !== "transcode" && playMethod !== "remux")) {
      return;
    }

    const autoStartKey = `${sessionId}:${selectedVersion?.file_id ?? "none"}:${initialPosition}`;
    if (autoStartKeyRef.current === autoStartKey) {
      return;
    }

    autoStartKeyRef.current = autoStartKey;

    // If the user has a quality preference, start at that tier if it's a valid
    // option for this file (i.e., lower than the file's native resolution).
    // Otherwise fall back to "original" for remux sessions (codec copy is fast)
    // or the highest available quality tier for transcode sessions (encoding at
    // a capped resolution is much faster than encoding at full original resolution).
    let autoStartQuality = "original";
    if (qualityPreference && qualityPreference !== "auto") {
      const prefRes = QUALITY_TO_RESOLUTION[qualityPreference];
      if (prefRes) {
        const match = qualityOptions.find((o) => o.resolution === prefRes);
        if (match) {
          autoStartQuality = match.id;
        }
      }
    } else if (playMethod === "transcode") {
      // No explicit preference and video needs transcoding — pick the highest
      // quality tier available (first tier option, which is just below native
      // resolution). Encoding at e.g. 1080p is significantly faster than at 4K
      // and produces a much faster startup time.
      const bestTier = qualityOptions.find((o) => !o.isOriginal && o.resolution !== "");
      if (bestTier) {
        autoStartQuality = bestTier.id;
      }
    }

    startTranscode(autoStartQuality, initialPosition, true);
  }, [
    initialPosition,
    playMethod,
    qualityOptions,
    qualityPreference,
    selectedVersion?.file_id,
    sessionId,
    startTranscode,
  ]);

  return {
    qualityOptions,
    activeQualityId,
    switchQuality,
    setSubtitleBurnIn,
    burnInSubtitleIndex,
    transcodeStreamUrl,
    playerStartSeconds,
    streamOriginSeconds,
    canSeekAnywhere,
    durationSeconds,
    isTranscoding,
    error,
    switchedFileId,
    effectiveVersion,
  };
}
