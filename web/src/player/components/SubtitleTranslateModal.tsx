import { useState, useEffect, useCallback, useMemo, useRef } from "react";
import { createPortal } from "react-dom";
import { toast } from "sonner";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import type { PlayerAudioTrack, PlayerSubtitleInfo } from "../types";
import { playerV2 } from "../player-v2";
import { PlayerFetchError } from "../player-fetch";
import { LANGUAGES, getLanguageName, normalizeLanguageCode } from "../utils/languageNames";
import {
  buildSubtitleTranslateRequest,
  isTranslatableSource,
  type SubtitleTranslateMode,
} from "./subtitleTranslateRequest";
import { QUOTA_PERIOD_WINDOW_LABELS } from "@/lib/quotaPeriods";

interface SubtitleTranslateModalProps {
  preferredSubtitleLanguage?: string | null;
  mediaFileId: number;
  playerConfig: PlayerConfig;
  tracks: PlayerSubtitleInfo[];
  audioTracks?: PlayerAudioTrack[];
  translateEnabled?: boolean;
  transcribeEnabled?: boolean;
  isOpen: boolean;
  onSubtitleJobAccepted?: (jobId: string) => void;
  sessionId?: string;
  getStartPosition?: () => number;
  onClose: () => void;
}

function sourceLabel(track: PlayerSubtitleInfo): string {
  const lang = getLanguageName(track.language) || track.language || "Unknown";
  const origin = track.source ? ` · ${track.source}` : "";
  return `${lang}${origin}`;
}

function audioLabel(track: PlayerAudioTrack, i: number): string {
  const lang = getLanguageName(track.language ?? "") || track.language || `Track ${i + 1}`;
  const layout = track.layout ? ` · ${track.layout}` : "";
  return `${lang}${layout}${track.default ? " · default" : ""}`;
}

// Per-user transcription quota as reported by GET /subtitles/ai/quota.
// `limited` is false when no quota applies to the caller.
interface TranscribeQuota {
  limited: boolean;
  limit: number;
  used: number;
  remaining: number;
  period: string;
}

