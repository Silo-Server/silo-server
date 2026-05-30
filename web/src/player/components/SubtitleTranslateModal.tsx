import { useState, useEffect, useCallback, useMemo } from "react";
import { createPortal } from "react-dom";
import { toast } from "sonner";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import type { PlayerSubtitleInfo } from "../types";
import { playerFetch } from "../player-fetch";
import { LANGUAGES, getLanguageName } from "../utils/languageNames";

// Formats the server can parse directly from an external/downloaded file.
const TRANSLATABLE_TEXT_CODECS = new Set(["srt", "subrip", "vtt", "webvtt"]);
// Bitmap codecs the server can't extract as text (it would burn them in).
const BITMAP_CODECS = new Set(["pgs", "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle"]);

// Mirror the server's loadSource acceptance: embedded non-bitmap tracks are
// extracted to text via ffmpeg, while external/downloaded sources must already
// be a parseable text format. Offering anything else starts a job that fails
// asynchronously instead of preventing the choice up front.
export function isTranslatableSource(track: PlayerSubtitleInfo): boolean {
  if (track.live) return false;
  const codec = (track.codec ?? "").toLowerCase();
  if (track.source === "embedded") {
    return !BITMAP_CODECS.has(codec);
  }
  return TRANSLATABLE_TEXT_CODECS.has(codec);
}

interface SubtitleTranslateModalProps {
  mediaFileId: number;
  playerConfig: PlayerConfig;
  tracks: PlayerSubtitleInfo[];
  isOpen: boolean;
  sessionId?: string;
  getStartPosition?: () => number;
  onClose: () => void;
}

function sourceLabel(track: PlayerSubtitleInfo): string {
  const lang = getLanguageName(track.language) || track.language || "Unknown";
  const origin = track.source ? ` · ${track.source}` : "";
  return `${lang}${origin}`;
}

export function SubtitleTranslateModal({
  mediaFileId,
  playerConfig,
  tracks,
  isOpen,
  sessionId,
  getStartPosition,
  onClose,
}: SubtitleTranslateModalProps) {
  // Only offer sources the server can actually translate (excludes live tracks,
  // bitmap embedded tracks, and ASS/non-text external/downloaded tracks).
  const sourceTracks = useMemo(() => tracks.filter(isTranslatableSource), [tracks]);
  const [sourceIndex, setSourceIndex] = useState<number | null>(null);
  const [targetLang, setTargetLang] = useState("en");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const effectiveSourceIndex = sourceIndex ?? sourceTracks[0]?.index ?? null;

  useEffect(() => {
    if (!isOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [isOpen, onClose]);

  const handleTranslate = useCallback(async () => {
    if (effectiveSourceIndex === null) return;
    const source = sourceTracks.find((t) => t.index === effectiveSourceIndex);
    setSubmitting(true);
    setError(null);
    try {
      const res = await playerFetch<{ job?: { status?: string } }>(
        playerConfig,
        "/subtitles/ai/translate",
        {
          method: "POST",
          body: JSON.stringify({
            media_file_id: mediaFileId,
            source_index: effectiveSourceIndex,
            source_language: source?.language ?? "",
            target_language: targetLang,
            session_id: sessionId ?? "",
            start_position: getStartPosition?.() ?? 0,
          }),
        },
      );
      // A request that collapses onto an already-running job (e.g. after a
      // reload, or a second viewer) won't get its own live stream — tell the
      // user it's underway; it'll appear via the subtitle-ready refresh.
      if (res?.job?.status === "running") {
        toast.info(
          "A translation for this track is already in progress — it'll appear when it's ready.",
        );
      }
      // Otherwise the player takes over: it pauses, streams cues in as they're
      // translated, then resumes once your position is covered.
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Couldn't start translation.");
    } finally {
      setSubmitting(false);
    }
  }, [
    effectiveSourceIndex,
    sourceTracks,
    mediaFileId,
    targetLang,
    sessionId,
    getStartPosition,
    playerConfig,
    onClose,
  ]);

  if (!isOpen) return null;

  const modal = (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/80"
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label="Translate subtitles with AI"
    >
      <div
        className="w-full max-w-[440px] rounded-lg bg-neutral-900 text-white shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-white/10 px-4 py-3">
          <h2 className="text-sm font-semibold">Translate subtitles with AI</h2>
          <button
            type="button"
            className="rounded text-white/60 hover:text-white focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
            onClick={onClose}
            aria-label="Close"
          >
            ✕
          </button>
        </div>

        <div className="space-y-3 px-4 py-4">
          {sourceTracks.length === 0 ? (
            <p className="py-4 text-center text-xs text-white/50">
              No text subtitle track is available to translate. Add or download one first.
            </p>
          ) : (
            <>
              <label className="block">
                <span className="mb-1 block text-xs font-medium text-white/60">Translate from</span>
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

              <label className="block">
                <span className="mb-1 block text-xs font-medium text-white/60">Translate to</span>
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

              {error && (
                <div role="alert" className="rounded bg-red-900/40 px-3 py-2 text-xs text-red-300">
                  {error}
                </div>
              )}

              <div className="flex justify-end gap-2 pt-1">
                <button
                  type="button"
                  className="rounded px-3 py-1.5 text-sm text-white/60 hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
                  onClick={onClose}
                >
                  Cancel
                </button>
                <button
                  type="button"
                  className="rounded bg-white/10 px-3 py-1.5 text-sm font-medium hover:bg-white/20 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none disabled:opacity-50"
                  onClick={handleTranslate}
                  disabled={submitting || effectiveSourceIndex === null}
                >
                  {submitting ? "Starting…" : "Translate"}
                </button>
              </div>

              <p className="text-[11px] leading-relaxed text-white/35">
                Playback pauses while the first lines are translated, then resumes with subtitles
                streaming in. The finished track is saved for everyone.
              </p>
            </>
          )}
        </div>
      </div>
    </div>
  );

  return createPortal(modal, document.body);
}