export function SubtitleTranslateModal({
  preferredSubtitleLanguage,
  mediaFileId,
  playerConfig,
  tracks,
  audioTracks,
  translateEnabled = true,
  transcribeEnabled = false,
  isOpen,
  onSubtitleJobAccepted,
  sessionId,
  getStartPosition,
  onClose,
}: SubtitleTranslateModalProps) {
  // Only offer sources the server can actually translate (excludes live tracks,
  // bitmap embedded tracks, and ASS/non-text external/downloaded tracks).
  const sourceTracks = useMemo(() => tracks.filter(isTranslatableSource), [tracks]);
  const canTranslate = translateEnabled && sourceTracks.length > 0;
  const canTranscribe = transcribeEnabled && (audioTracks?.length ?? 0) > 0;
  // Subtitle translation is the default; generating from audio takes over when
  // it's the only possible path (e.g. bitmap-only files).
  const [mode, setMode] = useState<SubtitleTranslateMode>(canTranslate ? "subtitles" : "audio");
  const [sourceIndex, setSourceIndex] = useState<number | null>(null);
  const [audioIndex, setAudioIndex] = useState(0);
  const [targetLang, setTargetLang] = useState(() => {
    const preferred = normalizeLanguageCode(preferredSubtitleLanguage);
    return LANGUAGES.some((language) => language.code === preferred) ? preferred : "en";
  });
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [quota, setQuota] = useState<TranscribeQuota | null>(null);

  const effectiveSourceIndex = sourceIndex ?? sourceTracks[0]?.index ?? null;
  const quotaExhausted = quota !== null && quota.remaining <= 0;
  const quotaPeriodLabel = quota ? (QUOTA_PERIOD_WINDOW_LABELS[quota.period] ?? quota.period) : "";

  const generation = useRef(0);
  const inFlight = useRef(false);
  const currentProps = useRef({ mediaFileId, sessionId, playerConfig, isOpen });
  currentProps.current = { mediaFileId, sessionId, playerConfig, isOpen };
  useEffect(() => {
    generation.current++;
    inFlight.current = false;
    setSubmitting(false);
    setError(null);
    setQuota(null);
    return () => {
      generation.current++;
    };
  }, [mediaFileId, sessionId, playerConfig, isOpen]);

  const captureCurrent = useCallback(() => {
    const capturedGeneration = generation.current;
    const token = playerConfig.getAccessToken();
    const profile = playerConfig.getProfileId();
    const pin = playerConfig.getProfileToken?.();
    return () =>
      capturedGeneration === generation.current &&
      currentProps.current.isOpen &&
      currentProps.current.mediaFileId === mediaFileId &&
      currentProps.current.sessionId === sessionId &&
      currentProps.current.playerConfig === playerConfig &&
      token === playerConfig.getAccessToken() &&
      profile === playerConfig.getProfileId() &&
      pin === playerConfig.getProfileToken?.();
  }, [playerConfig, mediaFileId, sessionId]);
  const handleClose = useCallback(() => {
    generation.current++;
    onClose();
  }, [onClose]);

  // A failed or stale lookup cannot overwrite another viewer's quota display.
  const refreshQuota = useCallback(() => {
    const current = captureCurrent();
    playerV2(playerConfig, "GET /api/v2/subtitles/ai/quota", {})
      .then((q) => {
        if (current()) setQuota(q?.limited ? q : null);
      })
      .catch(() => {
        if (current()) setQuota(null);
      });
  }, [playerConfig, captureCurrent]);

  // Refresh the transcription quota each time the modal opens, so the user
  // sees how many jobs they have left before starting one.
  useEffect(() => {
    if (!isOpen || !canTranscribe) return;
    refreshQuota();
  }, [isOpen, canTranscribe, refreshQuota]);

  useEffect(() => {
    if (!isOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") handleClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [isOpen, handleClose]);

  const handleTranslate = useCallback(async () => {
    if (inFlight.current || !isOpen) return;
    const fromAudio = mode === "audio";
    if (!fromAudio && effectiveSourceIndex === null) return;
    inFlight.current = true;
    const current = captureCurrent();
    setSubmitting(true);
    setError(null);
    try {
      const body = buildSubtitleTranslateRequest({
        mode,
        mediaFileId,
        sourceTracks,
        effectiveSourceIndex,
        audioTracks,
        audioIndex,
        targetLang,
        sessionId,
        startPosition: getStartPosition?.() ?? 0,
      });
      const res = await playerV2(playerConfig, "POST /api/v2/subtitles/ai/translate", {
        body: { ...body, media_file_id: String(mediaFileId), kind: body.kind ?? "translate" },
      });
      if (!current()) return;
      if (
        !res ||
        !/^[1-9][0-9]*$/.test(res.job.id) ||
        res.job.media_file_id !== String(mediaFileId) ||
        res.job.kind !== (body.kind ?? "translate") ||
        res.job.source_index !== body.source_index
      ) {
        throw new Error("Subtitle processing returned an invalid job.");
      }
      onSubtitleJobAccepted?.(res.job.id);
      if (!res.live_delivery_attached) {
        toast.info("Your subtitle job is underway. The track will appear when it's ready.");
      }
      handleClose();
    } catch (err) {
      if (!current()) return;
      // A quota rejection means the cached counter was stale (e.g. another
      // device used the last slot) — refresh it so the banner and the
      // disabled Generate button match the error we're about to show.
      if (err instanceof PlayerFetchError && err.code === "rate_limited") {
        refreshQuota();
      }
      setError(
        err instanceof Error
          ? err.message
          : mode === "audio"
            ? "Couldn't start subtitle generation."
            : "Couldn't start translation.",
      );
    } finally {
      if (current()) {
        inFlight.current = false;
        setSubmitting(false);
      }
    }
  }, [
    mode,
    effectiveSourceIndex,
    sourceTracks,
    audioTracks,
    audioIndex,
    mediaFileId,
    targetLang,
    sessionId,
    getStartPosition,
    playerConfig,
    handleClose,
    captureCurrent,
    isOpen,
    refreshQuota,
    onSubtitleJobAccepted,
  ]);

  if (!isOpen) return null;

  const modal = (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/80"
      onClick={handleClose}
      role="dialog"
      aria-modal="true"
      aria-label="Translate subtitles with AI"
    >
      <div
        className="w-full max-w-[440px] rounded-lg bg-neutral-900 text-white shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-white/10 px-4 py-3">
          <h2 className="text-sm font-semibold">
            {mode === "audio" ? "Generate subtitles with AI" : "Translate subtitles with AI"}
          </h2>
          <button
            type="button"
            className="rounded text-white/60 hover:text-white focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
            onClick={handleClose}
            aria-label="Close"
          >
            ✕
          </button>
        </div>

        <div className="space-y-3 px-4 py-4">
          {canTranslate && canTranscribe && (
            <div className="flex gap-1 rounded bg-neutral-800 p-1" role="tablist">
              {(
                [
                  ["subtitles", "From subtitles"],
                  ["audio", "From audio"],
                ] as const
              ).map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  role="tab"
                  aria-selected={mode === value}
                  className={`flex-1 rounded px-2 py-1 text-xs font-medium focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
                    mode === value ? "bg-white/15 text-white" : "text-white/50 hover:text-white/80"
                  }`}
                  onClick={() => setMode(value)}
                  disabled={submitting}
                >
                  {label}
                </button>
              ))}
            </div>
          )}

          {!canTranslate && !canTranscribe ? (
            <p className="py-4 text-center text-xs text-white/50">
              No text subtitle track is available to translate. Add or download one first.
            </p>
          ) : (
            <>
              {mode === "subtitles" ? (
                <label className="block">
                  <span className="mb-1 block text-xs font-medium text-white/60">
                    Translate from
                  </span>
                  <select
                    className="w-full rounded bg-neutral-800 px-2 py-1.5 text-sm text-white focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none disabled:opacity-50"
                    value={effectiveSourceIndex ?? ""}
                    onChange={(e) => setSourceIndex(Number(e.target.value))}
                    disabled={submitting}
                  >
                    {sourceTracks.map((track) => (
                      <option key={track.index} value={track.index}>
                        {sourceLabel(track)}
                      </option>
                    ))}
                  </select>
                </label>
              ) : (
                <label className="block">
                  <span className="mb-1 block text-xs font-medium text-white/60">Audio track</span>
                  <select
                    className="w-full rounded bg-neutral-800 px-2 py-1.5 text-sm text-white focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none disabled:opacity-50"
                    value={audioIndex}
                    onChange={(e) => setAudioIndex(Number(e.target.value))}
                    disabled={submitting}
                  >
                    {(audioTracks ?? []).map((track, i) => (
                      <option key={i} value={i}>
                        {audioLabel(track, i)}
                      </option>
                    ))}
                  </select>
                </label>
              )}

              <label className="block">
                <span className="mb-1 block text-xs font-medium text-white/60">
                  {mode === "audio" ? "Subtitle language" : "Translate to"}
                </span>
                <select
                  className="w-full rounded bg-neutral-800 px-2 py-1.5 text-sm text-white focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none disabled:opacity-50"
                  value={targetLang}
                  onChange={(e) => setTargetLang(e.target.value)}
                  disabled={submitting}
                >
                  {LANGUAGES.map((lang) => (
                    <option key={lang.code} value={lang.code}>
                      {lang.label}
                    </option>
                  ))}
                </select>
              </label>

              {mode === "audio" && quota && (
                <p
                  className={`text-[11px] leading-relaxed ${
                    quotaExhausted ? "text-amber-300/90" : "text-white/35"
                  }`}
                >
                  {quotaExhausted
                    ? `You've used all ${quota.limit} transcriptions for the last ${quotaPeriodLabel}. Try again later.`
                    : `${quota.remaining} of ${quota.limit} transcriptions left for the last ${quotaPeriodLabel}.`}
                </p>
              )}

              {error && (
                <div role="alert" className="rounded bg-red-900/40 px-3 py-2 text-xs text-red-300">
                  {error}
                </div>
              )}

              <div className="flex justify-end gap-2 pt-1">
                <button
                  type="button"
                  className="rounded px-3 py-1.5 text-sm text-white/60 hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
                  onClick={handleClose}
                >
                  Cancel
                </button>
                <button
                  type="button"
                  className="rounded bg-white/10 px-3 py-1.5 text-sm font-medium hover:bg-white/20 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none disabled:opacity-50"
                  onClick={handleTranslate}
                  disabled={
                    submitting ||
                    (mode === "subtitles" && effectiveSourceIndex === null) ||
                    (mode === "audio" && quotaExhausted)
                  }
                >
                  {submitting ? "Starting…" : mode === "audio" ? "Generate" : "Translate"}
                </button>
              </div>

              <p className="text-[11px] leading-relaxed text-white/35">
                {mode === "audio"
                  ? "The audio is transcribed on the server (and translated if the language differs) — longer files take a while. The finished track is saved for everyone."
                  : "Playback pauses while the first lines are translated, then resumes with subtitles streaming in. The finished track is saved for everyone."}
              </p>
            </>
          )}
        </div>
      </div>
    </div>
  );

  return createPortal(modal, document.body);
}
